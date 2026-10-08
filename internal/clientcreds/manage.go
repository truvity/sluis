package clientcreds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// The overlap of a rotation: how long the secret it replaces is still
// accepted, so a relying party is not cut off between the new value being made
// and being deployed. Zero is a hard cut.
const (
	DefaultOverlap = 24 * time.Hour
	MaxOverlap     = 7 * 24 * time.Hour
)

// What a person's request to [Manager] can be refused for. None holds a value.
var (
	// ErrNotGenerated: the client does not have `secret: {generate: true}`.
	ErrNotGenerated = errors.New("clientcreds: the client does not have `secret: {generate: true}`")
	// ErrNoRecord: there is no stored secret for the client (yet).
	ErrNoRecord = errors.New("clientcreds: the client has no stored secret")
	// ErrBusy: another writer holds the client's lease, or changed the record
	// between our read and our write. Nothing was written; try again.
	ErrBusy = errors.New("clientcreds: the client's secret is being changed by another writer")
	// ErrOverlap: the overlap is negative or longer than [MaxOverlap].
	ErrOverlap = errors.New("clientcreds: the overlap is between 0 and 7 days")
	// ErrStillDeclared: the client is still a generated client of the policy,
	// so its record is not an orphan to purge.
	ErrStillDeclared = errors.New("clientcreds: the client is still a generated client of the policy; remove it from the policy first")
)

// Manager is what a person asks of the stored secrets: rotate one, look at its
// metadata, purge an orphan. It never returns or logs a value.
type Manager struct {
	Store port.Secrets
	// Lock serialises writers of one client's record (see [KindLease]). Nil
	// runs unserialised, which is right only for one process.
	Lock Locker
	// Resolver is told to forget a client after its record changes, so this
	// replica serves the new value at once; another replica's cache lapses
	// within [CacheTTL]. May be nil.
	Resolver *Resolver
	// Generated says whether the policy in force declares the client with
	// `secret: {generate: true}`.
	Generated func(clientID string) bool
	// Changed is called after a rotation was written, for what follows from
	// it (an immediate export). May be nil; it must not block.
	Changed func(ctx context.Context, clientID string)
	// Now replaces the clock, for a test.
	Now func() time.Time
	Log *slog.Logger
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) generated(id string) bool { return m.Generated != nil && m.Generated(id) }

func (m *Manager) forget(id string) {
	if m.Resolver != nil {
		m.Resolver.Forget(id)
	}
}

// Rotation is what [Manager.Rotate] did.
type Rotation struct {
	Rotated            time.Time
	Overlap            time.Duration
	PreviousValidUntil time.Time
	// DiscardedPrevious is set when a rotation was made while the one before
	// still had an open overlap: that older secret stopped being accepted.
	DiscardedPrevious bool
}

// Rotate replaces the client's current secret with a new one. The old one is
// accepted for overlap more (zero: not at all). It is refused for a client that
// is not generated, and for one with no record yet.
func (m *Manager) Rotate(ctx context.Context, clientID string, overlap time.Duration) (Rotation, error) {
	res, err := m.rotate(ctx, clientID, overlap)
	countRotation(ctx, rotationOutcome(err))
	if err != nil {
		return Rotation{}, err
	}
	m.forget(clientID)
	if m.Changed != nil {
		m.Changed(ctx, clientID)
	}
	return res, nil
}

func rotationOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrBusy):
		return "busy"
	case errors.Is(err, ErrNotGenerated):
		return "not_generated"
	case errors.Is(err, ErrNoRecord):
		return "no_record"
	default:
		return "failed"
	}
}

func (m *Manager) rotate(ctx context.Context, id string, overlap time.Duration) (Rotation, error) {
	if overlap < 0 || overlap > MaxOverlap {
		return Rotation{}, ErrOverlap
	}
	if !m.generated(id) {
		return Rotation{}, ErrNotGenerated
	}
	if m.Store == nil {
		return Rotation{}, ErrNoRecord
	}
	var (
		res Rotation
		err error
	)
	ran, lerr := withLock(ctx, m.Lock, KindLease, leaseTarget(id), func(held context.Context) {
		res, err = m.rotateHeld(held, id, overlap)
	})
	switch {
	case lerr != nil:
		return Rotation{}, fmt.Errorf("take the lease on the client: %w", lerr)
	case !ran:
		return Rotation{}, ErrBusy
	}
	return res, err
}

// Overlapper is a Secrets store that fixes the overlap of a rotation itself:
// the layout-v4 store keeps no bookkeeping, so the previous secret is whatever
// the document's previous revision is, for the configured grace.
type Overlapper interface{ Overlap() time.Duration }

