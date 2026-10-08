package clientcreds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/rails"
)

// fakeLock is a Locker a test can hold or break.
type fakeLock struct {
	held  bool
	err   error
	calls atomic.Int32
	// targets are the lease targets asked for.
	mu      sync.Mutex
	targets []string
}

func (l *fakeLock) Do(ctx context.Context, kind, target string, fn func(context.Context)) (bool, error) {
	l.calls.Add(1)
	l.mu.Lock()
	l.targets = append(l.targets, kind+":"+target)
	l.mu.Unlock()
	if l.err != nil {
		return false, l.err
	}
	if l.held {
		return false, nil
	}
	fn(ctx)
	return true, nil
}

type managed struct {
	*Manager
	store    port.Secrets
	resolver *Resolver
	clock    *clock
}

func newManaged(t *testing.T, store port.Secrets, lock Locker, generated ...string) *managed {
	t.Helper()
	m := &managed{store: store, clock: &clock{t: t0}}
	m.resolver = NewResolver(store, nil, quiet())
	m.resolver.now = m.clock.now
	m.Manager = &Manager{
		Store: store, Lock: lock, Resolver: m.resolver,
		Generated: func(id string) bool {
			for _, g := range generated {
				if g == id {
					return true
				}
			}
			return false
		},
		Now: m.clock.now,
		Log: quiet(),
	}
	return m
}

func seed(t *testing.T, store port.Secrets, id string) Record {
	t.Helper()
	if res := Reconcile(ctx0, []string{id}, store, nil, t0, quiet(), Hooks{}); res.Outcomes[id] != OutcomeCreated {
		t.Fatalf("seed %s: %+v", id, res)
	}
	return stored(t, store, id)
}

func TestRotateWithNoOverlapIsAHardCut(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	before := seed(t, store, "grafana")
	m := newManaged(t, store, nil, "grafana")
	m.clock.t = t0.Add(time.Hour)

	res, err := m.Rotate(ctx0, "grafana", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Overlap != 0 || !res.PreviousValidUntil.IsZero() || res.DiscardedPrevious || !res.Rotated.Equal(m.clock.t) {
		t.Errorf("rotation = %+v", res)
	}
	after := stored(t, store, "grafana")
	if after.Current == before.Current || len(after.Current) != 43 {
		t.Errorf("current = %q (was %q)", after.Current, before.Current)
	}
	if after.Previous != "" || !after.PreviousValidUntil.IsZero() {
		t.Errorf("a hard cut kept a previous secret: %+v", after)
	}
	if !after.Created.Equal(before.Created) || !after.Rotated.Equal(m.clock.t) {
		t.Errorf("times = created %v rotated %v", after.Created, after.Rotated)
	}
}

func TestRotateWithAnOverlapKeepsThePreviousUntilItEnds(t *testing.T) {
	t.Parallel()
	for name, overlap := range map[string]time.Duration{"a second": time.Second, "24h": DefaultOverlap, "the maximum": MaxOverlap} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := memory.NewSecrets()
			before := seed(t, store, "grafana")
			m := newManaged(t, store, nil, "grafana")
			res, err := m.Rotate(ctx0, "grafana", overlap)
			if err != nil {
				t.Fatal(err)
			}
			want := t0.Add(overlap)
			if !res.PreviousValidUntil.Equal(want) || res.Overlap != overlap {
				t.Errorf("rotation = %+v, want valid until %v", res, want)
			}
			after := stored(t, store, "grafana")
			if after.Previous != before.Current || !after.PreviousValidUntil.Equal(want) || after.Current == before.Current {
				t.Errorf("record = %+v", after)
			}
		})
	}
}

func TestRotateRefusesAnOverlapOutsideZeroToSevenDays(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	before := seed(t, store, "grafana")
	m := newManaged(t, store, nil, "grafana")
	for name, overlap := range map[string]time.Duration{
		"over a week by a second": MaxOverlap + time.Second, "a month": 30 * 24 * time.Hour, "negative": -time.Second, "very negative": -time.Hour,
	} {
		if _, err := m.Rotate(ctx0, "grafana", overlap); !errors.Is(err, ErrOverlap) {
			t.Errorf("%s: %v, want ErrOverlap", name, err)
		}
	}
	if stored(t, store, "grafana") != before {
		t.Error("a refused rotation changed the record")
	}
}

