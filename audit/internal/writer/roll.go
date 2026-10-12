package writer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/truvity/sluis/audit/internal/recobj"
	"github.com/truvity/sluis/audit/internal/ulid"
	"github.com/truvity/sluis/audit/profile"
	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/audit/store"
)

// Roller gathers copies into objects and puts them.
//
// One object is one ingest batch of one profile and one tenant, and its key is
// the moment the batch was taken, not the time of any record in it:
//
//	records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>
//
// (docs/reference/audit/bucket-contract.md). The profile is first, and deliberately:
// an object store's lifecycle rules filter by literal prefix and take no
// wildcards, so a rule that moves one profile's objects to colder storage after
// its hot window can only exist if the profile is the leading component.
// Per-tenant credentials are still expressible, because a policy's resource
// may carry a wildcard where a lifecycle filter may not.
//
// The ULID is made when the put starts, from the same clock the hour is read
// from, and never goes backwards within a writer, so within an hour the keys
// sort in the order the batches were taken.
type Roller struct {
	// Store is where objects go: the archive, which with several install presets
	// is the routed store that puts each profile's objects in its preset's.
	Store store.Store
	// Instance names this writer in its own records. It is not in any key: the
	// ULID is what keeps two writers' keys apart.
	Instance string
	// Interval rolls an object that has been open this long. Default 5m.
	Interval time.Duration
	// MaxBytes rolls an object that has grown this large, measured before
	// compression. Default 8 MiB.
	MaxBytes int
	// Held reports whether a profile and tenant are under a legal hold. An
	// object written under a held prefix is put with the hold already on it: a
	// hold is placed on a prefix, objects keep arriving under it, and one held
	// only by a later sweep was deletable in between.
	Held func(profile, tenant string) bool
	// Now is the clock, for tests.
	Now func() time.Time
	// OnPut is called after each object is written.
	OnPut func(key string, records int)
	// OnRedirect is called when an object's hour was already sealed and the
	// object was put into a later, open hour instead: from is the hour the
	// clock named, to the hour used.
	OnRedirect func(profile, tenant string, from, to time.Time)
	// OnSealProbeError is called when the writer could not tell whether an
	// hour is sealed (the store failed, or the writer may not read seals/).
	// The object is then put into the hour the clock names: the check is a
	// guard against a late object, and is not allowed to stop the trail.
	OnSealProbeError func(profile, tenant string, hour time.Time, err error)

	mu   sync.Mutex
	open map[partition]*batch
	ids  ulid.Generator
}

type partition struct {
	profile string
	tenant  string
}

type batch struct {
	profile *profile.Profile
	opened  time.Time
	bytes   int
	// lines are the object's lines, hash and record, in the order the copies
	// were added.
	lines    [][]byte
	retainAt time.Time
}

// ErrKeyComponent is returned for a profile or a tenant that cannot be a key
// component: one with a slash in it, or none at all.
var ErrKeyComponent = errors.New("writer: not usable in a key")

// Add puts one copy into the object being gathered for its profile and tenant,
// rolling that object first if it is full or old.
//
// Nothing here indexes: the index is observe's, which follows the bucket
// (docs/decisions/0062), so an object is complete when it is put.
func (r *Roller) Add(ctx context.Context, p *profile.Profile, c *record.Record) error {
	return r.AddExpiring(ctx, p, c, nil)
}

// AddExpiring is Add for a record that says when the credential it is about
// expires. Under an after_expiry profile the object is then locked until that
// moment plus the profile's years — or the fallback, if that is later — and an
// object holding several such records is locked for the latest, because every
// record in it must outlive what relies on it. Under any other profile the
// expiry changes nothing.
func (r *Roller) AddExpiring(
	ctx context.Context, p *profile.Profile, c *record.Record, expiry *time.Time,
) error {
	line, err := record.Canonical(c)
	if err != nil {
		return fmt.Errorf("writer: %w", err)
	}
	key := partition{profile: p.Name, tenant: tenantOf(c)}
	for kind, part := range map[string]string{"profile": key.profile, "tenant": key.tenant} {
		if why := store.KeyComponent(part); why != "" {
			return fmt.Errorf("%w: the %s %q %s", ErrKeyComponent, kind, part, why)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()

	b, ok := r.open[key]
	if ok && r.full(b) {
		if err := r.put(ctx, key, b); err != nil {
			return err
		}
		ok = false
	}
	if !ok {
		b = &batch{
			profile: p,
			opened:  r.now(),
			// The retention is fixed when the object is opened, not when it is
			// written, so every copy in it is kept at least as long as the
			// profile asks of the oldest.
			retainAt: p.RetainUntil(r.now(), nil),
		}
		r.open[key] = b
	}
	if expiry != nil {
		if until := p.RetainUntil(b.opened, expiry); until.After(b.retainAt) {
			b.retainAt = until
		}
	}
	b.lines = append(b.lines, recobj.EncodeLine(line))
	b.bytes += len(line) + 1
	return nil
}

// Flush rolls every open object. It is called on a timer, before the writer
// acknowledges anything it must not lose, and on shutdown.
func (r *Roller) Flush(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()

	keys := make([]partition, 0, len(r.open))
	for k := range r.open {
		keys = append(keys, k)
	}
	// A stable order so that a failure leaves the same objects written every
	// time, which is what makes a retry after one predictable.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].profile != keys[j].profile {
			return keys[i].profile < keys[j].profile
		}
		return keys[i].tenant < keys[j].tenant
	})
	for _, k := range keys {
		if err := r.put(ctx, k, r.open[k]); err != nil {
			return err
		}
	}
	return nil
}

