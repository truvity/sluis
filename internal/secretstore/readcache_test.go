package secretstore_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/memory"
)

// counting counts the reads and writes that reach the backend.
type counting struct {
	state.Store
	gets, puts *atomic.Int64
}

func newCounting(s state.Store) counting {
	return counting{Store: s, gets: new(atomic.Int64), puts: new(atomic.Int64)}
}

func (c counting) Get(ctx context.Context, k string) (state.Item, error) {
	c.gets.Add(1)
	return c.Store.Get(ctx, k)
}

func (c counting) Put(ctx context.Context, k string, v []byte, r state.Rev) (state.Rev, error) {
	c.puts.Add(1)
	return c.Store.Put(ctx, k, v, r)
}

func (c counting) Child(p string, o ...state.Option) state.Store {
	return counting{Store: c.Store.Child(p, o...), gets: c.gets, puts: c.puts}
}

type clock struct{ t atomic.Int64 }

func (c *clock) now() time.Time          { return time.Unix(0, c.t.Load()) }
func (c *clock) advance(d time.Duration) { c.t.Add(int64(d)) }

func setup(t *testing.T) (state.Store, counting, *clock) {
	t.Helper()
	clk := &clock{}
	clk.t.Store(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).UnixNano())
	back := newCounting(memory.New())
	cached := secretstore.NewReadCache(5*time.Minute).WithClock(clk.now).Wrap(back, "/sluis/x")
	return cached, back, clk
}

func TestReadCacheWarmReadsDoNotReachTheBackend(t *testing.T) {
	ctx := context.Background()
	s, back, _ := setup(t)
	child := s.Child("internal").Child("oidc")
	if _, err := child.Put(ctx, "k", []byte(`{"a":"1"}`), ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		it, err := child.Get(ctx, "k")
		if err != nil || string(it.Value) != `{"a":"1"}` {
			t.Fatalf("get %d: %q %v", i, it.Value, err)
		}
		it.Value[0] = 'X' // a caller's edit must not reach the cache
	}
	if got := back.gets.Load(); got != 1 {
		t.Fatalf("50 reads made %d backend reads, want 1", got)
	}
}

func TestReadCacheTTLExpires(t *testing.T) {
	ctx := context.Background()
	s, back, clk := setup(t)
	_, _ = s.Put(ctx, "k", []byte(`{"a":"1"}`), "")
	_, _ = s.Get(ctx, "k")
	clk.advance(4 * time.Minute)
	_, _ = s.Get(ctx, "k")
	if back.gets.Load() != 1 {
		t.Fatalf("inside the TTL: %d reads", back.gets.Load())
	}
	clk.advance(2 * time.Minute)
	_, _ = s.Get(ctx, "k")
	if back.gets.Load() != 2 {
		t.Fatalf("after the TTL: %d reads", back.gets.Load())
	}
}

func TestReadCachePutDeleteAndFreshInvalidate(t *testing.T) {
	ctx := context.Background()
	s, back, _ := setup(t)
	rev, _ := s.Put(ctx, "k", []byte(`{"a":"1"}`), "")
	_, _ = s.Get(ctx, "k")
	// A write through the process is read back at once.
	rev2, err := s.Put(ctx, "k", []byte(`{"a":"2"}`), rev)
	if err != nil {
		t.Fatal(err)
	}
	if it, _ := s.Get(ctx, "k"); string(it.Value) != `{"a":"2"}` || it.Rev != rev2 {
		t.Fatalf("after put: %q %s", it.Value, it.Rev)
	}
	// A write by another instance is not seen until the TTL, unless forced.
	_, _ = back.Store.Put(ctx, "k", []byte(`{"a":"3"}`), rev2)
	if it, _ := s.Get(ctx, "k"); string(it.Value) != `{"a":"2"}` {
		t.Fatalf("expected the cached copy, got %q", it.Value)
	}
	if it, _ := s.Get(state.WithFreshRead(ctx), "k"); string(it.Value) != `{"a":"3"}` {
		t.Fatalf("fresh read: %q", it.Value)
	}
	it3, _ := s.Get(ctx, "k")
	if string(it3.Value) != `{"a":"3"}` {
		t.Fatalf("a fresh read replaces the copy, got %q", it3.Value)
	}
	rev4, err := back.Store.Put(ctx, "k", []byte(`{"a":"4"}`), it3.Rev)
	if err != nil {
		t.Fatal(err)
	}
	state.Invalidate(s, "k")
	if it, _ := s.Get(ctx, "k"); string(it.Value) != `{"a":"4"}` {
		t.Fatalf("after invalidate: %q", it.Value)
	}
	// A lost conditional write drops the copy: the retry reads what won.
	_, _ = s.Get(ctx, "k")
	if _, err := back.Store.Put(ctx, "k", []byte(`{"a":"5"}`), rev4); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "k", []byte(`{"a":"6"}`), rev4); err == nil {
		t.Fatal("stale conditional put succeeded")
	}
	if it, _ := s.Get(ctx, "k"); string(it.Value) != `{"a":"5"}` {
		t.Fatalf("after a conflict: %q", it.Value)
	}
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "k"); err != state.ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
}

