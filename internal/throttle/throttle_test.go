package throttle_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/throttle"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func TestOneSourceCannotLockAnotherOut(t *testing.T) {
	t.Parallel()
	clk := &clock{now: time.Now()}
	l := throttle.New(3, 100, time.Minute, 0, clk.Now)

	for range 3 {
		if l.Blocked("203.0.113.9") {
			t.Fatal("blocked before its refusals were spent")
		}
		l.Refused("203.0.113.9")
	}
	if !l.Blocked("203.0.113.9") {
		t.Error("a source that spent its refusals is still answered")
	}
	if l.Blocked("198.51.100.1") {
		t.Error("another source is locked out by the first one's refusals")
	}

	clk.now = clk.now.Add(time.Minute)
	if l.Blocked("203.0.113.9") {
		t.Error("still blocked after the window")
	}
}

func TestTheGlobalCeilingStopsManySources(t *testing.T) {
	t.Parallel()
	clk := &clock{now: time.Now()}
	l := throttle.New(3, 10, time.Minute, 0, clk.Now)

	for i := range 10 {
		l.Refused(fmt.Sprintf("192.0.2.%d", i))
	}
	if !l.Blocked("192.0.2.200") {
		t.Error("a source nobody has seen is answered past the global ceiling")
	}
	clk.now = clk.now.Add(time.Minute)
	if l.Blocked("192.0.2.200") {
		t.Error("the ceiling outlives its window")
	}
}

func TestAnAcceptedProofClearsItsSourceOnly(t *testing.T) {
	t.Parallel()
	l := throttle.New(2, 100, time.Minute, 0, nil)
	l.Refused("a")
	l.Refused("a")
	l.Refused("b")
	l.Accepted("a")
	if l.Blocked("a") {
		t.Error("a source whose proof was accepted is still blocked")
	}
	l.Refused("b")
	if !l.Blocked("b") {
		t.Error("an accepted proof from one source cleared another's count")
	}
}

func TestTheTableIsBounded(t *testing.T) {
	t.Parallel()
	clk := &clock{now: time.Now()}
	l := throttle.New(2, 1_000_000, time.Minute, 8, clk.Now)

	for i := range 1000 {
		l.Refused(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	if l.Len() > 9 {
		t.Errorf("the table holds %d sources, want at most 8 and the shared overflow", l.Len())
	}
	// Sources beyond the table share a count, so the flood is slowed.
	if !l.Blocked("10.9.9.9") {
		t.Error("a new source during a flood is not slowed by the shared count")
	}

	// Once the window is over the table frees its places, and a new source is
	// told apart again rather than lumped with the flood.
	clk.now = clk.now.Add(2 * time.Minute)
	l.Refused("10.1.0.1")
	l.Refused("10.1.0.1")
	if !l.Blocked("10.1.0.1") {
		t.Error("a source after the window is not counted on its own")
	}
	if l.Blocked("10.1.0.2") {
		t.Error("a quiet source after the window is lumped with the flood")
	}
}
