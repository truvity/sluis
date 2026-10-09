package clientcreds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/storage/logtest"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/secrets"
)

type orphanHooks struct {
	reported []string
	hooks    Hooks
}

func newOrphanHooks(lock Locker) *orphanHooks {
	o := &orphanHooks{}
	o.hooks = Hooks{Lock: lock, Orphaned: func(_ context.Context, id string) { o.reported = append(o.reported, id) }}
	return o
}

func TestOrphansAreMarkedAndReportedExactlyOnce(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	declared := seed(t, store, "grafana")
	gone := seed(t, store, "gone")
	slashed := seed(t, store, "old/app")
	spaced := seed(t, store, "has space")
	h := newOrphanHooks(nil)

	got := ReconcileOrphans(ctx0, []string{"grafana"}, store, t0, quiet(), h.hooks)
	slices.Sort(got)
	slices.Sort(h.reported)
	want := []string{"gone", "has space", "old/app"}
	if !slices.Equal(got, want) || !slices.Equal(h.reported, want) {
		t.Fatalf("reported %q, hook saw %q; want %q (u-<hex> ids decoded)", got, h.reported, want)
	}
	if stored(t, store, "grafana") != declared {
		t.Error("a declared client's record was touched")
	}
	for id, before := range map[string]Record{"gone": gone, "old/app": slashed, "has space": spaced} {
		after := stored(t, store, id)
		if !after.Orphaned.Equal(t0) {
			t.Errorf("%s: orphaned = %v", id, after.Orphaned)
		}
		after.Orphaned = time.Time{}
		if after != before {
			t.Errorf("%s: the mark changed more than the mark: %+v -> %+v", id, before, after)
		}
	}

	// The next pass finds them marked and says nothing, writing nothing.
	marked := stored(t, store, "gone")
	if again := ReconcileOrphans(ctx0, []string{"grafana"}, store, t0.Add(time.Hour), quiet(), h.hooks); len(again) != 0 {
		t.Errorf("reported again: %v", again)
	}
	if len(h.reported) != 3 {
		t.Errorf("the hook saw %d reports, want 3", len(h.reported))
	}
	if stored(t, store, "gone") != marked {
		t.Error("the mark was rewritten")
	}
}

func TestOrphansAreReportedWithNoGeneratedClientAtAll(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	seed(t, store, "gone")
	h := newOrphanHooks(nil)
	if got := ReconcileOrphans(ctx0, nil, store, t0, quiet(), h.hooks); !slices.Equal(got, []string{"gone"}) {
		t.Errorf("reported %v", got)
	}
}

func TestOrphansIgnorePathsThatAreNotCanonicalRecords(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	body, _ := Record{Current: "x", Created: t0}.Encode()
	for _, p := range []string{
		"credentials/oidc-client/u-6162/secret",    // "ab" is a plain id: its path is not this
		"credentials/oidc-client/u-zz/secret",      // not hex
		"credentials/oidc-client/x/y/secret",       // two segments
		"credentials/oidc-client/x/other",          // not a record
		"credentials/oidc-client/u-612f62/secret2", // not a record
	} {
		if _, err := store.Put(ctx0, p, body); err != nil {
			t.Fatal(err)
		}
	}
	h := newOrphanHooks(nil)
	if got := ReconcileOrphans(ctx0, nil, store, t0, quiet(), h.hooks); len(got) != 0 {
		t.Errorf("reported %v", got)
	}
}

func TestOrphansToleratACorruptRecord(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	if _, err := store.Put(ctx0, Path("bad"), []byte(`{"v":1,"current":"leaky-value","created":`)); err != nil {
		t.Fatal(err)
	}
	seed(t, store, "gone")
	log, logs := logtest.Logger()
	h := newOrphanHooks(nil)
	got := ReconcileOrphans(ctx0, nil, store, t0, log, h.hooks)
	if !slices.Equal(got, []string{"gone"}) {
		t.Errorf("reported %v, want only the readable one", got)
	}
	if logs.CountLevel(slog.LevelWarn) == 0 || !logs.Mentions("bad") {
		t.Errorf("no warning for the corrupt record: %q", logs.Messages())
	}
	if logs.Mentions("leaky-value") {
		t.Error("the log holds a value")
	}
}

func TestOrphansAConflictIsRetriedNextPass(t *testing.T) {
	t.Parallel()
	inner := memory.NewSecrets()
	seed(t, inner, "gone")
	var calls atomic.Int32
	store := &flaky{Secrets: inner, onPut: func(in port.Secrets, path string, value []byte) (string, error) {
		if calls.Add(1) == 1 {
			return "", port.ErrConflict
		}
		return in.PutIfVersion(ctx0, path, value, currentVersion(in, path))
	}}
	h := newOrphanHooks(nil)
	if got := ReconcileOrphans(ctx0, nil, store, t0, quiet(), h.hooks); len(got) != 0 || len(h.reported) != 0 {
		t.Fatalf("a lost write was reported: %v", got)
	}
	if !stored(t, inner, "gone").Orphaned.IsZero() {
		t.Fatal("marked despite the conflict")
	}
	if got := ReconcileOrphans(ctx0, nil, store, t0, quiet(), h.hooks); !slices.Equal(got, []string{"gone"}) {
		t.Errorf("the retry reported %v", got)
	}
	if len(h.reported) != 1 {
		t.Errorf("reported %d times", len(h.reported))
	}
}

func currentVersion(s port.Secrets, path string) string {
	got, _ := s.Get(ctx0, path)
	return got.Version
}

