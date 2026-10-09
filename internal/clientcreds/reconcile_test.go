package clientcreds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/secrets"
)

var (
	ctx0 = context.Background()
	t0   = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
)

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// inputs is a fake secrets.Source by name: a value, or an error for a name.
type inputs struct {
	mu     sync.Mutex
	values map[string]string
	errs   map[string]error
	reads  []string
}

func (i *inputs) Get(_ context.Context, name string) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reads = append(i.reads, name)
	if err := i.errs[name]; err != nil {
		return "", err
	}
	v, ok := i.values[name]
	if !ok {
		return "", fmt.Errorf("%w: %s", secrets.ErrNotFound, name)
	}
	return v, nil
}

// flaky wraps a store and lets a test change what a call does.
type flaky struct {
	port.Secrets
	mu     sync.Mutex
	puts   int
	getErr error
	// onPut, when set, answers PutIfVersion instead of the store.
	onPut func(inner port.Secrets, path string, value []byte) (string, error)
}

func (f *flaky) Get(ctx context.Context, path string) (port.Secret, error) {
	f.mu.Lock()
	err := f.getErr
	f.mu.Unlock()
	if err != nil {
		return port.Secret{}, err
	}
	return f.Secrets.Get(ctx, path)
}

func (f *flaky) PutIfVersion(ctx context.Context, path string, value []byte, version string) (string, error) {
	f.mu.Lock()
	f.puts++
	hook := f.onPut
	f.mu.Unlock()
	if hook != nil {
		return hook(f.Secrets, path, value)
	}
	return f.Secrets.PutIfVersion(ctx, path, value, version)
}

