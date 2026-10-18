// Package recoverygate is the runtime switch that turns the recovery sign-in
// off for a whole installation, and the one-way rule it follows
// (docs/decisions/0075).
//
// Recovery is turned ON by configuration (`recovery.enabled`) and, since this
// package, turned OFF either by configuration or at run time by an operator who
// signed in through the identity provider. There is no way to turn it on at run
// time: the only action is [Gate.Disable].
//
// A disable is one record in the State, [Key], that names the configuration
// GENERATION it overrode (`recovery.generation`). Recovery is switched off
// while a record exists for the generation in force, and a record for any other
// generation covers nothing. So a Lambda cold start, a redeploy or a rolled pod
// that reads the SAME document finds the record and stays off, while an
// operator who changes the generation on purpose has said "on" in the one place
// a reviewed change is made. A stale document can therefore not turn recovery
// back on, and a new one can.
//
// Every process reads the record, not a local flag. A gate trusts what it read
// for [DefaultTTL] when recovery is on, so a disable reaches every Lambda
// container and every replica within that and a few milliseconds; once it has
// seen the record it never reads again, because a disable is never undone while
// the process runs. A read that fails answers "off": recovery is the door that
// bypasses the directory, and a door that cannot be checked stays shut.
package recoverygate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// Key is the State key of the record. It is in the oidc module's own table (the
// issuer and the console are one binary) and is permanent: it is the rec.
// family.
const Key = "rec.recovery.off"

// DefaultTTL is how long a gate trusts "recovery is on". It bounds how late
// another container learns of a disable.
const DefaultTTL = 3 * time.Second

// ErrUnsupported is a State that cannot hold the record (the legacy adapter,
// or no State at all). A disable that cannot be kept would be gone at the next
// restart, so it is refused rather than half done.
var ErrUnsupported = errors.New("recoverygate: this installation's State cannot keep the record of a disable")

// Record is what a disable keeps.
type Record struct {
	// Generation is the `recovery.generation` the disable overrode.
	Generation string `json:"generation"`
	// At is when the disable was made.
	At time.Time `json:"at"`
	// By is who made it: the operator's address.
	By string `json:"by"`

	// unreadable marks a record that exists and cannot be parsed. It covers every
	// generation: something wrote the switch, and a record nobody can read must
	// not turn recovery on.
	unreadable bool
}

// Covers reports whether the record switches recovery off for a generation.
func (r Record) Covers(generation string) bool { return r.unreadable || r.Generation == generation }

// Status is what the gate knows now.
type Status struct {
	// Disabled is the record that covers the generation in force, or nil.
	Disabled *Record
	// Superseded is a record of an earlier generation: a disable the
	// configuration has since turned back on by changing the generation. Nil
	// when there is none, or when Disabled is set.
	Superseded *Record
	// Generation is the `recovery.generation` in force.
	Generation string
	// Durable is whether a disable can be kept: the State can hold the record.
	Durable bool
}

// Gate reads and writes the record. The zero value is not usable; a nil *Gate
// is, and never switches anything off (a process with no shared State, a test).
type Gate struct {
	st         port.State
	generation string
	ttl        time.Duration
	now        func() time.Time
	log        *slog.Logger

	mu   sync.Mutex
	off  *Record   // set once seen; never cleared
	on   time.Time // when "no covering record" was last read
	seen bool
}

// Option adjusts a gate.
type Option func(*Gate)

// WithTTL sets how long "on" is trusted. Zero or less is [DefaultTTL].
func WithTTL(d time.Duration) Option { return func(g *Gate) { g.ttl = d } }

// WithClock sets the clock, for a test.
func WithClock(now func() time.Time) Option { return func(g *Gate) { g.now = now } }

// WithLog sets the logger a failed read is reported to; nil is the default one.
func WithLog(l *slog.Logger) Option { return func(g *Gate) { g.log = l } }

// New returns the gate over the State, for the configuration generation in
// force. A nil State is a gate that can switch nothing off and cannot disable.
func New(st port.State, generation string, opts ...Option) *Gate {
	g := &Gate{st: st, generation: generation, ttl: DefaultTTL, now: time.Now}
	for _, o := range opts {
		o(g)
	}
	if g.ttl <= 0 {
		g.ttl = DefaultTTL
	}
	return g
}

func (g *Gate) logger() *slog.Logger {
	if g.log != nil {
		return g.log
	}
	return slog.Default()
}

