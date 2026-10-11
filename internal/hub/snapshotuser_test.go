package hub_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/truvity/sluis/backend/fake"
)

// SnapshotUser answers from the stored snapshot and never reaches the
// directory: not for a stale snapshot, not for an address the snapshot does not
// hold, and not to patch anything.
func TestSnapshotUserNeverReadsTheDirectory(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.hub.Refresh(ctx, oneID); err != nil {
		t.Fatal(err)
	}
	// From here any directory call fails, and the snapshot ages past anything
	// the hub would serve: ResolveUser would go live on both counts.
	boom := errors.New("the directory was asked")
	for _, op := range []fake.Op{fake.OpTenant, fake.OpProbe, fake.OpAccounts, fake.OpGroups, fake.OpAccount, fake.OpGroupsOf} {
		h.one.Fail(op, boom)
	}
	before := map[fake.Op]int{}
	for _, op := range []fake.Op{fake.OpAccounts, fake.OpGroups, fake.OpAccount, fake.OpGroupsOf} {
		before[op] = h.one.Calls(op)
	}
	h.advance(48 * time.Hour)

	got, err := h.hub.SnapshotUser(ctx, "alice@one.example")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Suspended || !slices.Contains(got.Groups, "platform@one.example") {
		t.Errorf("alice = %+v", got)
	}
	if got.Authoritative {
		t.Error("a snapshot two days old was reported as authoritative")
	}
	missing, err := h.hub.SnapshotUser(ctx, "new.hire@one.example")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Found || missing.Authoritative {
		t.Errorf("an address the snapshot lacks = %+v: absence must not read as a removal", missing)
	}
	elsewhere, err := h.hub.SnapshotUser(ctx, "x@nobody.example")
	if err != nil || elsewhere.InDomain {
		t.Errorf("an unserved domain = %+v, %v", elsewhere, err)
	}
	for op, n := range before {
		if h.one.Calls(op) != n {
			t.Errorf("%s was called: SnapshotUser reached the directory", op)
		}
	}
}

func TestSnapshotUserReportsASuspendedAccount(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.one.Suspend("bob@one.example")
	if _, err := h.hub.Refresh(context.Background(), oneID); err != nil {
		t.Fatal(err)
	}
	got, err := h.hub.SnapshotUser(context.Background(), "bob@one.example")
	if err != nil || !got.Found || !got.Suspended || !got.Authoritative {
		t.Errorf("bob = %+v, %v", got, err)
	}
}
