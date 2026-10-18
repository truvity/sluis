package recoverygate_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/recoverygate"
)

// counting counts the reads of the record and can fail them.
type counting struct {
	port.State
	gets atomic.Int32
	err  error
}

func (c *counting) Get(ctx context.Context, key string) (port.Record, error) {
	c.gets.Add(1)
	if c.err != nil {
		return port.Record{}, c.err
	}
	return c.State.Get(ctx, key)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func off(t *testing.T, g *recoverygate.Gate) bool {
	t.Helper()
	got, _ := g.Off(t.Context())
	return got
}

func TestRecoveryIsOnUntilAnOperatorDisablesIt(t *testing.T) {
	t.Parallel()
	g := recoverygate.New(memory.New(), "")
	if off(t, g) {
		t.Fatal("recovery was off with no record")
	}
	rec, wrote, err := g.Disable(t.Context(), "ada@example.com")
	if err != nil || !wrote {
		t.Fatalf("Disable: wrote=%v err=%v", wrote, err)
	}
	if rec.By != "ada@example.com" || rec.Generation != "" || rec.At.IsZero() {
		t.Errorf("record: %+v", rec)
	}
	if !off(t, g) {
		t.Error("recovery was still on after the disable")
	}
}

func TestADisableIsIdempotentAndKeepsTheFirstRecord(t *testing.T) {
	t.Parallel()
	st := memory.New()
	first, wrote, err := recoverygate.New(st, "g1").Disable(t.Context(), "ada@example.com")
	if err != nil || !wrote {
		t.Fatalf("first: wrote=%v err=%v", wrote, err)
	}
	// Another container, which never saw the first.
	again, wrote, err := recoverygate.New(st, "g1").Disable(t.Context(), "bob@example.com")
	if err != nil || wrote {
		t.Fatalf("second: wrote=%v err=%v", wrote, err)
	}
	if again.By != "ada@example.com" || !again.At.Equal(first.At) {
		t.Errorf("the second disable replaced the first: %+v vs %+v", again, first)
	}
}

func TestAColdStartWithTheSameConfigurationStaysDisabled(t *testing.T) {
	t.Parallel()
	st := memory.New()
	if _, _, err := recoverygate.New(st, "2026-10").Disable(t.Context(), "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	// A new execution environment: a new gate over the same State and the same document.
	if !off(t, recoverygate.New(st, "2026-10")) {
		t.Error("a redeploy of the same configuration turned recovery back on")
	}
}

func TestChangingTheGenerationTurnsRecoveryBackOn(t *testing.T) {
	t.Parallel()
	st := memory.New()
	if _, _, err := recoverygate.New(st, "a").Disable(t.Context(), "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	g := recoverygate.New(st, "b")
	if off(t, g) {
		t.Fatal("a bumped generation left recovery off")
	}
	status, err := g.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Disabled != nil || status.Superseded == nil || status.Superseded.Generation != "a" {
		t.Errorf("status: %+v", status)
	}
	// And a disable of the new generation adds to the set: both stay off.
	rec, wrote, err := g.Disable(t.Context(), "bob@example.com")
	if err != nil || !wrote || rec.Generation != "b" {
		t.Fatalf("disable at b: %+v wrote=%v err=%v", rec, wrote, err)
	}
	if !off(t, recoverygate.New(st, "b")) || !off(t, recoverygate.New(st, "a")) || off(t, recoverygate.New(st, "c")) {
		t.Error("the record covers exactly the generations that were disabled")
	}
}

func TestAnotherContainerSeesTheDisableWithinTheTTLAndNeverReadsAgainOnceOff(t *testing.T) {
	t.Parallel()
	mem := memory.New()
	st := &counting{State: mem}
	clk := &clock{t: time.Unix(1_000, 0)}
	reader := recoverygate.New(st, "", recoverygate.WithClock(clk.now), recoverygate.WithTTL(3*time.Second))
	for range 2 {
		if off(t, reader) {
			t.Fatal("on")
		}
	}
	if got := st.gets.Load(); got != 1 {
		t.Fatalf("two checks inside the TTL read %d times, want 1", got)
	}
	if _, _, err := recoverygate.New(mem, "").Disable(t.Context(), "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	if off(t, reader) {
		t.Error("the reader saw the disable before its TTL ran out (and read nothing)")
	}
	clk.add(3 * time.Second)
	if !off(t, reader) {
		t.Fatal("the reader did not see the disable after the TTL")
	}
	reads := st.gets.Load()
	clk.add(time.Hour)
	if !off(t, reader) || st.gets.Load() != reads {
		t.Errorf("an off gate read again (%d reads, was %d)", st.gets.Load(), reads)
	}
}

func TestAStateThatCannotBeReadMeansOff(t *testing.T) {
	t.Parallel()
	st := &counting{State: memory.New(), err: errors.New("dynamodb: throttled")}
	got, err := recoverygate.New(st, "").Off(t.Context())
	if !got || err == nil {
		t.Errorf("Off = %v, %v; a door that cannot be checked stays shut and says why", got, err)
	}
}

func TestAnUnreadableRecordSwitchesRecoveryOffForEveryGeneration(t *testing.T) {
	t.Parallel()
	st := memory.New()
	if _, err := st.Put(t.Context(), recoverygate.Key, []byte("{not json"), 0); err != nil {
		t.Fatal(err)
	}
	if !off(t, recoverygate.New(st, "")) || !off(t, recoverygate.New(st, "x")) {
		t.Error("a corrupt record turned recovery on")
	}
	// A disable replaces it with a readable one.
	g := recoverygate.New(st, "x")
	if _, wrote, err := g.Disable(t.Context(), "ada@example.com"); err != nil || !wrote {
		t.Fatalf("disable over a corrupt record: wrote=%v err=%v", wrote, err)
	}
	if off(t, recoverygate.New(st, "y")) {
		t.Error("the replaced record still covers every generation")
	}
	if !off(t, recoverygate.New(st, "x")) {
		t.Error("the replacing record does not cover the generation it was made for")
	}
}

func TestNoStateCannotDisableAndSwitchesNothingOff(t *testing.T) {
	t.Parallel()
	for name, g := range map[string]*recoverygate.Gate{"nil gate": nil, "nil state": recoverygate.New(nil, "")} {
		if off(t, g) {
			t.Errorf("%s: off", name)
		}
		if _, _, err := g.Disable(t.Context(), "ada@example.com"); !errors.Is(err, recoverygate.ErrUnsupported) {
			t.Errorf("%s: Disable = %v, want ErrUnsupported", name, err)
		}
		if g.Durable() {
			t.Errorf("%s: durable", name)
		}
	}
}

func TestADisableRaceWritesOneRecord(t *testing.T) {
	t.Parallel()
	st := memory.New()
	var wrote atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, w, err := recoverygate.New(st, "").Disable(t.Context(), "ada@example.com"); err != nil {
				t.Error(err)
			} else if w {
				wrote.Add(1)
			}
		})
	}
	wg.Wait()
	if wrote.Load() != 1 {
		t.Errorf("%d callers wrote the record, want 1", wrote.Load())
	}
}

