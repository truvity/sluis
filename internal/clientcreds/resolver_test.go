package clientcreds

import (
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/secrets"
)

// clock is a settable time for a Resolver.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func putRecord(t *testing.T, s port.Secrets, id string, rec Record) {
	t.Helper()
	body, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put(ctx0, Path(id), body); err != nil {
		t.Fatal(err)
	}
}

func newResolver(store port.Secrets, in Input) (*Resolver, *clock) {
	r := NewResolver(store, in, quiet())
	c := &clock{t: t0}
	r.now = c.now
	return r, c
}

func TestResolveTheRecordWinsOverTheInput(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	valid := t0.Add(time.Hour)
	putRecord(t, store, "grafana", Record{Current: "from-record", Previous: "old", PreviousValidUntil: valid, Created: t0})
	in := &inputs{values: map[string]string{secrets.ClientSecret("grafana"): "from-input"}}
	r, _ := newResolver(store, in)
	got, ok := r.Resolve(ctx0, "grafana")
	if !ok || got.Current != "from-record" || got.Previous != "old" || !got.PreviousValidUntil.Equal(valid) {
		t.Errorf("got %+v, %v", got, ok)
	}
}

func TestResolveTheInputServesWhenThereIsNoRecord(t *testing.T) {
	t.Parallel()
	in := &inputs{values: map[string]string{
		secrets.ClientSecret("generated"): "gen-input",
		secrets.ClientSecret("plain"):     "plain-input",
	}}
	r, _ := newResolver(memory.NewSecrets(), in)
	// The resolver does not know which clients generate: any id reads the input.
	for id, want := range map[string]string{"generated": "gen-input", "plain": "plain-input"} {
		got, ok := r.Resolve(ctx0, id)
		if !ok || got.Current != want || got.Previous != "" {
			t.Errorf("%s: %+v, %v", id, got, ok)
		}
	}
	if _, ok := r.Resolve(ctx0, "nobody"); ok {
		t.Error("an id with neither a record nor an input resolved")
	}
}

func TestResolveWithNoStoreAndNoInput(t *testing.T) {
	t.Parallel()
	r := NewResolver(nil, nil, nil)
	if _, ok := r.Resolve(ctx0, "grafana"); ok {
		t.Error("resolved with nothing behind it")
	}
	r = NewResolver(nil, &inputs{values: map[string]string{secrets.ClientSecret("grafana"): "s"}}, nil)
	if got, ok := r.Resolve(ctx0, "grafana"); !ok || got.Current != "s" {
		t.Errorf("input only: %+v, %v", got, ok)
	}
}

func TestResolveRefusesIdsThatAreNotOneSegment(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	in := &inputs{values: map[string]string{
		secrets.ClientSecret("a/b"):  "x",
		secrets.ClientSecret(`a\b`):  "x",
		secrets.ClientSecret("../x"): "x",
	}}
	putRecord(t, store, "a/b", Record{Current: "rec", Created: t0})
	r, _ := newResolver(store, in)
	for _, id := range []string{"a/b", `a\b`, "../x", "/", ""} {
		if got, ok := r.Resolve(ctx0, id); ok {
			t.Errorf("%q resolved to %+v", id, got)
		}
	}
	if len(in.reads) != 0 {
		t.Errorf("the input was read for a refused id: %v", in.reads)
	}
}

func TestResolveCachesTheRecordFor30SecondsAndForgetDropsIt(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	putRecord(t, store, "grafana", Record{Current: "one", Created: t0})
	r, clk := newResolver(store, nil)

	if got, _ := r.Resolve(ctx0, "grafana"); got.Current != "one" {
		t.Fatalf("first = %+v", got)
	}
	putRecord(t, store, "grafana", Record{Current: "two", Created: t0})

	clk.t = t0.Add(CacheTTL - time.Nanosecond)
	if got, _ := r.Resolve(ctx0, "grafana"); got.Current != "one" {
		t.Errorf("inside the TTL the record was read again: %+v", got)
	}
	clk.t = t0.Add(CacheTTL)
	if got, _ := r.Resolve(ctx0, "grafana"); got.Current != "two" {
		t.Errorf("at the TTL the record was not read again: %+v", got)
	}

	putRecord(t, store, "grafana", Record{Current: "three", Created: t0})
	if got, _ := r.Resolve(ctx0, "grafana"); got.Current != "two" {
		t.Errorf("a fresh cache was bypassed: %+v", got)
	}
	r.Forget("grafana")
	if got, _ := r.Resolve(ctx0, "grafana"); got.Current != "three" {
		t.Errorf("after Forget the record was not read again: %+v", got)
	}
	r.Forget("never-seen") // no-op, no panic
}