// Due reports whether anything has been open long enough to roll.
func (r *Roller) Due() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.open {
		if r.full(b) {
			return true
		}
	}
	return false
}

// Pending is how many copies are gathered but not yet written.
func (r *Roller) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, b := range r.open {
		n += len(b.lines)
	}
	return n
}

// Close is called when the writer is done. The compressor is shared by the
// process, so there is nothing of the roller's to release.
func (r *Roller) Close() error { return nil }

// put writes one object and forgets the batch. The caller holds the lock.
func (r *Roller) put(ctx context.Context, key partition, b *batch) error {
	if b == nil || len(b.lines) == 0 {
		delete(r.open, key)
		return nil
	}
	body, metadata := recobj.Encode(b.lines)

	// The batch is taken now: the ULID and the hour come from one moment, which
	// is the ingest time the contract keys by.
	id, at, err := r.nextOpen(ctx, key, r.now())
	if err != nil {
		return err
	}
	objectKey := store.RecordKey(key.profile, key.tenant, at, id)

	err = r.Store.Put(ctx, store.Object{
		Key:         objectKey,
		Body:        body,
		RetainUntil: b.retainAt,
		LegalHold:   r.Held != nil && r.Held(b.profile.Name, key.tenant),
		ContentType: recobj.ContentType,
		Encoding:    recobj.Encoding,
		Metadata:    metadata,
	})
	if err != nil {
		// The batch stays open. Losing it here would lose records the writer
		// has already taken responsibility for.
		return fmt.Errorf("writer: put %s: %w", objectKey, err)
	}
	if r.OnPut != nil {
		r.OnPut(objectKey, len(b.lines))
	}
	delete(r.open, key)
	return nil
}

// maxRedirect bounds how many sealed hours an object is moved past. A writer
// whose clock is two days behind the notary's seals is broken, and says so.
const maxRedirect = 48

// nextOpen makes the ULID and the moment it encodes, and moves that moment into
// the first hour that has no seal.
//
// The notary writes one seal per profile, tenant and hour and never rewrites it
// (docs/decisions/0061), so an object that lands in a sealed hour is covered by
// nothing: a writer clock behind the notary's, or a batch held across the
// notary's settle time, is a mismatch for good. The key's hour is ingest time,
// and the record's own times are inside it, so an object put into a later hour
// loses nothing a reader uses; the ULID is made for the start of that hour,
// which keeps the hour and the ULID agreeing (store.ParseRecordKey) and the
// generator, which never goes backwards, from taking the writer back into the
// sealed hour afterwards.
func (r *Roller) nextOpen(ctx context.Context, key partition, now time.Time) (string, time.Time, error) {
	var from time.Time
	at := now
	for i := 0; ; i++ {
		id, got, err := r.ids.New(at)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("writer: %w", err)
		}
		hour := got.Truncate(time.Hour)
		if from.IsZero() {
			from = hour
		}
		sealed, err := r.sealed(ctx, key, hour)
		if err != nil {
			if r.OnSealProbeError != nil {
				r.OnSealProbeError(key.profile, key.tenant, hour, err)
			}
			return id, got, nil
		}
		if !sealed {
			if hour.After(from) && r.OnRedirect != nil {
				r.OnRedirect(key.profile, key.tenant, from, hour)
			}
			return id, got, nil
		}
		if i >= maxRedirect {
			return "", time.Time{}, fmt.Errorf("writer: the hours from %s on are sealed for %s/%s: the clock is too far behind the notary",
				from.Format(time.RFC3339), key.profile, key.tenant)
		}
		at = hour.Add(time.Hour)
	}
}

// sealed reports whether the hour already has a seal.
func (r *Roller) sealed(ctx context.Context, key partition, hour time.Time) (bool, error) {
	_, err := r.Store.Head(ctx, store.SealKey(key.profile, key.tenant, hour))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	}
	return false, err
}

func (r *Roller) ensure() {
	if r.open == nil {
		r.open = map[partition]*batch{}
	}
	if r.Instance == "" {
		r.Instance = record.InstanceName()
	}
}

func (r *Roller) full(b *batch) bool {
	interval := r.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	limit := r.MaxBytes
	if limit <= 0 {
		limit = 8 << 20
	}
	return b.bytes >= limit || r.now().Sub(b.opened) >= interval
}

func (r *Roller) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func tenantOf(c *record.Record) string {
	if t := c.GetTenantId(); t != "" {
		return t
	}
	// A copy whose profile drops the tenant still has to land somewhere, and
	// the platform partition is where records with no customer belong.
	return record.TenantPlatform
}