// Generations are unordered, so a container still running the older one (a
// rolling deploy) must not be able to undo the disable of the newer.
func TestADisableFromAnOlderGenerationDoesNotReenableTheNewer(t *testing.T) {
	t.Parallel()
	st := memory.New()
	if _, _, err := recoverygate.New(st, "B").Disable(t.Context(), "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, wrote, err := recoverygate.New(st, "A").Disable(t.Context(), "bob@example.com"); err != nil || !wrote {
		t.Fatalf("disable at A: wrote=%v err=%v", wrote, err)
	}
	for _, generation := range []string{"A", "B"} {
		if !off(t, recoverygate.New(st, generation)) {
			t.Errorf("generation %s is on after both were disabled", generation)
		}
	}
	// And the entry of B is still ada's.
	status, err := recoverygate.New(st, "B").Status(t.Context())
	if err != nil || status.Disabled == nil || status.Disabled.By != "ada@example.com" {
		t.Errorf("B's entry = %+v, %v", status.Disabled, err)
	}
	// Reusing a generation that was disabled once keeps recovery off.
	if !off(t, recoverygate.New(st, "A")) {
		t.Error("reusing a disabled generation turned recovery on")
	}
}

// A State that cannot hold the record holds no switch: recovery is never off
// there, which is not a lockout, and the action is withheld.
func TestAStateThatDoesNotSupportTheRecordIsNeverOffAndNotDurable(t *testing.T) {
	t.Parallel()
	st := &counting{State: memory.New(), err: port.ErrUnsupported}
	g := recoverygate.New(st, "")
	got, err := g.Off(t.Context())
	if got || err != nil {
		t.Errorf("Off = %v, %v; want it never off, without an error", got, err)
	}
	if g.Durable() {
		t.Error("durable")
	}
	if _, _, err = g.Disable(t.Context(), "ada@example.com"); !errors.Is(err, recoverygate.ErrUnsupported) {
		t.Errorf("Disable = %v, want ErrUnsupported", err)
	}
	status, err := g.Status(t.Context())
	if err != nil || status.Durable {
		t.Errorf("Status = %+v, %v", status, err)
	}
}
