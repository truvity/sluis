package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/backend/fake"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/portstore"
)

const workspaceID = "C0123abcd"

// newApp assembles the module over the in-memory ports with one declared
// workspace, read through a fake directory in place of Google.
func newApp(t *testing.T, dir *fake.Backend) *App {
	t.Helper()
	t.Setenv("SLUIS_SECRET_DIRECTORY_EXAMPLE_KEY", `{"fake":true}`)
	doc := &config.SluisGoogle{
		APIVersion: config.GoogleAPIVersion,
		Instance:   "example",
		Secrets:    &config.Secrets{Source: "env"},
		Ports:      &config.Ports{Adapter: "memory"},
		Google: config.Google{Workspaces: []config.DirectoryWorkspace{{
			ID: workspaceID, Backend: "google", Admin: "admin@example.com", KeySecret: "directory/example/key",
		}}},
	}
	cfg, err := FromDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	cfg.openDeclared = func(_ context.Context, d *hub.Declared) (backend.Backend, error) {
		if string(d.Key) != `{"fake":true}` {
			t.Errorf("the key the module read = %q", d.Key)
		}
		return dir, nil
	}
	a, err := New(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	a.Hub().Wait() // the first snapshot an adoption starts
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func directory() *fake.Backend {
	return fake.New(workspaceID, "example.com").
		WithAccount("ada@example.com", "Ada", "Lovelace").
		WithGroup("staff@example.com", "ada@example.com")
}

// What the module refreshes, the issuer decodes: a hub over the same ports with
// no reader of its own answers from the snapshot and reads no directory.
func TestARefreshWritesASnapshotTheIssuerDecodes(t *testing.T) {
	dir := directory()
	a := newApp(t, dir)
	ctx := context.Background()

	// Adoption took the first snapshot; make the one under test unambiguous.
	if err := a.stores.Ports.Blob.Delete(ctx, hub.SnapshotBlobPrefix+workspaceID); err != nil {
		t.Fatal(err)
	}
	// The adoption's lease is still held (a successful pass keeps it): lift it.
	releaseLeases(t, a)
	before := dir.Calls(fake.OpAccounts)

	out, err := a.Pass(ctx, workspaceID, true)
	if err != nil || out != hub.RefreshRan {
		t.Fatalf("Pass = %v, %v; want %v", out, err, hub.RefreshRan)
	}
	if got := dir.Calls(fake.OpAccounts); got != before+1 {
		t.Fatalf("the directory was read %d times by the refresh, want 1", got-before)
	}

	// The issuer's side: the same ports, the snapshot store and the workspace
	// store, and nothing that could open a reader or refresh.
	issuer := hub.New(portstore.NewWorkspaces(portstore.New(a.stores.Ports).WithV4(a.stores.V4).WithV5(a.stores.V5)),
		hub.NewBlobSnapshots(a.stores.Ports.Blob, a.stores.Ports.State), hub.Config{}, slog.New(slog.DiscardHandler))
	// Even twenty minutes later, with the snapshot due, signing in reads it as it is.
	issuer.SetClock(func() time.Time { return time.Now().Add(20 * time.Minute) })
	calls := dir.Calls(fake.OpAccounts) + dir.Calls(fake.OpAccount) + dir.Calls(fake.OpGroupsOf)
	res, err := issuer.ResolveUser(ctx, "ada@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found || res.Suspended || !res.Authoritative || len(res.Groups) != 1 || res.Groups[0] != "staff@example.com" {
		t.Errorf("the issuer's answer from the module's snapshot = %+v", res)
	}
	issuer.Wait()
	if got := dir.Calls(fake.OpAccounts) + dir.Calls(fake.OpAccount) + dir.Calls(fake.OpGroupsOf); got != calls {
		t.Errorf("signing in read the directory %d times: the issuer only reads the snapshot (D-v176-6 a)", got-calls)
	}
}

// releaseLeases lifts the refresh lease of the workspace, which a pass keeps
// held for most of the interval.
func releaseLeases(t *testing.T, a *App) {
	t.Helper()
	if err := a.stores.Ports.State.Delete(context.Background(), hub.SnapshotLeasePrefix+"refresh:"+workspaceID); err != nil {
		t.Logf("no lease to lift: %v", err)
	}
}

// Two runners ask for the same workspace: the one that holds the lease reads,
// the other is told it is contended and reads nothing.
func TestARunnerHoldingTheLeaseKeepsOthersOff(t *testing.T) {
	dir := directory()
	a := newApp(t, dir)
	ctx := context.Background()
	releaseLeases(t, a)

	snaps := hub.NewBlobSnapshots(a.stores.Ports.Blob, a.stores.Ports.State)
	release, taken, err := snaps.Lock(ctx, "refresh:"+workspaceID, time.Minute)
	if err != nil || !taken {
		t.Fatalf("another runner could not take the lease: %v, %v", taken, err)
	}
	before := dir.Calls(fake.OpAccounts)
	out, err := a.Pass(ctx, workspaceID, true)
	if err != nil || out != hub.RefreshContended {
		t.Fatalf("Pass under another runner's lease = %v, %v; want %v", out, err, hub.RefreshContended)
	}
	if dir.Calls(fake.OpAccounts) != before {
		t.Error("a contended pass read the directory")
	}

	release(ctx)
	if out, err = a.Pass(ctx, workspaceID, true); err != nil || out != hub.RefreshRan {
		t.Fatalf("Pass after the lease was released = %v, %v; want %v", out, err, hub.RefreshRan)
	}
	// And a pass that ran keeps the next one off for most of the interval.
	if out, err = a.Pass(ctx, workspaceID, true); err != nil || out != hub.RefreshContended {
		t.Fatalf("a second pass in the same interval = %v, %v; want %v", out, err, hub.RefreshContended)
	}
	res, err := a.RefreshPass(ctx)
	if err != nil || res.Contended != 1 || res.Ran != 0 {
		t.Errorf("RefreshPass = %+v, %v; want one contended", res, err)
	}
}

// A failed refresh is an error and hands the lease straight back; the snapshot
// it had is untouched.
func TestAFailedRefreshKeepsTheSnapshotAndHandsTheLeaseBack(t *testing.T) {
	dir := directory()
	a := newApp(t, dir)
	ctx := context.Background()
	releaseLeases(t, a)
	if _, err := a.Pass(ctx, workspaceID, true); err != nil {
		t.Fatal(err)
	}
	releaseLeases(t, a)
	snaps := hub.NewBlobSnapshots(a.stores.Ports.Blob, a.stores.Ports.State)
	was, err := snaps.Get(ctx, workspaceID)
	if err != nil || was == nil {
		t.Fatalf("no snapshot to keep: %v, %v", was, err)
	}

	dir.Fail(fake.OpAccounts, errors.New("directory unavailable"))
	if out, err := a.Pass(ctx, workspaceID, true); err == nil || out != hub.RefreshFailed {
		t.Fatalf("Pass = %v, %v; want a failure", out, err)
	}
	now, err := snaps.Get(ctx, workspaceID)
	if err != nil || now == nil || !now.TakenAt.Equal(was.TakenAt) {
		t.Errorf("the snapshot changed on a failed refresh: %v -> %v (%v)", was.TakenAt, now, err)
	}
	dir.Heal(fake.OpAccounts)
	if out, err := a.Pass(ctx, workspaceID, true); err != nil || out != hub.RefreshRan {
		t.Errorf("Pass after the failure = %v, %v; want the lease handed back and %v", out, err, hub.RefreshRan)
	}
}

// Under maintenance the module writes nothing: a restore is writing the snapshots.
func TestARefreshStandsStillUnderMaintenance(t *testing.T) {
	dir := directory()
	a := newApp(t, dir)
	ctx := context.Background()
	releaseLeases(t, a)
	if err := a.stores.Ports.Blob.Delete(ctx, hub.SnapshotBlobPrefix+workspaceID); err != nil {
		t.Fatal(err)
	}

	state := a.stores.Ports.State
	if err := maintenance.Write(ctx, state, maintenance.Flag{State: maintenance.StateRestoring, Since: time.Now(), By: "test"}); err != nil {
		t.Fatal(err)
	}
	// A gate that trusts nothing it read, so the flag can be lifted below.
	a.hub.UseMaintenance(maintenance.New(state, maintenance.WithTTL(time.Nanosecond)))
	a.maint = maintenance.New(state, maintenance.WithTTL(time.Nanosecond))

	before := dir.Calls(fake.OpAccounts)
	out, err := a.Pass(ctx, workspaceID, true)
	if err != nil || out != hub.RefreshPaused || !a.Paused(ctx) {
		t.Fatalf("Pass under a restore = %v, %v (paused %v); want %v", out, err, a.Paused(ctx), hub.RefreshPaused)
	}
	if dir.Calls(fake.OpAccounts) != before {
		t.Error("a paused pass read the directory")
	}
	if snap, _ := hub.NewBlobSnapshots(a.stores.Ports.Blob, state).Get(ctx, workspaceID); snap != nil {
		t.Error("a paused pass wrote a snapshot")
	}
	res, err := a.RefreshPass(ctx)
	if err != nil || res.Paused != 1 || res.Ran != 0 {
		t.Errorf("RefreshPass under a restore = %+v, %v; want one paused", res, err)
	}

	if err = maintenance.Clear(ctx, state); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if out, err = a.Pass(ctx, workspaceID, true); err != nil || out != hub.RefreshRan {
		t.Errorf("Pass after the restore = %v, %v; want %v", out, err, hub.RefreshRan)
	}
}

func TestATickRefusesAnUnknownWorkspaceAndAnUnsharedLease(t *testing.T) {
	a := newApp(t, directory())
	ctx := context.Background()
	if err := a.Tick(ctx, "nobody", true); !errors.Is(err, ErrUnknownWorkspace) {
		t.Errorf("Tick of an unknown workspace = %v", err)
	}
	// The memory adapter's leases are this process's own: a tick refuses
	// unless the operator says the controller is stopped.
	if err := a.Tick(ctx, workspaceID, false); err == nil {
		t.Error("a tick over a lease held in this process only was not refused")
	}
	// The workspace is found by any spelling of its id.
	releaseLeases(t, a)
	if err := a.Tick(ctx, "c0123ABCD", true); err != nil {
		t.Errorf("Tick = %v", err)
	}
}