func TestReadCacheNeverOutlivesTheDocumentsExpiry(t *testing.T) {
	ctx := context.Background()
	s, back, clk := setup(t)
	exp := clk.now().Add(90 * time.Second).UTC().Format(time.RFC3339)
	_, _ = s.Put(ctx, "tok", []byte(`{"token":"t","expires_on":"`+exp+`"}`), "")
	_, _ = s.Get(ctx, "tok")
	clk.advance(60 * time.Second)
	_, _ = s.Get(ctx, "tok")
	if back.gets.Load() != 1 {
		t.Fatalf("before the expiry: %d reads", back.gets.Load())
	}
	clk.advance(40 * time.Second)
	_, _ = s.Get(ctx, "tok")
	if back.gets.Load() != 2 {
		t.Fatalf("after the expiry: %d reads", back.gets.Load())
	}
	// An already expired document is never kept.
	old := clk.now().Add(-time.Hour).UTC().Format(time.RFC3339)
	_, _ = s.Put(ctx, "old", []byte(`{"expires_on":"`+old+`"}`), "")
	_, _ = s.Get(ctx, "old")
	_, _ = s.Get(ctx, "old")
	if back.gets.Load() != 4 {
		t.Fatalf("an expired document was cached: %d reads", back.gets.Load())
	}
}

func TestReadCacheOffAndConcurrent(t *testing.T) {
	ctx := context.Background()
	back := newCounting(memory.New())
	if got := secretstore.NewReadCache(0).Wrap(back, "/r"); got != state.Store(back) {
		t.Fatal("a zero TTL must leave the store alone")
	}
	s, back2, _ := setup(t)
	_, _ = s.Put(ctx, "k", []byte(`{"n":0}`), "")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if _, err := s.Get(ctx, "k"); err != nil {
					t.Error(err)
					return
				}
				if g == 0 && i%20 == 0 {
					_, _ = s.Put(ctx, "k", []byte(`{"n":1}`), "")
				}
			}
		}()
	}
	wg.Wait()
	if back2.gets.Load() > 100 {
		t.Fatalf("1600 reads made %d backend reads", back2.gets.Load())
	}
}

func TestReadCacheCountsHitsAndMisses(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })
	// The counter is created at package init under the global provider, which
	// is a delegating one, so it follows the provider set above.
	ctx := context.Background()
	s, _, _ := setup(t)
	_, _ = s.Put(ctx, "k", []byte(`{"a":"1"}`), "")
	for i := 0; i < 10; i++ {
		_, _ = s.Get(ctx, "k")
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "sluis.secret.reads" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				v, _ := dp.Attributes.Value("result")
				got[v.AsString()] += dp.Value
			}
		}
	}
	if got["miss"] != 1 || got["hit"] != 9 {
		t.Fatalf("reads by result: %v, want 1 miss and 9 hits", got)
	}
}
