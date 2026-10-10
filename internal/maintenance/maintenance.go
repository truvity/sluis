// Package maintenance is the flag that says a module is being restored, and the
// gate every module reads before it writes (docs/decisions/0072, decision 9).
//
// The flag is one record in each module's OWN table, [Key], so a module needs
// no grant on another module's table to read it. Only the restore function and
// an operator (`sluis maintenance`, with the grant of the Pulumi library's
// MaintenanceOperatorStatements) write it: the library denies every function role
// a write of the `maintenance` partition of its own table. A migration freeze
// ([StateMigration]) also serves a read-only token path ([Gate.MiddlewareFrozen]). A module reads it through a [Gate],
// which caches the answer for a short time (a Lambda function reads it at most
// every [DefaultTTL] however many requests it serves) and refuses writes while
// it is set. Reads of what was already issued keep working.
//
// A flag that cannot be read fails CLOSED for a write and OPEN for a read: a
// module that cannot tell whether a restore is running must not write over it,
// and need not stop verifying tokens.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// Key is the State key of the flag, in every module's table. The layout puts it
// in the kind `maintenance`, which the Pulumi library keys its deny on.
const Key = "rec.maintenance"

// DefaultTTL is how long a gate trusts what it read.
const DefaultTTL = 10 * time.Second

// RetryAfter is the Retry-After a refused HTTP request carries, in seconds.
const RetryAfter = 30

// The values of [Flag.State].
const (
	// StateRestoring is a restore in progress, or one that failed and left the
	// module closed until an operator looks.
	StateRestoring = "restoring"
	// StateMigration is a migration freeze, set by `sluis maintenance on`: the
	// installation refuses every write of state, and still answers what deploy
	// tooling and verification read ([Gate.MiddlewareFrozen], [ReadOnly]).
	StateMigration = "migration"
)

// Code is the name a refusal has where a caller answers it by name.
const Code = "maintenance"