func stored(t *testing.T, s port.Secrets, id string) Record {
	t.Helper()
	got, err := s.Get(ctx0, Path(id))
	if err != nil {
		t.Fatalf("read the record of %q: %v", id, err)
	}
	rec, err := DecodeRecord(got.Value)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

type seen struct {
	mu   sync.Mutex
	got  map[string][]Outcome
	errs map[string][]error
}

func (s *seen) hooks() Hooks {
	s.got, s.errs = map[string][]Outcome{}, map[string][]error{}
	return Hooks{Outcome: func(_ context.Context, id string, o Outcome, err error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.got[id] = append(s.got[id], o)
		s.errs[id] = append(s.errs[id], err)
	}}
}

func TestReconcileCreatesThenFindsTheRecord(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	var h seen
	first := Reconcile(ctx0, []string{"grafana"}, store, nil, t0, quiet(), h.hooks())
	if first.Outcomes["grafana"] != OutcomeCreated || first.Failed() != 0 {
		t.Fatalf("first = %+v", first)
	}
	rec := stored(t, store, "grafana")
	if len(rec.Current) != 43 || !rec.Created.Equal(t0) || rec.Previous != "" || !rec.Rotated.IsZero() {
		t.Errorf("record = %+v", rec)
	}

	second := Reconcile(ctx0, []string{"grafana"}, store, nil, t0.Add(time.Hour), quiet(), h.hooks())
	if second.Outcomes["grafana"] != OutcomeExisting {
		t.Fatalf("second = %+v", second)
	}
	if again := stored(t, store, "grafana"); again != rec {
		t.Errorf("an existing record was changed: %+v -> %+v", rec, again)
	}
}

func TestReconcileAdoptsTheInputSecret(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	in := &inputs{values: map[string]string{secrets.ClientSecret("grafana"): "already-in-use"}}
	res := Reconcile(ctx0, []string{"grafana"}, store, in, t0, quiet(), Hooks{})
	if res.Outcomes["grafana"] != OutcomeAdopted {
		t.Fatalf("outcome = %q", res.Outcomes["grafana"])
	}
	if rec := stored(t, store, "grafana"); rec.Current != "already-in-use" {
		t.Errorf("stored current = %q, want the input", rec.Current)
	}
	// The input changing later does not move a record that exists.
	in.values[secrets.ClientSecret("grafana")] = "changed"
	if res = Reconcile(ctx0, []string{"grafana"}, store, in, t0, quiet(), Hooks{}); res.Outcomes["grafana"] != OutcomeExisting {
		t.Errorf("second outcome = %q", res.Outcomes["grafana"])
	}
	if rec := stored(t, store, "grafana"); rec.Current != "already-in-use" {
		t.Errorf("an existing record followed the input: %q", rec.Current)
	}
}

func TestReconcileGeneratesWhenTheInputHasNothingUsable(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		id string
		in Input
	}{
		"no input":        {"grafana", nil},
		"input not found": {"grafana", &inputs{}},
		"input empty":     {"grafana", &inputs{values: map[string]string{secrets.ClientSecret("grafana"): ""}}},
		"id with a slash": {"a/b", &inputs{values: map[string]string{secrets.ClientSecret("a/b"): "x"}}},
		"id not a name":   {"has space", &inputs{values: map[string]string{secrets.ClientSecret("has space"): "x"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := memory.NewSecrets()
			res := Reconcile(ctx0, []string{tc.id}, store, tc.in, t0, nil, Hooks{})
			if res.Outcomes[tc.id] != OutcomeCreated {
				t.Fatalf("outcome = %q", res.Outcomes[tc.id])
			}
			if rec := stored(t, store, tc.id); len(rec.Current) != 43 {
				t.Errorf("current = %q, want a generated secret", rec.Current)
			}
		})
	}
}

func TestReconcileALostCreateRaceKeepsTheOtherWritersRecord(t *testing.T) {
	t.Parallel()
	inner := memory.NewSecrets()
	winner := Record{V: RecordVersion, Current: "the-winner", Created: t0.Add(-time.Second)}
	body, _ := winner.Encode()
	store := &flaky{Secrets: inner, onPut: func(in port.Secrets, path string, _ []byte) (string, error) {
		// The other writer lands between our read and our create.
		if _, err := in.Put(ctx0, path, body); err != nil {
			t.Error(err)
		}
		return "", port.ErrConflict
	}}
	var h seen
	res := Reconcile(ctx0, []string{"grafana"}, store, nil, t0, quiet(), h.hooks())
	if res.Outcomes["grafana"] != OutcomeConflict || res.Failed() != 0 {
		t.Fatalf("res = %+v", res)
	}
	if store.puts != 1 {
		t.Errorf("PutIfVersion was called %d times; a lost race is never retried", store.puts)
	}
	if rec := stored(t, inner, "grafana"); rec.Current != "the-winner" {
		t.Errorf("the record was overwritten: %+v", rec)
	}
	if h.errs["grafana"][0] != nil {
		t.Errorf("a conflict is not an error: %v", h.errs["grafana"][0])
	}
}

func TestReconcileAConflictWhoseRecordCannotBeReadFails(t *testing.T) {
	t.Parallel()
	store := &flaky{Secrets: memory.NewSecrets(), onPut: func(port.Secrets, string, []byte) (string, error) {
		return "", port.ErrConflict
	}}
	res := Reconcile(ctx0, []string{"grafana"}, store, nil, t0, quiet(), Hooks{})
	// Get says not found, though the conflict said it exists: do not claim
	// a record is in place.
	if res.Outcomes["grafana"] != OutcomeFailed || res.Failed() != 1 {
		t.Errorf("res = %+v", res)
	}
}

func TestReconcileAStoreThatCannotCreateOnlyIfAbsentIsUnsupported(t *testing.T) {
	t.Parallel()
	store := &flaky{Secrets: memory.NewSecrets(), onPut: func(port.Secrets, string, []byte) (string, error) {
		return "", fmt.Errorf("%w: no create-only", port.ErrUnsupported)
	}}
	var h seen
	res := Reconcile(ctx0, []string{"grafana"}, store, nil, t0, quiet(), h.hooks())
	if res.Outcomes["grafana"] != OutcomeUnsupported || res.Failed() != 1 {
		t.Fatalf("res = %+v", res)
	}
	if !errors.Is(h.errs["grafana"][0], port.ErrUnsupported) {
		t.Errorf("the hook's error = %v, want ErrUnsupported", h.errs["grafana"][0])
	}
	if _, err := store.Secrets.Get(ctx0, Path("grafana")); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("a record exists after an unsupported write: %v", err)
	}
}

