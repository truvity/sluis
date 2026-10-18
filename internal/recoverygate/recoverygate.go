// Package recoverygate is the runtime switch that turns the recovery sign-in
// off for a whole installation, and the one-way rule it follows
// (docs/decisions/0075).
//
// Recovery is turned ON by configuration (`recovery.enabled`) and, since this
// package, turned OFF either by configuration or at run time by an operator who
// signed in through the identity provider. There is no way to turn it on at run
// time: the only action is [Gate.Disable].
//
// A disable is one record in the State, [Key], holding the SET of configuration
// generations (`recovery.generation`) that have been disabled. Recovery is
// switched off while the generation in force is in the set; a generation that
// is not in it is covered by nothing. So a Lambda cold start, a redeploy or a
// rolled pod that reads the SAME document finds its generation in the set and
// stays off, while an operator who names a generation never disabled has said
// "on" in the one place a reviewed change is made. A stale document can
// therefore not turn recovery back on, and a new one can.
//
// The set only grows. Generations are strings, not an order, so a container
// still running generation A (a rolling deploy) that is asked to disable adds A
// to the set and leaves B where it was: nothing it does can re-enable B. The
// price is that naming a generation that was disabled once keeps recovery off;
// an operator picks a string that was never used.
//
// Every process reads the record, not a local flag. A gate trusts what it read
// for [DefaultTTL] when recovery is on, so a disable reaches every Lambda
// container and every replica within that and a few milliseconds; once it has
// seen the record it never reads again, because a disable is never undone while
// the process runs. A read that fails DENIES recovery (Off answers true with the
// error): recovery is the door that bypasses the directory, and a door that
// cannot be checked stays shut. A caller that would DESTROY something because
// recovery is off (end a session, clear a cookie) does so only when Off answered
// with no error, so a transient fault in the State never ends a session. A State
// that does not support the record at all (the legacy adapter) holds no switch:
// recovery is never off there, and nothing can be disabled.
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

// stored is the value of the State record: every generation ever disabled.
type stored struct {
	Disabled []Record `json:"disabled"`
}

