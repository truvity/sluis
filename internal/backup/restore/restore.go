package restore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
)

// Errors a caller can match with errors.Is.
var (
	// ErrNotEmpty is a destination that holds a different value for a record
	// of the archive, and no overwrite was asked for. Nothing was written.
	ErrNotEmpty = errors.New("restore: the destination holds different data (use overwrite)")
	// ErrVerify is a restore whose read-back differs from the archive.
	ErrVerify = errors.New("restore: the destination differs from the archive after the restore")
	// ErrArchive is a record the archive should not hold: another module's
	// key, a name outside the module's blob prefixes, an unknown namespace.
	ErrArchive = errors.New("restore: the archive holds a record that does not belong")
)

// Table is the State and the Index of one module's table.
type Table interface {
	port.State
	port.Index
}

// Target is the installation a backup is restored into.
type Target struct {
	// Table returns the table of a module.
	Table func(port.Module) Table
	// Secrets are the installation's secrets on layout v5.
	Secrets *secretstore.StoresV5
	// Blob holds the modules' blobs.
	Blob port.Blob
}

// Options are the optional parts of a run.
type Options struct {
	// Overwrite lets Apply replace a different value in the destination.
	Overwrite bool
	// Now is the clock the remaining lifetimes are computed with.
	Now func() time.Time
	// MaxItems bounds the names listed per section of the report; the default
	// is 100. The counts are always exact.
	MaxItems int
}

type mode int

const (
	modePlan mode = iota
	modeWrite
	modeVerify
)

// regenerated are the kinds the modules rebuild.
var regenerated = map[string]bool{"lease": true, "notify": true, "gate": true, "cache": true, "dedupe": true,
	"maintenance": true}

// ownRecord reports whether the key is one of the backup module's own run
// records or its retention marker: they describe the installation that was
// backed up, not the one restored into, and the module writes them afresh.
func ownRecord(a port.Address5) bool {
	return a.Module == port.ModuleBackup && (a.Kind == "run" || a.Kind == "retention")
}

// Preview verifies the archive and reports what Apply would do. It writes
// nothing.
func Preview(ctx context.Context, r *backup.Reader, t Target, opt Options) (*Report, error) {
	p, err := newRun(r, t, opt)
	if err != nil {
		return nil, err
	}
	if err := r.Verify(ctx); err != nil {
		return nil, err
	}
	return p.walk(ctx, modePlan)
}

// Apply restores the archive. The archive is verified before any write; a
// different value in the destination refuses the restore unless
// [Options.Overwrite]; afterwards every record is read back. The report is the
// plan that was carried out: Create and Overwrite count the writes.
func Apply(ctx context.Context, r *backup.Reader, t Target, opt Options) (*Report, error) {
	p, err := newRun(r, t, opt)
	if err != nil {
		return nil, err
	}
	if err := r.Verify(ctx); err != nil {
		return nil, err
	}
	plan, err := p.walk(ctx, modePlan)
	if err != nil {
		return nil, err
	}
	if n := plan.Totals().Overwrite; n > 0 && !opt.Overwrite {
		return plan, fmt.Errorf("%w: %d records differ", ErrNotEmpty, n)
	}
	rep, err := p.walk(ctx, modeWrite)
	if err != nil {
		return rep, err
	}
	back, err := p.walk(ctx, modeVerify)
	if err != nil {
		return rep, err
	}
	if t := back.Totals(); t.Create+t.Overwrite > 0 {
		return back, fmt.Errorf("%w: %d missing, %d different", ErrVerify, t.Create, t.Overwrite)
	}
	return rep, nil
}

type run struct {
	r   *backup.Reader
	t   Target
	opt Options
	now func() time.Time
	max int

	// per walk
	mode mode
	rep  *Report
	cur  *SectionReport
	at   time.Time
}

func newRun(r *backup.Reader, t Target, opt Options) (*run, error) {
	if r == nil || t.Table == nil || t.Secrets == nil || t.Blob == nil {
		return nil, errors.New("restore: a reader and a target with Table, Secrets and Blob are required")
	}
	p := &run{r: r, t: t, opt: opt, now: opt.Now, max: opt.MaxItems}
	if p.now == nil {
		p.now = time.Now
	}
	if p.max <= 0 {
		p.max = 100
	}
	return p, nil
}

// order lists the modules of the archive in write order: oidc last.
func (p *run) order() []port.Module {
	var out []port.Module
	for _, m := range port.Modules() {
		if m != port.ModuleOIDC && slices.ContainsFunc(p.r.Manifest().Modules, func(e backup.ModuleEntry) bool { return e.Module == m }) {
			out = append(out, m)
		}
	}
	if slices.ContainsFunc(p.r.Manifest().Modules, func(e backup.ModuleEntry) bool { return e.Module == port.ModuleOIDC }) {
		out = append(out, port.ModuleOIDC)
	}
	return out
}