func TestReconcileWithNoStoreIsUnsupported(t *testing.T) {
	t.Parallel()
	res := Reconcile(ctx0, []string{"a", "b"}, nil, nil, t0, quiet(), Hooks{})
	if res.Outcomes["a"] != OutcomeUnsupported || res.Outcomes["b"] != OutcomeUnsupported || res.Failed() != 2 {
		t.Errorf("res = %+v", res)
	}
}

func TestReconcileAnInputThatCannotBeReadGeneratesNothing(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	name := secrets.ClientSecret("grafana")
	in := &inputs{errs: map[string]error{name: errors.New("ssm throttled")}}
	var h seen
	res := Reconcile(ctx0, []string{"grafana"}, store, in, t0, quiet(), h.hooks())
	if res.Outcomes["grafana"] != OutcomeFailed || res.Failed() != 1 {
		t.Fatalf("res = %+v", res)
	}
	if _, err := store.Get(ctx0, Path("grafana")); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("a secret was made over an input that is there but unread: %v", err)
	}
	if h.errs["grafana"][0] == nil {
		t.Error("the hook was not given the error")
	}
	// The next pass, with the input readable, adopts it.
	in.errs = nil
	in.values = map[string]string{name: "kept"}
	if res = Reconcile(ctx0, []string{"grafana"}, store, in, t0, quiet(), Hooks{}); res.Outcomes["grafana"] != OutcomeAdopted {
		t.Errorf("retry = %+v", res)
	}
}

func TestReconcileAStoreReadErrorFailsOnlyThatPass(t *testing.T) {
	t.Parallel()
	store := &flaky{Secrets: memory.NewSecrets(), getErr: errors.New("openbao sealed")}
	res := Reconcile(ctx0, []string{"grafana"}, store, nil, t0, quiet(), Hooks{})
	if res.Outcomes["grafana"] != OutcomeFailed || store.puts != 0 {
		t.Errorf("res = %+v, puts = %d: a failed read must not lead to a write", res, store.puts)
	}
}

func TestReconcileAWriteErrorFails(t *testing.T) {
	t.Parallel()
	store := &flaky{Secrets: memory.NewSecrets(), onPut: func(port.Secrets, string, []byte) (string, error) {
		return "", errors.New("timeout")
	}}
	res := Reconcile(ctx0, []string{"grafana"}, store, nil, t0, quiet(), Hooks{})
	if res.Outcomes["grafana"] != OutcomeFailed {
		t.Errorf("res = %+v", res)
	}
}

func TestReconcileOneClientsFailureDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	in := &inputs{
		values: map[string]string{secrets.ClientSecret("adopted"): "kept"},
		errs:   map[string]error{secrets.ClientSecret("broken"): errors.New("boom")},
	}
	// Order matters: the broken one is in the middle.
	ids := []string{"adopted", "broken", "fresh"}
	var h seen
	res := Reconcile(ctx0, ids, store, in, t0, quiet(), h.hooks())
	want := map[string]Outcome{"adopted": OutcomeAdopted, "broken": OutcomeFailed, "fresh": OutcomeCreated}
	for id, o := range want {
		if res.Outcomes[id] != o {
			t.Errorf("%s = %q, want %q", id, res.Outcomes[id], o)
		}
	}
	if res.Failed() != 1 {
		t.Errorf("Failed = %d, want 1", res.Failed())
	}
	for _, id := range ids {
		if n := len(h.got[id]); n != 1 {
			t.Errorf("the hook fired %d times for %s, want exactly once", n, id)
		}
		if h.got[id][0] != want[id] {
			t.Errorf("the hook saw %q for %s, want %q", h.got[id][0], id, want[id])
		}
	}
	if _, err := store.Get(ctx0, Path("broken")); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("the broken client has a record: %v", err)
	}
}