// Record is one disable: what a generation's entry in the set keeps.
type Record struct {
	// Generation is the `recovery.generation` the disable covers.
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
	// Superseded is the latest disable of an earlier generation: one the
	// configuration has since turned back on by naming another generation. Nil
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
	// unsupported is set when the State answered that it cannot hold the
	// record: recovery is then never off, and nothing can be disabled.
	unsupported bool
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

// Durable reports whether a disable can be kept: there is a State, and it has
// not said it cannot hold the record.
func (g *Gate) Durable() bool {
	if g == nil || g.st == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.unsupported
}

// Off reports whether recovery is switched off at run time.
//
// The result is a verdict to DENY on: true means refuse the request. A non-nil
// error means the answer is "could not tell" and is also true; the caller
// refuses but destroys nothing. Only (true, nil) says an operator turned
// recovery off, and only then may a caller end a session or clear a cookie.
func (g *Gate) Off(ctx context.Context) (bool, error) {
	if g == nil || g.st == nil {
		return false, nil
	}
	g.mu.Lock()
	switch {
	case g.off != nil:
		g.mu.Unlock()
		return true, nil
	case g.unsupported:
		g.mu.Unlock()
		return false, nil
	case g.seen && g.now().Sub(g.on) < g.ttl:
		g.mu.Unlock()
		return false, nil
	}
	g.mu.Unlock()

	set, bad, _, err := g.read(ctx)
	if errors.Is(err, ErrUnsupported) {
		g.markUnsupported()
		return false, nil
	}
	if err != nil {
		g.logger().WarnContext(ctx, "the recovery switch could not be read, so the recovery sign-in is refused", slog.Any("error", err))
		return true, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if rec, covered := covering(set, bad, g.generation); covered {
		g.off = rec
		return true, nil
	}
	g.on, g.seen = g.now(), true
	return false, nil
}

func (g *Gate) markUnsupported() {
	g.mu.Lock()
	g.unsupported = true
	g.mu.Unlock()
}

// covering is the entry of set that covers generation, and whether anything does.
// An unreadable record covers every generation, with no entry to show.
func covering(set []Record, bad bool, generation string) (*Record, bool) {
	if bad {
		return &Record{unreadable: true}, true
	}
	for i := range set {
		if set[i].Generation == generation {
			return &set[i], true
		}
	}
	return nil, false
}

// Status reads the record afresh, for the console's page.
func (g *Gate) Status(ctx context.Context) (Status, error) {
	st := Status{Generation: g.Generation(), Durable: g.Durable()}
	if !st.Durable {
		return st, nil
	}
	set, bad, _, err := g.read(ctx)
	if errors.Is(err, ErrUnsupported) {
		g.markUnsupported()
		st.Durable = false
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if rec, covered := covering(set, bad, g.generation); covered {
		st.Disabled = rec
		g.remember(rec)
		return st, nil
	}
	for i := range set {
		if st.Superseded == nil || set[i].At.After(st.Superseded.At) {
			st.Superseded = &set[i]
		}
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

// read returns the set of disabled generations and the record's revision; an
// empty set and no revision when there is no record. bad says the record exists
// and cannot be parsed.
func (g *Gate) read(ctx context.Context) (set []Record, bad bool, rev port.Revision, err error) {
	got, err := g.st.Get(ctx, Key)
	switch {
	case errors.Is(err, port.ErrNotFound):
		return nil, false, "", nil
	case errors.Is(err, port.ErrUnsupported):
		return nil, false, "", fmt.Errorf("%w: %w", ErrUnsupported, err)
	case err != nil:
		return nil, false, "", fmt.Errorf("read the recovery switch: %w", err)
	}
	var rec stored
	if err = json.Unmarshal(got.Value, &rec); err != nil {
		// A record nobody can read still says someone wrote it: it switches
		// recovery off for every generation, and a disable replaces it with a
		// readable one (which is the only way to narrow it).
		return nil, true, got.Revision, nil
	}
	return rec.Disabled, false, got.Revision, nil
}

// Disable switches recovery off for the generation in force, for good until the
// configuration names another one. It adds the generation to the set the record
// holds, by compare-and-swap, and never removes or rewrites an entry: a
// container on an older generation cannot undo a disable of a newer one. It
// returns the entry that stands for the generation and whether this call wrote
// it: a second call, here or in another container, writes nothing and returns
// the first one.
func (g *Gate) Disable(ctx context.Context, by string) (Record, bool, error) {
	if !g.Durable() {
		return Record{}, false, ErrUnsupported
	}
	want := Record{Generation: g.generation, At: g.now().UTC().Truncate(time.Second), By: by}
	for range 8 {
		set, bad, rev, err := g.read(ctx)
		if errors.Is(err, ErrUnsupported) {
			g.markUnsupported()
			return Record{}, false, err
		}
		if err != nil {
			return Record{}, false, err
		}
		if !bad {
			if have, covered := covering(set, false, g.generation); covered {
				g.remember(have)
				return *have, false, nil
			}
		}
		next := stored{Disabled: append(append([]Record(nil), set...), want)}
		if bad {
			// Only an unreadable record is replaced, and by this entry alone.
			next = stored{Disabled: []Record{want}}
		}
		body, err := json.Marshal(next)
		if err != nil {
			return Record{}, false, err
		}
		if rev == "" && !bad {
			_, err = g.st.Create(ctx, Key, body, 0)
		} else {
			_, err = g.st.Update(ctx, Key, body, 0, rev)
		}
		switch {
		case err == nil:
			g.remember(&want)
			return want, true, nil
		case errors.Is(err, port.ErrExists), errors.Is(err, port.ErrConflict), errors.Is(err, port.ErrNotFound):
			continue
		case errors.Is(err, port.ErrUnsupported):
			g.markUnsupported()
			return Record{}, false, fmt.Errorf("%w: %w", ErrUnsupported, err)
		default:
			return Record{}, false, fmt.Errorf("keep the recovery switch: %w", err)
		}
	}
	return Record{}, false, errors.New("recoverygate: the record kept moving; try again")
}