func TestResolveTheTTLIs30Seconds(t *testing.T) {
	t.Parallel()
	if CacheTTL != 30*time.Second {
		t.Errorf("CacheTTL = %v", CacheTTL)
	}
}

func TestResolveCachesTheAbsenceOfARecordAndForgetFindsTheNewOne(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	in := &inputs{values: map[string]string{secrets.ClientSecret("grafana"): "input"}}
	r, _ := newResolver(store, in)
	if got, _ := r.Resolve(ctx0, "grafana"); got.Current != "input" {
		t.Fatalf("got %+v", got)
	}
	// A reconcile writes the record; a cached absence hides it until Forget.
	putRecord(t, store, "grafana", Record{Current: "record", Created: t0})
	if got, _ := r.Resolve(ctx0, "grafana"); got.Current != "input" {
		t.Errorf("a cached absence was not honoured: %+v", got)
	}
	r.Forget("grafana")
	if got, _ := r.Resolve(ctx0, "grafana"); got.Current != "record" {
		t.Errorf("after Forget: %+v", got)
	}
}

func TestResolveAFailedReadServesTheLastGoodRecord(t *testing.T) {
	t.Parallel()
	inner := memory.NewSecrets()
	putRecord(t, inner, "grafana", Record{Current: "good", Created: t0})
	store := &flaky{Secrets: inner}
	r, clk := newResolver(store, nil)
	if got, _ := r.Resolve(ctx0, "grafana"); got.Current != "good" {
		t.Fatalf("got %+v", got)
	}

	store.mu.Lock()
	store.getErr = errors.New("openbao sealed")
	store.mu.Unlock()
	clk.t = t0.Add(10 * CacheTTL)
	got, ok := r.Resolve(ctx0, "grafana")
	if !ok || got.Current != "good" {
		t.Errorf("stale-on-error: %+v, %v", got, ok)
	}

	// And when the store is back, the record is read again.
	store.mu.Lock()
	store.getErr = nil
	store.mu.Unlock()
	putRecord(t, inner, "grafana", Record{Current: "newer", Created: t0})
	if got, _ = r.Resolve(ctx0, "grafana"); got.Current != "newer" {
		t.Errorf("after recovery: %+v", got)
	}
}

func TestResolveAFailedReadWithNothingCachedFallsToTheInput(t *testing.T) {
	t.Parallel()
	store := &flaky{Secrets: memory.NewSecrets(), getErr: errors.New("down")}
	in := &inputs{values: map[string]string{secrets.ClientSecret("grafana"): "input"}}
	r, _ := newResolver(store, in)
	if got, ok := r.Resolve(ctx0, "grafana"); !ok || got.Current != "input" {
		t.Errorf("got %+v, %v", got, ok)
	}
	r2, _ := newResolver(store, nil)
	if _, ok := r2.Resolve(ctx0, "grafana"); ok {
		t.Error("resolved with no record, no input and a failing store")
	}
}

func TestResolveAnUnreadableRecordServesTheLastGoodOne(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	putRecord(t, store, "grafana", Record{Current: "good", Created: t0})
	r, clk := newResolver(store, nil)
	r.Resolve(ctx0, "grafana")
	if _, err := store.Put(ctx0, Path("grafana"), []byte(`{"v":9,"current":"x"}`)); err != nil {
		t.Fatal(err)
	}
	clk.t = t0.Add(time.Hour)
	if got, ok := r.Resolve(ctx0, "grafana"); !ok || got.Current != "good" {
		t.Errorf("got %+v, %v", got, ok)
	}
}

func TestResolveAnInputReadErrorMeansNoSecret(t *testing.T) {
	t.Parallel()
	name := secrets.ClientSecret("grafana")
	in := &inputs{errs: map[string]error{name: errors.New("ssm throttled")}}
	r, _ := newResolver(memory.NewSecrets(), in)
	if got, ok := r.Resolve(ctx0, "grafana"); ok {
		t.Errorf("resolved to %+v on a read error", got)
	}
}

func TestResolveAnIdThatIsNotASecretNameDoesNotReadTheInput(t *testing.T) {
	t.Parallel()
	in := &inputs{}
	r, _ := newResolver(memory.NewSecrets(), in)
	if _, ok := r.Resolve(ctx0, "has space"); ok {
		t.Error("resolved")
	}
	if len(in.reads) != 0 {
		t.Errorf("the input was read: %v", in.reads)
	}
}
