package hub_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/truvity/sluis/backend/fake"
	"github.com/truvity/sluis/internal/hub"
)

func directoryCalls(b *fake.Backend) int {
	n := 0
	for _, op := range []fake.Op{fake.OpTenant, fake.OpProbe, fake.OpAccounts, fake.OpGroups, fake.OpAccount, fake.OpGroupsOf, fake.OpRevoke} {
		n += b.Calls(op)
	}
	return n
}

// With the directory in another module, a sign-in that finds the snapshot due
// or stale, or an address missing from it, makes no call to the directory and
// starts no refresh: the hub serves what the module wrote.
func TestAReaderHubMakesNoDirectoryCallWhateverTheSnapshotsAge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	directory := fake.New("C0north", "north.example").WithAccount("ada@north.example", "Ada", "North")
	h := hub.New(hub.NewMemoryStore(), hub.NewMemorySnapshots(), hub.Config{}, slog.New(slog.DiscardHandler))
	h.SetClock(func() time.Time { return clock })
	if _, err := h.Adopt(ctx, hub.Workspace{Admin: "admin@north.example"}, directory); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	h.Wait()
	h.UseRequestRefresh(time.Second)
	h.UseReader()
	h.UseRequestRefresh(time.Second) // the reader wins
	if !h.Reader() {
		t.Fatal("Reader() = false after UseReader")
	}
	before := directoryCalls(directory)

	zero := time.Duration(0)
	for _, age := range []time.Duration{5 * time.Minute, 16 * time.Minute, 31 * time.Minute, 24 * time.Hour} {
		clock = clock.Add(age)
		for _, maxAge := range []*time.Duration{nil, &zero} {
			got, err := h.ResolveUser(ctx, "ada@north.example", maxAge)
			if err != nil {
				t.Fatalf("ResolveUser after %s: %v", age, err)
			}
			if !got.Found {
				t.Errorf("after %s the snapshot's account was not served", age)
			}
			// An address the snapshot lacks would go live in the hub that owns
			// its directory.
			if _, err = h.ResolveUser(ctx, "grace@north.example", maxAge); err != nil {
				t.Fatalf("ResolveUser (missing) after %s: %v", age, err)
			}
		}
	}
	h.Wait()

	if _, err := h.Refresh(ctx, "C0north"); !errors.Is(err, hub.ErrReader) {
		t.Errorf("Refresh = %v, want ErrReader", err)
	}
	if _, err := h.Probe(ctx, ""); !errors.Is(err, hub.ErrReader) {
		t.Errorf("Probe = %v, want ErrReader", err)
	}
	res, err := h.RefreshPass(ctx)
	if err != nil || res != (hub.RefreshResult{}) {
		t.Errorf("RefreshPass = %+v, %v; want the empty pass", res, err)
	}
	if outcome := h.RefreshWorkspace(ctx, "C0north"); outcome == hub.RefreshRan {
		t.Error("RefreshWorkspace ran a refresh")
	}
	h.Wait()
	if after := directoryCalls(directory); after != before {
		t.Errorf("the directory was called %d times, want 0", after-before)
	}
}

// The reader's Run drives nothing and ends with its context.
func TestAReaderHubRunWaitsForItsContext(t *testing.T) {
	t.Parallel()
	h := hub.New(hub.NewMemoryStore(), hub.NewMemorySnapshots(), hub.Config{}, slog.New(slog.DiscardHandler))
	h.UseReader()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}