// Flag is the record.
type Flag struct {
	State  string    `json:"state"`
	Since  time.Time `json:"since"`
	By     string    `json:"by,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// Active reports whether the flag is set.
func (f Flag) Active() bool { return f.State != "" }

// Frozen reports whether the flag is a migration freeze, the one state in which
// a read-only token path is served.
func (f Flag) Frozen() bool { return f.State == StateMigration }

// readOnlyKey marks the context of a request served during a migration freeze.
type readOnlyKey struct{}

// WithReadOnly marks ctx as the context of a request that must change no
// state: a store that would record a token, rotate a refresh token or note a
// client among a sign-in's clients leaves that out.
func WithReadOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnlyKey{}, true)
}

// ReadOnly reports whether ctx was marked by [WithReadOnly].
func ReadOnly(ctx context.Context) bool {
	v, _ := ctx.Value(readOnlyKey{}).(bool)
	return v
}

// ErrMaintenance is a module that is under maintenance.
var ErrMaintenance = errors.New("maintenance: the service is under maintenance")

// ErrUnknown is a flag that could not be read, so a write is refused.
var ErrUnknown = errors.New("maintenance: the flag could not be read, so writes are refused")

// Error is the refusal of a write.
type Error struct {
	// Flag is what was read; zero when Unknown.
	Flag Flag
	// Err is why the flag could not be read, when it could not.
	Err error
}

// Error implements error.
func (e *Error) Error() string {
	if e.Err != nil {
		return ErrUnknown.Error() + ": " + e.Err.Error()
	}
	return ErrMaintenance.Error()
}

// Is makes the refusal match its sentinel.
func (e *Error) Is(target error) bool {
	if e.Err != nil {
		return target == ErrUnknown
	}
	return target == ErrMaintenance
}

// Unwrap exposes the read error.
func (e *Error) Unwrap() error { return e.Err }

// Gate reads the flag of one module's table. The zero value is not usable;
// a nil *Gate is: it never refuses, which is a process with no shared state.
type Gate struct {
	src port.StateReader
	ttl time.Duration
	now func() time.Time

	mu   sync.Mutex
	flag Flag
	at   time.Time
	read bool
}

// Option adjusts a gate.
type Option func(*Gate)

// WithTTL sets how long a read is trusted. Zero or less is [DefaultTTL].
func WithTTL(d time.Duration) Option { return func(g *Gate) { g.ttl = d } }

// WithClock sets the clock, for a test.
func WithClock(now func() time.Time) Option { return func(g *Gate) { g.now = now } }

// New makes a gate over the module's own State (or a read-only view of it).
func New(src port.StateReader, opts ...Option) *Gate {
	g := &Gate{src: src, ttl: DefaultTTL, now: time.Now}
	for _, o := range opts {
		o(g)
	}
	if g.ttl <= 0 {
		g.ttl = DefaultTTL
	}
	return g
}

// Flag is the flag as read, from the cache when it is younger than the TTL. An
// absent record is the zero Flag. A read that fails is not cached, so the next
// call reads again.
func (g *Gate) Flag(ctx context.Context) (Flag, error) {
	if g == nil || g.src == nil {
		return Flag{}, nil
	}
	g.mu.Lock()
	if g.read && g.now().Sub(g.at) < g.ttl {
		f := g.flag
		g.mu.Unlock()
		return f, nil
	}
	g.mu.Unlock()
	f, err := g.fetch(ctx)
	if err != nil {
		return Flag{}, err
	}
	g.mu.Lock()
	g.flag, g.at, g.read = f, g.now(), true
	g.mu.Unlock()
	return f, nil
}

func (g *Gate) fetch(ctx context.Context) (Flag, error) {
	rec, err := g.src.Get(ctx, Key)
	if errors.Is(err, port.ErrNotFound) || errors.Is(err, port.ErrUnsupported) {
		// An adapter that cannot hold the record holds no flag either.
		return Flag{}, nil
	}
	if err != nil {
		return Flag{}, fmt.Errorf("read the maintenance flag: %w", err)
	}
	var f Flag
	if err := json.Unmarshal(rec.Value, &f); err != nil {
		// A record nobody can read is a flag nobody can clear by reading it:
		// treat it as set, since only the restore function writes it.
		return Flag{State: StateRestoring, Reason: "the maintenance record is unreadable"}, nil
	}
	return f, nil
}

// Writable is nil when the module may write, and an [*Error] when it is under
// maintenance or the flag cannot be read.
func (g *Gate) Writable(ctx context.Context) error {
	f, err := g.Flag(ctx)
	if err != nil {
		return &Error{Err: err}
	}
	if f.Active() {
		return &Error{Flag: f}
	}
	return nil
}

// Active is the flag if it is set. A read that fails is "not set": it is for
// what a module still serves in maintenance, and for showing the state.
func (g *Gate) Active(ctx context.Context) (Flag, bool) {
	f, err := g.Flag(ctx)
	if err != nil || !f.Active() {
		return Flag{}, false
	}
	return f, true
}

// Set is the gates of the modules one process serves, by module.
type Set map[port.Module]*Gate

// Of is the module's gate; nil (which never refuses) when the process has none.
func (s Set) Of(m port.Module) *Gate { return s[m] }

// Any is the first module under maintenance, in the layout's module order.
func (s Set) Any(ctx context.Context) (port.Module, Flag, bool) {
	for _, m := range port.Modules() {
		if f, ok := s[m].Active(ctx); ok {
			return m, f, true
		}
	}
	return "", Flag{}, false
}

// Write sets the flag in st. It is what the restore function calls, with the
// restore role's own credentials; no other role can write it.
func Write(ctx context.Context, st port.State, f Flag) error {
	if !f.Active() {
		return errors.New("maintenance: a flag needs a state; use Clear to lift it")
	}
	if f.Since.IsZero() {
		f.Since = time.Now().UTC()
	}
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = st.Put(ctx, Key, body, 0)
	return err
}

// Clear lifts the flag in st.
func Clear(ctx context.Context, st port.State) error { return st.Delete(ctx, Key) }

// Refuse writes the answer to a refused HTTP request: 503 with Retry-After, in
// JSON or, to a browser, a short page.
func Refuse(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Retry-After", strconv.Itoa(RetryAfter))
	w.Header().Set("Cache-Control", "no-store")
	msg := "sluis is under maintenance; try again shortly"
	if errors.Is(err, ErrUnknown) {
		msg = "sluis cannot tell whether it is under maintenance, so it refuses writes; try again shortly"
	}
	if acceptsHTML(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, "<!doctype html><title>Under maintenance</title><h1>Under maintenance</h1><p>%s.</p>\n", msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": Code, "error_description": msg})
}

func acceptsHTML(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept") {
		if strings.Contains(v, "text/html") {
			return true
		}
	}
	return false
}

// Middleware refuses, while the module is under maintenance, every request that
// serves does not name. A request serves names passes through with no read of
// the flag.
func (g *Gate) Middleware(serves func(*http.Request) bool, next http.Handler) http.Handler {
	if g == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serves(r) {
			next.ServeHTTP(w, r)
			return
		}
		if err := g.Writable(r.Context()); err != nil {
			Refuse(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MiddlewareFrozen is [Gate.Middleware] with a second list. While the flag is a
// migration freeze, a request that frozen names is served too, with its context
// marked [WithReadOnly] and a Retry-After on any 5xx it answers: it is a
// request that reads (the token exchange, a refresh that is not rotated). While
// the flag is any other state, frozen is not asked.
func (g *Gate) MiddlewareFrozen(serves, frozen func(*http.Request) bool, next http.Handler) http.Handler {
	if g == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serves(r) {
			next.ServeHTTP(w, r)
			return
		}
		f, err := g.Flag(r.Context())
		switch {
		case err != nil:
			Refuse(w, r, &Error{Err: err})
		case !f.Active():
			next.ServeHTTP(w, r)
		case f.Frozen() && frozen(r):
			next.ServeHTTP(&retryAfter{ResponseWriter: w}, r.WithContext(WithReadOnly(r.Context())))
		default:
			Refuse(w, r, &Error{Flag: f})
		}
	})
}

// retryAfter adds the Retry-After header to a server error, so that a client
// refused by a read-only path retries as it does when it is refused outright.
type retryAfter struct{ http.ResponseWriter }

func (w *retryAfter) WriteHeader(code int) {
	if code >= 500 {
		w.Header().Set("Retry-After", strconv.Itoa(RetryAfter))
		w.Header().Set("Cache-Control", "no-store")
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *retryAfter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Settle is how long after a flag is written every reader has seen it: a gate
// trusts what it read for [DefaultTTL], and this adds a margin.
const Settle = DefaultTTL + 5*time.Second

// Quiet reports how much longer a caller must wait before it can treat the
// writers of a module as stopped: zero once the flag has been set for [Settle].
func (f Flag) Quiet(now time.Time) time.Duration {
	if left := f.Since.Add(Settle).Sub(now); left > 0 {
		return left
	}
	return 0
}

// Read is the flag in st as it is now, with no cache. A read that fails is an
// error, since an operator asking must not be told "off" by a failure.
func Read(ctx context.Context, st port.StateReader) (Flag, error) {
	return New(st, WithTTL(time.Nanosecond)).fetch(ctx)
}