func TestRotateDuringAnOpenOverlapDiscardsTheOlderSecret(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	first := seed(t, store, "grafana")
	m := newManaged(t, store, nil, "grafana")

	r1, err := m.Rotate(ctx0, "grafana", DefaultOverlap)
	if err != nil || r1.DiscardedPrevious {
		t.Fatalf("first rotation = %+v, %v", r1, err)
	}
	second := stored(t, store, "grafana")

	m.clock.t = t0.Add(time.Hour)
	r2, err := m.Rotate(ctx0, "grafana", time.Hour)
	if err != nil || !r2.DiscardedPrevious {
		t.Fatalf("second rotation = %+v, %v; want the open overlap reported discarded", r2, err)
	}
	third := stored(t, store, "grafana")
	if third.Previous != second.Current {
		t.Error("the previous secret is not the one just replaced")
	}
	if third.Previous == first.Current || third.Current == first.Current {
		t.Error("the discarded secret is still in the record")
	}

	// Once an overlap has run out, rotating again discards nothing.
	m.clock.t = t0.Add(10 * time.Hour)
	r3, err := m.Rotate(ctx0, "grafana", 0)
	if err != nil || r3.DiscardedPrevious {
		t.Errorf("after the overlap ended: %+v, %v", r3, err)
	}
}

func TestRotateRefusals(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	seed(t, store, "named-in-policy")
	m := newManaged(t, store, nil, "grafana", "norecord")

	if _, err := m.Rotate(ctx0, "named-in-policy", 0); !errors.Is(err, ErrNotGenerated) {
		t.Errorf("a client that is not generated: %v", err)
	}
	if _, err := m.Rotate(ctx0, "undeclared", 0); !errors.Is(err, ErrNotGenerated) {
		t.Errorf("an undeclared client: %v", err)
	}
	if _, err := m.Rotate(ctx0, "norecord", 0); !errors.Is(err, ErrNoRecord) {
		t.Errorf("a generated client with no record: %v", err)
	}
	if _, err := (&Manager{Store: store}).Rotate(ctx0, "grafana", 0); !errors.Is(err, ErrNotGenerated) {
		t.Errorf("a manager with no policy: %v", err)
	}
	none := newManaged(t, nil, nil, "grafana")
	none.Store = nil
	if _, err := none.Rotate(ctx0, "grafana", 0); !errors.Is(err, ErrNoRecord) {
		t.Errorf("no store: %v", err)
	}
}

func TestRotateRefusesACorruptRecordWithoutItsValue(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	if _, err := store.Put(ctx0, Path("grafana"), []byte(`{"v":7,"current":"leaky-value"}`)); err != nil {
		t.Fatal(err)
	}
	m := newManaged(t, store, nil, "grafana")
	_, err := m.Rotate(ctx0, "grafana", 0)
	if err == nil || strings.Contains(err.Error(), "leaky-value") {
		t.Errorf("err = %v", err)
	}
}