func TestOrphansAWriteFailureIsLeftForTheNextPass(t *testing.T) {
	t.Parallel()
	inner := memory.NewSecrets()
	seed(t, inner, "gone")
	store := &flaky{Secrets: inner, onPut: func(port.Secrets, string, []byte) (string, error) { return "", errors.New("timeout") }}
	h := newOrphanHooks(nil)
	if got := ReconcileOrphans(ctx0, nil, store, t0, quiet(), h.hooks); len(got) != 0 {
		t.Errorf("reported %v", got)
	}
}

func TestOrphansASecretsStoreThatCannotListIsTolerated(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"unsupported": fmt.Errorf("%w: no list", port.ErrUnsupported),
		"not found":   port.ErrNotFound,
		"a failure":   errors.New("sealed"),
	} {
		store := &listFails{Secrets: memory.NewSecrets(), err: err}
		h := newOrphanHooks(nil)
		if got := ReconcileOrphans(ctx0, []string{"x"}, store, t0, quiet(), h.hooks); len(got) != 0 {
			t.Errorf("%s: reported %v", name, got)
		}
	}
	if got := ReconcileOrphans(ctx0, nil, nil, t0, nil, Hooks{}); got != nil {
		t.Errorf("no store: %v", got)
	}
}

type listFails struct {
	port.Secrets
	err error
}

func (l *listFails) List(context.Context, string) ([]string, error) { return nil, l.err }

func TestOrphansAHeldLeaseLeavesTheRecordForLater(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	seed(t, store, "gone")
	lock := &fakeLock{held: true}
	h := newOrphanHooks(lock)
	if got := ReconcileOrphans(ctx0, nil, store, t0, quiet(), h.hooks); len(got) != 0 {
		t.Errorf("reported %v under a held lease", got)
	}
	if !stored(t, store, "gone").Orphaned.IsZero() {
		t.Error("marked under a held lease")
	}
	if len(lock.targets) != 1 || lock.targets[0] != "client-secret:gone" {
		t.Errorf("lease = %v", lock.targets)
	}
	lock.held = false
	if got := ReconcileOrphans(ctx0, nil, store, t0, quiet(), h.hooks); len(got) != 1 {
		t.Errorf("after the lease was free: %v", got)
	}
}

func TestAClientThatReturnsIsRestoredAndItsRecordIsUnchanged(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]Input{
		"no input":                  nil,
		"an input with other value": &inputs{values: map[string]string{secrets.ClientSecret("gone"): "a-different-secret"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := memory.NewSecrets()
			m := newManaged(t, store, nil, "gone")
			original := seed(t, store, "gone")
			if _, err := m.Rotate(ctx0, "gone", time.Hour); err != nil {
				t.Fatal(err)
			}
			rotated := stored(t, store, "gone")
			ReconcileOrphans(ctx0, nil, store, t0.Add(time.Minute), quiet(), Hooks{})
			if stored(t, store, "gone").Orphaned.IsZero() {
				t.Fatal("setup: not marked")
			}

			var seen seen
			res := Reconcile(ctx0, []string{"gone"}, store, in, t0.Add(time.Hour), quiet(), seen.hooks())
			if res.Outcomes["gone"] != OutcomeRestored || res.Failed() != 0 {
				t.Fatalf("res = %+v", res)
			}
			back := stored(t, store, "gone")
			if !back.Orphaned.IsZero() {
				t.Error("still marked")
			}
			back.Orphaned = time.Time{}
			if back != rotated || back.Current == original.Current {
				t.Errorf("the record changed beyond the mark: %+v vs %+v", back, rotated)
			}
			if len(seen.got["gone"]) != 1 || seen.got["gone"][0] != OutcomeRestored || seen.errs["gone"][0] != nil {
				t.Errorf("hook = %v %v", seen.got, seen.errs)
			}
			// Settled: the next pass finds it as it is, and does not mark it again.
			if again := Reconcile(ctx0, []string{"gone"}, store, in, t0.Add(2*time.Hour), quiet(), Hooks{}); again.Outcomes["gone"] != OutcomeExisting {
				t.Errorf("second pass = %q", again.Outcomes["gone"])
			}
		})
	}
}

func TestRestoreUnderAHeldLeaseChangesNothing(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	seed(t, store, "gone")
	ReconcileOrphans(ctx0, nil, store, t0, quiet(), Hooks{})
	marked := stored(t, store, "gone")
	res := Reconcile(ctx0, []string{"gone"}, store, nil, t0, quiet(), Hooks{Lock: &fakeLock{held: true}})
	if res.Outcomes["gone"] != OutcomeExisting || stored(t, store, "gone") != marked {
		t.Errorf("res = %+v", res)
	}
	res = Reconcile(ctx0, []string{"gone"}, store, nil, t0, quiet(), Hooks{Lock: &fakeLock{err: errors.New("down")}})
	if res.Outcomes["gone"] != OutcomeFailed {
		t.Errorf("a lease that cannot be taken: %+v", res)
	}
}

func TestRestoreConflictIsRetriedNextPass(t *testing.T) {
	t.Parallel()
	inner := memory.NewSecrets()
	seed(t, inner, "gone")
	ReconcileOrphans(ctx0, nil, inner, t0, quiet(), Hooks{})
	var calls atomic.Int32
	store := &flaky{Secrets: inner, onPut: func(in port.Secrets, path string, value []byte) (string, error) {
		if calls.Add(1) == 1 {
			return "", port.ErrConflict
		}
		return in.PutIfVersion(ctx0, path, value, currentVersion(in, path))
	}}
	if res := Reconcile(ctx0, []string{"gone"}, store, nil, t0, quiet(), Hooks{}); res.Outcomes["gone"] != OutcomeExisting {
		t.Fatalf("first = %+v", res)
	}
	if res := Reconcile(ctx0, []string{"gone"}, store, nil, t0, quiet(), Hooks{}); res.Outcomes["gone"] != OutcomeRestored {
		t.Errorf("retry = %+v", res)
	}
}