func (m *Manager) rotateHeld(ctx context.Context, id string, overlap time.Duration) (Rotation, error) {
	if o, ok := m.Store.(Overlapper); ok && overlap > 0 {
		overlap = o.Overlap()
	}
	path := Path(id)
	got, err := m.Store.Get(ctx, path)
	switch {
	case errors.Is(err, port.ErrNotFound):
		return Rotation{}, ErrNoRecord
	case err != nil:
		return Rotation{}, fmt.Errorf("read the record: %w", err)
	}
	rec, err := DecodeRecord(got.Value)
	if err != nil {
		return Rotation{}, err
	}
	next, err := Generate()
	if err != nil {
		return Rotation{}, err
	}
	now := m.now()
	res := Rotation{
		Rotated:           now,
		Overlap:           overlap,
		DiscardedPrevious: rec.Previous != "" && now.Before(rec.PreviousValidUntil),
	}
	rec.Previous, rec.PreviousValidUntil = "", time.Time{}
	if overlap > 0 {
		rec.Previous = rec.Current
		rec.PreviousValidUntil = now.Add(overlap)
		res.PreviousValidUntil = rec.PreviousValidUntil
	}
	rec.Current, rec.Rotated = next, now
	body, err := rec.Encode()
	if err != nil {
		return Rotation{}, err
	}
	switch _, err = m.Store.PutIfVersion(ctx, path, body, got.Version); {
	case err == nil:
		return res, nil
	case errors.Is(err, port.ErrConflict), errors.Is(err, port.ErrNotFound):
		return Rotation{}, ErrBusy
	default:
		return Rotation{}, fmt.Errorf("write the record: %w", err)
	}
}

// Meta is what may be said about a client's stored secret: when, never what.
type Meta struct {
	// Exists is false when the client has no record.
	Exists bool
	// Generated is whether the policy in force declares the client generated.
	Generated          bool
	Created, Rotated   time.Time
	HasPrevious        bool
	PreviousValidUntil time.Time
	// PreviousActive is whether the previous secret is still accepted now.
	PreviousActive bool
	Orphaned       time.Time
}

// Show returns the metadata of a client's record. A client with no record is
// not an error: Exists is false.
func (m *Manager) Show(ctx context.Context, clientID string) (Meta, error) {
	meta := Meta{Generated: m.generated(clientID)}
	if m.Store == nil {
		return meta, nil
	}
	got, err := m.Store.Get(ctx, Path(clientID))
	switch {
	case errors.Is(err, port.ErrNotFound):
		return meta, nil
	case err != nil:
		return Meta{}, fmt.Errorf("read the record: %w", err)
	}
	rec, err := DecodeRecord(got.Value)
	if err != nil {
		return Meta{}, err
	}
	meta.Exists = true
	meta.Created, meta.Rotated, meta.Orphaned = rec.Created, rec.Rotated, rec.Orphaned
	meta.HasPrevious = rec.Previous != ""
	meta.PreviousValidUntil = rec.PreviousValidUntil
	meta.PreviousActive = rec.Previous != "" && m.now().Before(rec.PreviousValidUntil)
	return meta, nil
}

// Purge deletes the record of a client that is no longer a generated client of
// the policy. The copy an export made is the export's: the export is declared
// with the client, so it is gone with it and nothing here knows where it went.
func (m *Manager) Purge(ctx context.Context, clientID string) error {
	err := m.purge(ctx, clientID)
	switch {
	case err == nil:
		countPurge(ctx, "ok")
	case errors.Is(err, ErrStillDeclared):
		countPurge(ctx, "still_declared")
	case errors.Is(err, ErrNoRecord):
		countPurge(ctx, "no_record")
	case errors.Is(err, ErrBusy):
		countPurge(ctx, "busy")
	default:
		countPurge(ctx, "failed")
	}
	if err == nil {
		m.forget(clientID)
	}
	return err
}

func (m *Manager) purge(ctx context.Context, id string) error {
	if m.generated(id) {
		return ErrStillDeclared
	}
	if m.Store == nil {
		return ErrNoRecord
	}
	var err error
	ran, lerr := withLock(ctx, m.Lock, KindLease, leaseTarget(id), func(held context.Context) {
		// Looked at again under the lease: a purge of nothing is said so.
		if _, gerr := m.Store.Get(held, Path(id)); gerr != nil {
			if errors.Is(gerr, port.ErrNotFound) {
				err = ErrNoRecord
				return
			}
			err = fmt.Errorf("read the record: %w", gerr)
			return
		}
		if derr := m.Store.Delete(held, Path(id)); derr != nil {
			err = fmt.Errorf("delete the record: %w", derr)
		}
	})
	switch {
	case lerr != nil:
		return fmt.Errorf("take the lease on the client: %w", lerr)
	case !ran:
		return ErrBusy
	}
	return err
}
