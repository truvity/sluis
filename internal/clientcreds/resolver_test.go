package clientcreds

import (
	"context"
	"errors"
	"sync/atomic"
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

func TestResolveAFailedReadServesTheLastGoodRecordForAtMostFiveMinutes(t *testing.T) {
	t.Parallel()
	inner := memory.NewSecrets()
	putRecord(t, inner, "grafana", Record{Current: "good", Created: t0})
	store := &flaky{Secrets: inner}
	in := &inputs{values: map[string]string{secrets.ClientSecret("grafana"): "input"}}
	r, clk := newResolver(store, in)
	if got, _ := r.Resolve(ctx0, "grafana"); got.Current != "good" {
		t.Fatalf("got %+v", got)
	}

	store.mu.Lock()
	store.getErr = errors.New("openbao sealed")
	store.mu.Unlock()
	clk.t = t0.Add(2 * time.Minute)
	got, ok := r.Resolve(ctx0, "grafana")
	if !ok || got.Current != "good" {
		t.Errorf("stale-on-error within the window: %+v, %v", got, ok)
	}

	// The failure is remembered for a few seconds: a recovered store is not
	// asked again at once, and then it is.
	store.mu.Lock()
	store.getErr = nil
	store.mu.Unlock()
	putRecord(t, inner, "grafana", Record{Current: "newer", Created: t0})
	clk.t = t0.Add(2*time.Minute + retryAfterFailure - time.Second)
	if got, _ = r.Resolve(ctx0, "grafana"); got.Current != "good" {
		t.Errorf("inside the retry delay: %+v", got)
	}
	clk.t = t0.Add(2*time.Minute + retryAfterFailure + time.Second)
	if got, _ = r.Resolve(ctx0, "grafana"); got.Current != "newer" {
		t.Errorf("after recovery: %+v", got)
	}
}

func TestResolveAFailedReadIsNotServedPastFiveMinutesAndNeverFallsToTheInput(t *testing.T) {
	t.Parallel()
	inner := memory.NewSecrets()
	putRecord(t, inner, "grafana", Record{Current: "good", Created: t0})
	store := &flaky{Secrets: inner}
	in := &inputs{values: map[string]string{secrets.ClientSecret("grafana"): "input"}}
	r, clk := newResolver(store, in)
	r.Resolve(ctx0, "grafana")
	store.mu.Lock()
	store.getErr = errors.New("down")
	store.mu.Unlock()

	clk.t = t0.Add(StaleFor)
	if got, ok := r.Resolve(ctx0, "grafana"); !ok || got.Current != "good" {
		t.Errorf("at five minutes: %+v, %v", got, ok)
	}
	// (A stale answer is itself cached for the retry delay, so the cap holds
	// to within that delay.)
	clk.t = t0.Add(StaleFor + retryAfterFailure + time.Second)
	if got, ok := r.Resolve(ctx0, "grafana"); ok {
		t.Errorf("served %+v more than five minutes after the last good read", got)
	}
	// Nor is the input a way around it.
	if len(in.reads) != 0 {
		t.Errorf("the input was read for a generated client whose record could not be: %v", in.reads)
	}
}

func TestResolveAFailedReadWithNothingCachedFailsClosed(t *testing.T) {
	t.Parallel()
	store := &flaky{Secrets: memory.NewSecrets(), getErr: errors.New("down")}
	in := &inputs{values: map[string]string{secrets.ClientSecret("grafana"): "input"}}
	r, _ := newResolver(store, in)
	if got, ok := r.Resolve(ctx0, "grafana"); ok {
		t.Errorf("authenticated against %+v with the store down", got)
	}
	if len(in.reads) != 0 {
		t.Errorf("the input was read: %v", in.reads)
	}
	r2, _ := newResolver(store, nil)
	if _, ok := r2.Resolve(ctx0, "grafana"); ok {
		t.Error("resolved with no record, no input and a failing store")
	}
}

func TestResolveACorruptRecordFailsClosedWhateverTheInputSays(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	if _, err := store.Put(ctx0, Path("grafana"), []byte(`{"v":9,"current":"leaky-value"}`)); err != nil {
		t.Fatal(err)
	}
	in := &inputs{values: map[string]string{secrets.ClientSecret("grafana"): "input"}}
	r, _ := newResolver(store, in)
	if got, ok := r.Resolve(ctx0, "grafana"); ok {
		t.Errorf("a corrupt record authenticated against %+v", got)
	}
	if len(in.reads) != 0 {
		t.Errorf("the input was read: %v", in.reads)
	}
}

func TestResolveAnUnreadableRecordServesTheLastGoodOneForFiveMinutes(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	putRecord(t, store, "grafana", Record{Current: "good", Created: t0})
	r, clk := newResolver(store, nil)
	r.Resolve(ctx0, "grafana")
	if _, err := store.Put(ctx0, Path("grafana"), []byte(`{"v":9,"current":"x"}`)); err != nil {
		t.Fatal(err)
	}
	clk.t = t0.Add(time.Minute)
	if got, ok := r.Resolve(ctx0, "grafana"); !ok || got.Current != "good" {
		t.Errorf("within five minutes: %+v, %v", got, ok)
	}
	clk.t = t0.Add(StaleFor + time.Second)
	if got, ok := r.Resolve(ctx0, "grafana"); ok {
		t.Errorf("past five minutes: %+v", got)
	}
}

// A failed read is retried after a few seconds, not by every request.
func TestResolveAFailedReadIsRetriedAfterFiveSecondsNotEveryRequest(t *testing.T) {
	t.Parallel()
	inner := memory.NewSecrets()
	putRecord(t, inner, "grafana", Record{Current: "good", Created: t0})
	store := &countingGets{Secrets: inner}
	r, clk := newResolver(store, nil)
	r.Resolve(ctx0, "grafana")
	store.fail.Store(true)
	before := store.gets.Load()
	clk.t = t0.Add(time.Minute) // past the 30s cache: the next request reads
	for range 20 {
		r.Resolve(ctx0, "grafana")
	}
	if n := store.gets.Load() - before; n != 1 {
		t.Errorf("%d reads for 20 requests inside the retry delay, want 1", n)
	}
	clk.t = clk.t.Add(retryAfterFailure)
	r.Resolve(ctx0, "grafana")
	if n := store.gets.Load() - before; n != 2 {
		t.Errorf("%d reads after the delay, want 2", n)
	}
	if retryAfterFailure != 5*time.Second || StaleFor != 5*time.Minute {
		t.Errorf("retry %v, stale %v", retryAfterFailure, StaleFor)
	}
}

type countingGets struct {
	port.Secrets
	gets atomic.Int32
	fail atomic.Bool
}

func (c *countingGets) Get(ctx context.Context, path string) (port.Secret, error) {
	c.gets.Add(1)
	if c.fail.Load() {
		return port.Secret{}, errors.New("down")
	}
	return c.Secrets.Get(ctx, path)
}

// Only a generated client consults a record.
func TestResolveOnlyAGeneratedClientConsultsTheRecord(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	putRecord(t, store, "back-on-a-name", Record{Current: "stale-record", Created: t0})
	putRecord(t, store, "generated", Record{Current: "from-record", Created: t0})
	in := &inputs{values: map[string]string{
		secrets.ClientSecret("back-on-a-name"): "named-input",
		secrets.ClientSecret("generated"):      "input",
	}}
	r, _ := newResolver(store, in)
	generated := map[string]bool{"generated": true}
	r.UseGenerated(func(id string) bool { return generated[id] })

	if got, ok := r.Resolve(ctx0, "back-on-a-name"); !ok || got.Current != "named-input" || got.Previous != "" {
		t.Errorf("a named client: %+v, %v", got, ok)
	}
	if got, ok := r.Resolve(ctx0, "generated"); !ok || got.Current != "from-record" {
		t.Errorf("a generated client: %+v, %v", got, ok)
	}
	// The policy in force is asked each time: a client that leaves
	// `generate: true` is served by its input at once, cache or not.
	delete(generated, "generated")
	if got, ok := r.Resolve(ctx0, "generated"); !ok || got.Current != "input" {
		t.Errorf("after leaving generate: %+v, %v", got, ok)
	}
	generated["generated"] = true
	if got, ok := r.Resolve(ctx0, "generated"); !ok || got.Current != "from-record" {
		t.Errorf("generated again: %+v, %v", got, ok)
	}
	// Not a generated client and no input: nobody, whatever a record says.
	if got, ok := r.Resolve(ctx0, "unlisted"); ok {
		t.Errorf("an unlisted client: %+v", got)
	}
}

// A resolver told nothing about the policy treats every client as generated;
// the issuer's wiring must always tell it (see issuerapp).
func TestResolveWithoutAPolicyTreatsEveryClientAsGenerated(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	putRecord(t, store, "any", Record{Current: "from-record", Created: t0})
	r, _ := newResolver(store, nil)
	if got, ok := r.Resolve(ctx0, "any"); !ok || got.Current != "from-record" {
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