func TestRotateIsBusyWhenTheLeaseIsHeldOrTheRecordMovedUnderIt(t *testing.T) {
	t.Parallel()
	t.Run("the lease is held", func(t *testing.T) {
		t.Parallel()
		store := memory.NewSecrets()
		before := seed(t, store, "grafana")
		lock := &fakeLock{held: true}
		m := newManaged(t, store, lock, "grafana")
		if _, err := m.Rotate(ctx0, "grafana", 0); !errors.Is(err, ErrBusy) {
			t.Fatalf("err = %v, want ErrBusy", err)
		}
		if stored(t, store, "grafana") != before {
			t.Error("a busy rotation wrote or announced")
		}
		if len(lock.targets) != 1 || lock.targets[0] != "client-secret:grafana" {
			t.Errorf("lease asked for = %v", lock.targets)
		}
	})
	t.Run("the lease cannot be taken", func(t *testing.T) {
		t.Parallel()
		store := memory.NewSecrets()
		seed(t, store, "grafana")
		m := newManaged(t, store, &fakeLock{err: errors.New("state down")}, "grafana")
		_, err := m.Rotate(ctx0, "grafana", 0)
		if err == nil || errors.Is(err, ErrBusy) {
			t.Errorf("err = %v, want a failure that is not ErrBusy", err)
		}
	})
	for name, conflict := range map[string]error{"a conflict": port.ErrConflict, "the record gone": port.ErrNotFound} {
		t.Run("the write finds "+name, func(t *testing.T) {
			t.Parallel()
			inner := memory.NewSecrets()
			before := seed(t, inner, "grafana")
			store := &flaky{Secrets: inner, onPut: func(port.Secrets, string, []byte) (string, error) { return "", conflict }}
			m := newManaged(t, store, nil, "grafana")
			if _, err := m.Rotate(ctx0, "grafana", 0); !errors.Is(err, ErrBusy) {
				t.Fatalf("err = %v, want ErrBusy", err)
			}
			if stored(t, inner, "grafana") != before {
				t.Error("changed")
			}
		})
	}
	t.Run("another failure is not busy", func(t *testing.T) {
		t.Parallel()
		inner := memory.NewSecrets()
		seed(t, inner, "grafana")
		store := &flaky{Secrets: inner, onPut: func(port.Secrets, string, []byte) (string, error) { return "", errors.New("timeout") }}
		m := newManaged(t, store, nil, "grafana")
		if _, err := m.Rotate(ctx0, "grafana", 0); err == nil || errors.Is(err, ErrBusy) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRotateForgetsTheResolverAndTellsChangedOnce(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	before := seed(t, store, "grafana")
	m := newManaged(t, store, nil, "grafana")
	got, _ := m.resolver.Resolve(ctx0, "grafana")
	if got.Current != before.Current {
		t.Fatal("setup")
	}
	if _, err := m.Rotate(ctx0, "grafana", time.Hour); err != nil {
		t.Fatal(err)
	}
	// No time has passed: only a Forget makes the resolver look again.
	now, _ := m.resolver.Resolve(ctx0, "grafana")
	if now.Current == before.Current || now.Previous != before.Current {
		t.Errorf("resolver still serves %+v", now)
	}
}

func TestPurge(t *testing.T) {
	t.Parallel()
	t.Run("refused while the client is generated", func(t *testing.T) {
		t.Parallel()
		store := memory.NewSecrets()
		before := seed(t, store, "grafana")
		m := newManaged(t, store, nil, "grafana")
		if err := m.Purge(ctx0, "grafana"); !errors.Is(err, ErrStillDeclared) {
			t.Errorf("err = %v", err)
		}
		if stored(t, store, "grafana") != before {
			t.Error("a refused purge changed the record")
		}
	})
	t.Run("no record", func(t *testing.T) {
		t.Parallel()
		m := newManaged(t, memory.NewSecrets(), nil)
		if err := m.Purge(ctx0, "gone"); !errors.Is(err, ErrNoRecord) {
			t.Errorf("err = %v", err)
		}
		m.Store = nil
		if err := m.Purge(ctx0, "gone"); !errors.Is(err, ErrNoRecord) {
			t.Errorf("no store: %v", err)
		}
	})
	t.Run("deletes the record and the resolver forgets it", func(t *testing.T) {
		t.Parallel()
		store := memory.NewSecrets()
		seed(t, store, "gone/1")
		m := newManaged(t, store, nil)
		seed(t, store, "gone")
		if _, ok := m.resolver.Resolve(ctx0, "gone"); !ok {
			t.Fatal("setup")
		}
		if err := m.Purge(ctx0, "gone"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx0, Path("gone")); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("the record is still there: %v", err)
		}
		if _, ok := m.resolver.Resolve(ctx0, "gone"); ok {
			t.Error("the resolver still serves a purged client")
		}
		if _, err := store.Get(ctx0, Path("gone/1")); err != nil {
			t.Errorf("another client's record was deleted: %v", err)
		}
		if err := m.Purge(ctx0, "gone"); !errors.Is(err, ErrNoRecord) {
			t.Errorf("a second purge: %v", err)
		}
	})
	t.Run("busy", func(t *testing.T) {
		t.Parallel()
		store := memory.NewSecrets()
		seed(t, store, "gone")
		m := newManaged(t, store, &fakeLock{held: true})
		if err := m.Purge(ctx0, "gone"); !errors.Is(err, ErrBusy) {
			t.Errorf("err = %v", err)
		}
		if _, err := store.Get(ctx0, Path("gone")); err != nil {
			t.Error("a busy purge deleted")
		}
	})
	t.Run("a failing delete", func(t *testing.T) {
		t.Parallel()
		store := &deleteFails{Secrets: memory.NewSecrets()}
		seed(t, store, "gone")
		m := newManaged(t, store, nil)
		if err := m.Purge(ctx0, "gone"); err == nil || errors.Is(err, ErrBusy) {
			t.Errorf("err = %v", err)
		}
	})
}

type deleteFails struct{ port.Secrets }

func (deleteFails) Delete(context.Context, string) error { return errors.New("denied") }

func TestShowReturnsMetadataOnly(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	m := newManaged(t, store, nil, "grafana", "fresh")

	none, err := m.Show(ctx0, "fresh")
	if err != nil || none.Exists || !none.Generated {
		t.Errorf("no record: %+v, %v", none, err)
	}
	undeclared, err := m.Show(ctx0, "other")
	if err != nil || undeclared.Exists || undeclared.Generated {
		t.Errorf("undeclared: %+v, %v", undeclared, err)
	}

	seed(t, store, "grafana")
	m.clock.t = t0.Add(time.Hour)
	if _, err = m.Rotate(ctx0, "grafana", 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	rec := stored(t, store, "grafana")
	meta, err := m.Show(ctx0, "grafana")
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Exists || !meta.Generated || !meta.HasPrevious || !meta.PreviousActive ||
		!meta.Created.Equal(t0) || !meta.Rotated.Equal(t0.Add(time.Hour)) || !meta.PreviousValidUntil.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("meta = %+v", meta)
	}
	for _, v := range []string{rec.Current, rec.Previous} {
		if text := fmt.Sprintf("%+v %#v", meta, meta); strings.Contains(text, v) {
			t.Errorf("Show returned a secret value: %s", text)
		}
	}

	m.clock.t = t0.Add(3 * time.Hour)
	if meta, _ = m.Show(ctx0, "grafana"); !meta.HasPrevious || meta.PreviousActive {
		t.Errorf("at the end of the overlap: %+v", meta)
	}
}

func TestShowErrors(t *testing.T) {
	t.Parallel()
	inner := memory.NewSecrets()
	m := newManaged(t, &flaky{Secrets: inner, getErr: errors.New("sealed")}, nil, "grafana")
	if _, err := m.Show(ctx0, "grafana"); err == nil {
		t.Error("a store error was swallowed")
	}
	if _, err := inner.Put(ctx0, Path("bad"), []byte(`not json leaky-value`)); err != nil {
		t.Fatal(err)
	}
	m = newManaged(t, inner, nil)
	if _, err := m.Show(ctx0, "bad"); err == nil || strings.Contains(err.Error(), "leaky-value") {
		t.Errorf("err = %v", err)
	}
	m.Store = nil
	if meta, err := m.Show(ctx0, "x"); err != nil || meta.Exists {
		t.Errorf("no store: %+v, %v", meta, err)
	}
}

func TestShowShowsAnOrphan(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	seed(t, store, "gone")
	ReconcileOrphans(ctx0, nil, store, t0.Add(time.Minute), quiet(), Hooks{})
	m := newManaged(t, store, nil)
	meta, _ := m.Show(ctx0, "gone")
	if !meta.Exists || meta.Generated || !meta.Orphaned.Equal(t0.Add(time.Minute)) {
		t.Errorf("meta = %+v", meta)
	}
}

// ssmLike is a store whose conditional write is a read and then a write, as
// ssm's is: nothing excludes a second writer that read the same version.
type ssmLike struct {
	port.Secrets
	// barrier, when set, makes each writer wait (bounded) until n writers have
	// done their check, which is what two unserialised rotations do.
	n       int
	read    atomic.Int32
	arrived atomic.Int32
	gate    bool
	written []string
	mu      sync.Mutex
}

// Get, with the gate on, lets no reader go until n have read: so every writer
// holds the same version.
func (s *ssmLike) Get(ctx context.Context, path string) (port.Secret, error) {
	got, err := s.Secrets.Get(ctx, path)
	if s.gate {
		s.read.Add(1)
		deadline := time.Now().Add(300 * time.Millisecond)
		for int(s.read.Load()) < s.n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	return got, err
}

func (s *ssmLike) PutIfVersion(ctx context.Context, path string, value []byte, version string) (string, error) {
	cur, err := s.Secrets.Get(ctx, path)
	switch {
	case version == "" && err == nil:
		return "", port.ErrConflict
	case version != "" && (err != nil || cur.Version != version):
		return "", port.ErrConflict
	}
	if s.n > 1 {
		s.arrived.Add(1)
		deadline := time.Now().Add(300 * time.Millisecond)
		for int(s.arrived.Load()) < s.n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	rec, _ := DecodeRecord(value)
	s.mu.Lock()
	s.written = append(s.written, rec.Current)
	s.mu.Unlock()
	return s.Put(ctx, path, value)
}

func rotateTwice(t *testing.T, store port.Secrets, locks [2]Locker) (errs [2]error) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := newManaged(t, store, locks[i], "grafana")
			<-start
			_, errs[i] = m.Rotate(ctx0, "grafana", time.Hour)
		}()
	}
	close(start)
	wg.Wait()
	return errs
}

func TestTwoRotationsAtOnceOverANonAtomicStoreAreSerialisedByTheLease(t *testing.T) {
	t.Parallel()
	t.Run("without a lease the second write is a lost update", func(t *testing.T) {
		t.Parallel()
		store := &ssmLike{Secrets: memory.NewSecrets()}
		first := seed(t, store, "grafana")
		store.n, store.gate = 2, true
		errs := rotateTwice(t, store, [2]Locker{nil, nil})
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("the control needs both to pass the check: %v", errs)
		}
		// Both rotated the record they read: the second write replaced the
		// first, whose secret is nowhere in the record.
		final := stored(t, store, "grafana")
		w := store.written[1:] // the first is the seed's
		if len(w) != 2 || final.Previous != first.Current || final.Current != w[1] || w[0] == final.Current || w[0] == final.Previous {
			t.Errorf("the control shows no lost update: record %+v, writes %q", final, w)
		}
	})
	t.Run("with the lease nothing is lost", func(t *testing.T) {
		t.Parallel()
		state := memory.New()
		store := &ssmLike{Secrets: memory.NewSecrets()}
		first := seed(t, store, "grafana")
		store.n, store.gate = 2, true
		locks := [2]Locker{
			&rails.Leases{State: state, Holder: "replica-a"},
			&rails.Leases{State: state, Holder: "replica-b"},
		}
		errs := rotateTwice(t, store, locks)
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrBusy):
			default:
				t.Errorf("err = %v", err)
			}
		}
		if ok == 0 {
			t.Fatalf("neither rotation ran: %v", errs)
		}
		if len(store.written) != ok+1 {
			t.Fatalf("%d writes for %d successful rotations", len(store.written)-1, ok)
		}
		final := stored(t, store, "grafana")
		// Whatever ran, each successful rotation built on the one before it.
		chain := append([]string{first.Current}, store.written[1:]...)
		if final.Current != chain[len(chain)-1] || final.Previous != chain[len(chain)-2] {
			t.Errorf("the record is not the end of the chain: %+v", final)
		}
	})
}
