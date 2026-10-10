package maintenance_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
)

// counting counts the reads the gate makes of the flag.
type counting struct {
	port.StateReader
	gets atomic.Int32
	err  error
}

func (c *counting) Get(ctx context.Context, key string) (port.Record, error) {
	c.gets.Add(1)
	if c.err != nil {
		return port.Record{}, c.err
	}
	return c.StateReader.Get(ctx, key)
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestGateServesUntilTheFlagIsSetAndAgainWhenItIsCleared(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := memory.New()
	clk := &clock{t: time.Unix(1_000, 0)}
	g := maintenance.New(st, maintenance.WithClock(clk.now), maintenance.WithTTL(time.Second))

	if err := g.Writable(ctx); err != nil {
		t.Fatalf("no flag: %v", err)
	}
	if err := maintenance.Write(ctx, st, maintenance.Flag{State: maintenance.StateRestoring, By: "restore", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * time.Second)
	err := g.Writable(ctx)
	if !errors.Is(err, maintenance.ErrMaintenance) {
		t.Fatalf("flag set: %v, want ErrMaintenance", err)
	}
	var me *maintenance.Error
	if !errors.As(err, &me) || me.Flag.By != "restore" || me.Flag.Since.IsZero() {
		t.Fatalf("the refusal does not carry the flag: %+v", me)
	}
	if err := maintenance.Clear(ctx, st); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * time.Second)
	if err := g.Writable(ctx); err != nil {
		t.Fatalf("flag cleared: %v", err)
	}
}

func TestTheCacheHoldsForTheTTLAndNoLonger(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := memory.New()
	src := &counting{StateReader: st}
	clk := &clock{t: time.Unix(1_000, 0)}
	g := maintenance.New(src, maintenance.WithClock(clk.now)) // the default TTL

	for range 5 {
		if err := g.Writable(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := src.gets.Load(); n != 1 {
		t.Fatalf("5 calls inside the TTL read the flag %d times, want 1", n)
	}
	// Set behind the gate's back: it is not seen until the TTL has passed.
	if err := maintenance.Write(ctx, st, maintenance.Flag{State: maintenance.StateRestoring}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(maintenance.DefaultTTL - time.Nanosecond)
	if err := g.Writable(ctx); err != nil {
		t.Fatalf("just inside the TTL the cached answer stands: %v", err)
	}
	clk.t = clk.t.Add(time.Nanosecond)
	if err := g.Writable(ctx); !errors.Is(err, maintenance.ErrMaintenance) {
		t.Fatalf("at the TTL the flag is read again: %v", err)
	}
	if n := src.gets.Load(); n != 2 {
		t.Fatalf("reads = %d, want 2", n)
	}
}

func TestAnUnreadableFlagRefusesWritesAndKeepsReads(t *testing.T) {
	t.Parallel()
	src := &counting{StateReader: memory.New(), err: port.ErrUnavailable}
	g := maintenance.New(src)

	if err := g.Writable(t.Context()); !errors.Is(err, maintenance.ErrUnknown) {
		t.Fatalf("a flag that cannot be read: %v, want ErrUnknown (fail closed for a write)", err)
	}
	if _, on := g.Active(t.Context()); on {
		t.Fatal("a flag that cannot be read must not stop reads")
	}
	// A failure is not cached: the next call reads again.
	src.err = nil
	if err := g.Writable(t.Context()); err != nil {
		t.Fatalf("the store is back: %v", err)
	}
}

func TestANilGateAndAnAdapterThatHoldsNoFlagNeverRefuse(t *testing.T) {
	t.Parallel()
	var g *maintenance.Gate
	if err := g.Writable(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.New(&counting{StateReader: memory.New(), err: port.ErrUnsupported}).Writable(t.Context()); err != nil {
		t.Fatalf("an adapter with no such record: %v", err)
	}
}

func TestMiddlewareRefusesWhatIsNotServedWith503AndRetryAfter(t *testing.T) {
	t.Parallel()
	st := memory.New()
	g := maintenance.New(st, maintenance.WithTTL(time.Nanosecond))
	served := func(r *http.Request) bool { return r.URL.Path == "/keys" }
	h := g.Middleware(served, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	do := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		return w
	}

	if w := do("/token"); w.Code != http.StatusNoContent {
		t.Fatalf("no flag: %d", w.Code)
	}
	if err := maintenance.Write(t.Context(), st, maintenance.Flag{State: maintenance.StateRestoring}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	w := do("/token")
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("under maintenance: %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	if w := do("/keys"); w.Code != http.StatusNoContent {
		t.Fatalf("a served path under maintenance: %d", w.Code)
	}
}

func TestSetFindsTheModuleUnderMaintenance(t *testing.T) {
	t.Parallel()
	gh, slack := memory.New(), memory.New()
	set := maintenance.Set{port.ModuleGitHub: maintenance.New(gh, maintenance.WithTTL(time.Nanosecond)), port.ModuleSlack: maintenance.New(slack, maintenance.WithTTL(time.Nanosecond))}
	if _, _, on := set.Any(t.Context()); on {
		t.Fatal("nothing is set")
	}
	if err := maintenance.Write(t.Context(), slack, maintenance.Flag{State: maintenance.StateRestoring}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	m, _, on := set.Any(t.Context())
	if !on || m != port.ModuleSlack {
		t.Fatalf("Any = %q, %v", m, on)
	}
}