// Generation is the configuration generation the gate was built for.
func (g *Gate) Generation() string {
	if g == nil {
		return ""
	}
	return g.generation
}

// Durable reports whether a disable can be kept.
func (g *Gate) Durable() bool { return g != nil && g.st != nil }

// Off reports whether recovery is switched off at run time. A State that cannot
// be read answers true (see the package comment); the error is returned so a
// caller can say why.
func (g *Gate) Off(ctx context.Context) (bool, error) {
	if g == nil || g.st == nil {
		return false, nil
	}
	g.mu.Lock()
	if g.off != nil {
		g.mu.Unlock()
		return true, nil
	}
	if g.seen && g.now().Sub(g.on) < g.ttl {
		g.mu.Unlock()
		return false, nil
	}
	g.mu.Unlock()

	rec, _, err := g.read(ctx)
	if err != nil {
		g.logger().WarnContext(ctx, "the recovery switch could not be read, so the recovery sign-in is refused", slog.Any("error", err))
		return true, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if rec != nil && rec.Covers(g.generation) {
		g.off = rec
		return true, nil
	}
	g.on, g.seen = g.now(), true
	return false, nil
}

// Status reads the record afresh, for the console's page.
func (g *Gate) Status(ctx context.Context) (Status, error) {
	st := Status{Generation: g.Generation(), Durable: g.Durable()}
	if !st.Durable {
		return st, nil
	}
	rec, _, err := g.read(ctx)
	if err != nil {
		return st, err
	}
	switch {
	case rec == nil:
	case rec.Covers(g.generation):
		st.Disabled = rec
		g.remember(rec)
	default:
		st.Superseded = rec
	}
	return st, nil
}

// remember keeps a covering record, so this process answers "off" without a read.
func (g *Gate) remember(rec *Record) {
	g.mu.Lock()
	g.off = rec
	g.mu.Unlock()
}

// Forget clears what the gate holds. For a test that simulates a new process.
func (g *Gate) Forget() {
	g.mu.Lock()
	g.off, g.seen = nil, false
	g.mu.Unlock()
}

// read returns the record and its revision; both zero when there is none.
func (g *Gate) read(ctx context.Context) (*Record, port.Revision, error) {
	got, err := g.st.Get(ctx, Key)
	switch {
	case errors.Is(err, port.ErrNotFound):
		return nil, "", nil
	case errors.Is(err, port.ErrUnsupported):
		return nil, "", fmt.Errorf("%w: %w", ErrUnsupported, err)
	case err != nil:
		return nil, "", fmt.Errorf("read the recovery switch: %w", err)
	}
	var rec Record
	if err = json.Unmarshal(got.Value, &rec); err != nil {
		// A record nobody can read still says someone wrote it: it switches
		// recovery off, and the next disable replaces it with a readable one.
		return &Record{unreadable: true}, got.Revision, nil
	}
	return &rec, got.Revision, nil
}

// Disable switches recovery off for the generation in force, for good until the
// configuration changes it. It returns the record that stands and whether this
// call wrote it: a second call, here or in another container, writes nothing
// and returns the first one.
func (g *Gate) Disable(ctx context.Context, by string) (Record, bool, error) {
	if !g.Durable() {
		return Record{}, false, ErrUnsupported
	}
	want := Record{Generation: g.generation, At: g.now().UTC().Truncate(time.Second), By: by}
	body, err := json.Marshal(want)
	if err != nil {
		return Record{}, false, err
	}
	for range 5 {
		have, rev, err := g.read(ctx)
		if err != nil {
			return Record{}, false, err
		}
		if have != nil && !have.unreadable && have.Covers(g.generation) {
			g.remember(have)
			return *have, false, nil
		}
		if have == nil {
			_, err = g.st.Create(ctx, Key, body, 0)
		} else {
			// An earlier generation's record, or an unreadable one: replaced,
			// and the audit trail keeps the history.
			_, err = g.st.Update(ctx, Key, body, 0, rev)
		}
		switch {
		case err == nil:
			g.remember(&want)
			return want, true, nil
		case errors.Is(err, port.ErrExists), errors.Is(err, port.ErrConflict), errors.Is(err, port.ErrNotFound):
			continue
		case errors.Is(err, port.ErrUnsupported):
			return Record{}, false, fmt.Errorf("%w: %w", ErrUnsupported, err)
		default:
			return Record{}, false, fmt.Errorf("keep the recovery switch: %w", err)
		}
	}
	return Record{}, false, errors.New("recoverygate: the record kept moving; try again")
}