func TestReconcileResultFailedCountsFailedAndUnsupportedOnly(t *testing.T) {
	t.Parallel()
	r := Result{Outcomes: map[string]Outcome{
		"a": OutcomeCreated, "b": OutcomeAdopted, "c": OutcomeExisting, "d": OutcomeConflict,
		"e": OutcomeFailed, "f": OutcomeUnsupported,
	}}
	if got := r.Failed(); got != 2 {
		t.Errorf("Failed = %d, want 2", got)
	}
	if (Result{}).Failed() != 0 {
		t.Error("an empty result has failures")
	}
}

func TestReconcileNothingToDo(t *testing.T) {
	t.Parallel()
	res := Reconcile(ctx0, nil, memory.NewSecrets(), nil, t0, nil, Hooks{})
	if len(res.Outcomes) != 0 {
		t.Errorf("outcomes = %v", res.Outcomes)
	}
}

// N passes at once, the way N replicas starting together do: one value is made
// and everybody ends up serving it.
func TestReconcileConcurrentPassesMakeExactlyOneRecord(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]*inputs{
		"generated": nil,
		"adopted":   {values: map[string]string{secrets.ClientSecret("grafana"): "from-the-input"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			const n = 32
			store := memory.NewSecrets()
			var input Input
			if in != nil {
				input = in
			}
			var (
				wg      sync.WaitGroup
				start   = make(chan struct{})
				results = make([]Outcome, n)
			)
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					res := Reconcile(ctx0, []string{"grafana"}, store, input, t0, quiet(), Hooks{})
					results[i] = res.Outcomes["grafana"]
				}()
			}
			close(start)
			wg.Wait()

			counts := map[Outcome]int{}
			for _, o := range results {
				counts[o]++
			}
			made := counts[OutcomeCreated] + counts[OutcomeAdopted]
			if made != 1 {
				t.Errorf("%d passes made the record, want exactly one: %v", made, counts)
			}
			if counts[OutcomeCreated]+counts[OutcomeAdopted]+counts[OutcomeExisting]+counts[OutcomeConflict] != n {
				t.Errorf("an outcome that is none of the four: %v", counts)
			}
			if in != nil && counts[OutcomeAdopted] != 1 {
				t.Errorf("the input was not adopted: %v", counts)
			}
			if in == nil && counts[OutcomeCreated] != 1 {
				t.Errorf("nothing was created: %v", counts)
			}

			// Every resolver, fresh, serves the same current value, the stored one.
			want := stored(t, store, "grafana").Current
			if in != nil && want != "from-the-input" {
				t.Errorf("stored current = %q, want the input", want)
			}
			var rg sync.WaitGroup
			got := make([]string, n)
			for i := range n {
				rg.Add(1)
				go func() {
					defer rg.Done()
					s, ok := NewResolver(store, input, quiet()).Resolve(ctx0, "grafana")
					if ok {
						got[i] = s.Current
					}
				}()
			}
			rg.Wait()
			for i, v := range got {
				if v != want {
					t.Errorf("resolver %d got %q, want %q", i, v, want)
				}
			}
		})
	}
}

func TestReconcileACorruptRecordIsFailedNeverReplacedNorServedFromTheInput(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"not json":      `leaky-value`,
		"wrong version": `{"v":9,"current":"leaky-value"}`,
		"no current":    `{"v":1,"current":""}`,
	} {
		store := memory.NewSecrets()
		if _, err := store.Put(ctx0, Path("grafana"), []byte(body)); err != nil {
			t.Fatal(err)
		}
		in := &inputs{values: map[string]string{secrets.ClientSecret("grafana"): "input"}}
		var h seen
		res := Reconcile(ctx0, []string{"grafana"}, store, in, t0, quiet(), h.hooks())
		if res.Outcomes["grafana"] != OutcomeFailed || res.Failed() != 1 {
			t.Errorf("%s: %+v", name, res)
		}
		got, _ := store.Get(ctx0, Path("grafana"))
		if string(got.Value) != body {
			t.Errorf("%s: the record was replaced", name)
		}
		if err := h.errs["grafana"][0]; err == nil || strings.Contains(err.Error(), "leaky-value") {
			t.Errorf("%s: hook error %v", name, err)
		}
		if len(in.reads) != 0 {
			t.Errorf("%s: the input was read", name)
		}
	}
}