// which records of the state section a pass takes.
type which int

const (
	all which = iota
	noKeys
	onlyKeys
)

func (p *run) walk(ctx context.Context, m mode) (*Report, error) {
	mf := p.r.Manifest()
	p.mode = m
	p.at = p.now()
	p.rep = &Report{Installation: mf.Installation, ID: mf.ID, Layout: mf.Layout, Created: mf.Created}
	for _, mod := range p.order() {
		steps := []struct {
			s backup.Section
			w which
		}{{backup.Secrets, all}, {backup.Blobs, all}, {backup.State, all}}
		if mod == port.ModuleOIDC {
			steps[2].w = noKeys
			steps = append(steps, struct {
				s backup.Section
				w which
			}{backup.State, onlyKeys})
		}
		for _, st := range steps {
			if err := p.section(ctx, mod, st.s, st.w); err != nil {
				return p.rep, fmt.Errorf("restore %s/%s: %w", mod, st.s, err)
			}
		}
	}
	return p.rep, nil
}

func (p *run) section(ctx context.Context, mod port.Module, s backup.Section, w which) error {
	// The report has one entry per module and section, shared by the two
	// state passes of oidc.
	i := slices.IndexFunc(p.rep.Sections, func(e SectionReport) bool { return e.Module == mod && e.Section == s })
	if i < 0 {
		p.rep.Sections = append(p.rep.Sections, SectionReport{Module: mod, Section: s})
		i = len(p.rep.Sections) - 1
	}
	p.cur = &p.rep.Sections[i]
	var asm export.Assembler
	err := p.r.Records(ctx, s, mod, func(raw []byte) error {
		rec, err := export.Decode(s, raw)
		if err != nil {
			return err
		}
		switch r := rec.(type) {
		case export.State:
			if (w == onlyKeys) != isKeyring(r.Key, false) && w != all {
				return nil
			}
			return p.state(ctx, mod, r)
		case export.Index:
			if (w == onlyKeys) != isKeyring(r.Key, true) && w != all {
				return nil
			}
			return p.index(ctx, mod, r)
		case export.Secret:
			return p.secret(ctx, mod, r)
		case export.Blob:
			name, body, done, err := asm.Add(r)
			if err != nil || !done {
				return err
			}
			return p.blob(ctx, mod, name, body)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if asm.Pending() {
		return fmt.Errorf("%w: a blob ends before its last part", export.ErrRecord)
	}
	return nil
}

// isKeyring reports whether a key belongs to the signing key ring.
func isKeyring(key string, set bool) bool {
	var a port.Address5
	var err error
	if set {
		a, err = port.LocateSet5(key)
	} else {
		a, err = port.Locate5(key)
	}
	return err == nil && strings.HasPrefix(a.Kind, "keyring")
}

// remaining turns an absolute expiry into what is left. skip is true for a
// record that has expired.
func (p *run) remaining(expires string) (ttl time.Duration, skip bool, err error) {
	at, err := export.ExpiresAt(expires)
	if err != nil || at.IsZero() {
		return 0, false, err
	}
	ttl = at.Sub(p.at)
	return ttl, ttl <= 0, nil
}

func (p *run) note(a Action, typ, name, version string) {
	p.cur.add(a, Item{Type: typ, Name: name, Version: version}, p.max)
}

// settle applies the outcome of comparing one record: it counts it and, in
// write mode, calls write when the destination lacks the archive's value.
func (p *run) settle(a Action, typ, name, version string, write func() error) error {
	p.note(a, typ, name, version)
	if p.mode == modeWrite && (a == Create || a == Overwrite) {
		if a == Overwrite && !p.opt.Overwrite {
			return fmt.Errorf("%w: %s %s", ErrNotEmpty, typ, name)
		}
		return write()
	}
	return nil
}

func (p *run) state(ctx context.Context, mod port.Module, r export.State) error {
	a, err := port.Locate5(r.Key)
	if err != nil {
		return fmt.Errorf("%w: key %q", ErrArchive, r.Key)
	}
	if regenerated[a.Kind] || ownRecord(a) {
		p.note(Regenerated, TypeState, r.Key, "")
		return nil
	}
	if a.Module != mod {
		return fmt.Errorf("%w: key %q is not a %s record", ErrArchive, r.Key, mod)
	}
	ttl, skip, err := p.remaining(r.Expires)
	if err != nil {
		return err
	}
	if skip {
		p.note(Expired, TypeState, r.Key, "")
		return nil
	}
	tab := p.t.Table(mod)
	live, err := tab.Get(ctx, r.Key)
	switch {
	case errors.Is(err, port.ErrNotFound):
		return p.settle(Create, TypeState, r.Key, "", func() error {
			_, err := tab.Put(ctx, r.Key, r.Value, ttl)
			return err
		})
	case err != nil:
		return fmt.Errorf("read %s: %w", r.Key, err)
	case bytes.Equal(live.Value, r.Value):
		p.note(Same, TypeState, r.Key, "")
		return nil
	}
	return p.settle(Overwrite, TypeState, r.Key, string(live.Revision), func() error {
		_, err := tab.Put(ctx, r.Key, r.Value, ttl)
		return err
	})
}

func (p *run) index(ctx context.Context, mod port.Module, r export.Index) error {
	a, err := port.LocateSet5(r.Key)
	if err != nil || a.Module != mod {
		return fmt.Errorf("%w: set %q is not a %s set", ErrArchive, r.Key, mod)
	}
	ttl, skip, err := p.remaining(r.Expires)
	if err != nil {
		return err
	}
	if skip {
		p.note(Expired, TypeIndex, r.Key, "")
		return nil
	}
	tab := p.t.Table(mod)
	live, err := tab.Members(ctx, r.Key)
	if err != nil {
		return fmt.Errorf("read %s: %w", r.Key, err)
	}
	want := slices.Clone(r.Members)
	sort.Strings(want)
	want = slices.Compact(want)
	sort.Strings(live)
	write := func() error {
		for _, m := range live {
			if _, ok := slices.BinarySearch(want, m); !ok {
				if err := tab.Remove(ctx, r.Key, m); err != nil {
					return err
				}
			}
		}
		for _, m := range want {
			if err := tab.Add(ctx, r.Key, m, ttl); err != nil {
				return err
			}
		}
		return nil
	}
	switch {
	case slices.Equal(live, want):
		p.note(Same, TypeIndex, r.Key, "")
		return nil
	case len(live) == 0:
		return p.settle(Create, TypeIndex, r.Key, "", write)
	}
	return p.settle(Overwrite, TypeIndex, r.Key, "", write)
}

// canon is a JSON document in a form that compares by meaning.
func canon(b []byte) []byte {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}

func (p *run) secret(ctx context.Context, mod port.Module, r export.Secret) error {
	sm := secretstore.Module(mod)
	var st state.Store
	switch {
	case r.NS == export.Internal:
		st = p.t.Secrets.InternalStore(sm)
	case r.NS == export.External && secretstore.ModuleExternal(sm):
		st = p.t.Secrets.ExternalStore(sm)
	default:
		return fmt.Errorf("%w: secret namespace %q of %s", ErrArchive, r.NS, mod)
	}
	name := r.NS + "/" + string(mod) + "/" + r.Name
	live, err := st.Get(ctx, r.Name)
	switch {
	case errors.Is(err, state.ErrNotFound):
		return p.settle(Create, TypeSecret, name, "", func() error {
			_, err := st.Put(ctx, r.Name, r.Doc, "")
			return err
		})
	case err != nil:
		return fmt.Errorf("read secret %s: %w", name, err)
	case bytes.Equal(canon(live.Value), canon(r.Doc)):
		p.note(Same, TypeSecret, name, "")
		return nil
	}
	return p.settle(Overwrite, TypeSecret, name, string(live.Rev), func() error {
		_, err := st.Put(ctx, r.Name, r.Doc, live.Rev)
		return err
	})
}

func (p *run) blob(ctx context.Context, mod port.Module, name string, body []byte) error {
	if !slices.ContainsFunc(export.BlobPrefixes[mod], func(pre string) bool { return strings.HasPrefix(name, pre) }) {
		return fmt.Errorf("%w: blob %q is not under a %s prefix", ErrArchive, name, mod)
	}
	live, err := p.t.Blob.Read(ctx, name)
	write := func() error {
		// The assembler reuses body for the next object; an adapter may keep what it is given.
		_, err := p.t.Blob.Write(ctx, name, bytes.Clone(body))
		return err
	}
	switch {
	case errors.Is(err, port.ErrNotFound):
		return p.settle(Create, TypeBlob, name, "", write)
	case err != nil:
		return fmt.Errorf("read blob %s: %w", name, err)
	case bytes.Equal(live.Body, body):
		p.note(Same, TypeBlob, name, "")
		return nil
	}
	return p.settle(Overwrite, TypeBlob, name, live.Version, write)
}
