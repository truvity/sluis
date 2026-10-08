package memory_test

import (
	"testing"
	"time"

	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/conformance"
	"github.com/truvity/sluis/storage/state/memory"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(*testing.T) state.Store { return memory.New() })
}

func TestClockAndRevsNotReused(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s := memory.New(memory.WithClock(func() time.Time { return at }))
	r1, err := s.Put(t.Context(), "k", []byte(`{}`), "")
	if err != nil {
		t.Fatal(err)
	}
	it, _ := s.Get(t.Context(), "k")
	if !it.Modified.Equal(at) {
		t.Fatalf("Modified = %v, want %v", it.Modified, at)
	}
	if err := s.Delete(t.Context(), "k"); err != nil {
		t.Fatal(err)
	}
	r2, err := s.Put(t.Context(), "k", []byte(`{}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if r1 == r2 {
		t.Fatalf("Rev %q reused after Delete", r1)
	}
	if _, err := s.GetRev(t.Context(), "k", r1); err == nil {
		t.Fatal("a deleted version is still readable")
	}
}
