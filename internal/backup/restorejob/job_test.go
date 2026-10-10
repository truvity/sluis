package restorejob_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/keys"
	"github.com/truvity/sluis/storage/keys/local"
	statemem "github.com/truvity/sluis/storage/state/memory"
)

var ctx = context.Background()

var epoch = time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)

func archiveKey(t *testing.T) *keys.Key {
	t.Helper()
	a := sha256.Sum256([]byte("restorejob test root"))
	b := sha256.Sum256(a[:])
	be, err := local.New(append(a[:], b[:]...))
	if err != nil {
		t.Fatal(err)
	}
	set, err := keys.Open(keys.Config{Adapter: "local", Keys: map[keys.Purpose]keys.Entry{keys.Archive: {Key: "backup"}}},
		keys.Options{Backend: be, Instance: "example"})
	if err != nil {
		t.Fatal(err)
	}
	k, err := set.For(keys.Archive)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type fanout struct{ mods *memory.Modules }

func (f fanout) ExportState(c context.Context, prefix string, fn func(port.Exported) error) error {
	stores := port.Modules()
	if m, ok := port.LocateModule5(prefix); ok {
		stores = []port.Module{m}
	}
	for _, m := range stores {
		if err := f.mods.Store(m).ExportState(c, prefix, fn); err != nil {
			return err
		}
	}
	return nil
}

func (f fanout) ExportIndex(c context.Context, prefix string, fn func(port.Exported) error) error {
	return f.mods.Store(port.ModuleOIDC).ExportIndex(c, prefix, fn)
}

// hooked counts the writes to the destination's tables and calls back before
// each, so that a test can look at the world at the moment of a write.
type hooked struct {
	restore.Table
	mod  port.Module
	rig  *rig
	puts *[]string
}

func (h hooked) Put(c context.Context, key string, v []byte, ttl time.Duration) (port.Revision, error) {
	if err := h.rig.beforeWrite(h.mod, key); err != nil {
		return "", err
	}
	if h.rig.dropKey == key {
		return "", nil // the write is lost: the read-back will differ
	}
	return h.Table.Put(c, key, v, ttl)
}

type rig struct {
	t       *testing.T
	src     *memory.Modules
	dst     *memory.Modules
	secrets *secretstore.StoresV5
	blob    *memory.Blobs
	archive port.Blob
	key     *keys.Key
	audit   *audittest.Recorder

	mu         sync.Mutex
	writes     []string
	slept      []time.Duration
	flagsAtput map[string]int // writes -> modules under maintenance at that moment
	cancelAt   int            // cancel the context at the n-th write
	cancel     context.CancelFunc
	dropKey    string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, src: memory.NewModules(memory.WithClock(func() time.Time { return epoch })),
		dst:   memory.NewModules(memory.WithClock(func() time.Time { return epoch })),
		blob:  memory.New().Blobs(),
		audit: audittest.New(t), key: archiveKey(t), archive: memory.New().Blobs(), flagsAtput: map[string]int{}}
	r.secrets = secretstore.FromStoreV5(statemem.New(), "")
	put := func(m port.Module, key string, ttl time.Duration) {
		if _, err := r.src.Store(m).Put(ctx, key, []byte(fmt.Sprintf(`{"id":%q}`, key)), ttl); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 12 {
		put(port.ModuleOIDC, fmt.Sprintf("tok.j%02d", i), time.Hour)
	}
	put(port.ModuleGitHub, "gh.org.acme", 0)
	put(port.ModuleSlack, "ws.slack.T01", 0)
	// A backup is written from the source.
	for _, id := range []string{"b1", "b2"} {
		w, err := backup.NewWriter(ctx, r.archive, r.key, backup.Params{Installation: "example", ID: id, Layout: backup.LayoutV5,
			Creator: "test", Now: func() time.Time { return epoch }, ChunkBytes: 4096})
		if err != nil {
			t.Fatal(err)
		}
		f := fanout{r.src}
		src := export.Source{State: f, Index: f, Secrets: secretstore.FromStoreV5(statemem.New(), ""), Blob: memory.New().Blobs()}
		if _, err := export.Run(ctx, w, src, export.Options{Now: func() time.Time { return epoch }}); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// beforeWrite notes how many modules were under maintenance at a write.
func (r *rig) beforeWrite(m port.Module, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, mod := range port.Modules() {
		if _, err := r.dst.Store(mod).Get(ctx, maintenance.Key); err == nil {
			n++
		}
	}
	// The invocation's time runs out at the n-th write, which does not happen.
	if r.cancelAt > 0 && len(r.writes)+1 == r.cancelAt && r.cancel != nil {
		r.cancelAt = 0
		r.cancel()
		return context.Canceled
	}
	r.flagsAtput[fmt.Sprintf("%s %s", m, key)] = n
	r.writes = append(r.writes, string(m)+" "+key)
	return nil
}

func (r *rig) job(mutate ...func(*restorejob.Config)) *restorejob.Job {
	r.t.Helper()
	c := restorejob.Config{
		Installation: "example", State: r.dst.Store(port.ModuleBackup),
		Modules: port.Modules(), Flags: func(m port.Module) port.State { return r.dst.Store(m) },
		Target: restore.Target{
			Table:   func(m port.Module) restore.Table { return hooked{r.dst.Store(m), m, r, nil} },
			Secrets: r.secrets, Blob: r.blob,
		},
		Archive: r.archive, Key: r.key, Audit: r.audit, Now: func() time.Time { return epoch }, Holder: "h1",
		Sleep:  func(_ context.Context, d time.Duration) error { r.slept = append(r.slept, d); return nil },
		Suffix: func() string { return "aaaaaa" },
	}
	for _, f := range mutate {
		f(&c)
	}
	j, err := restorejob.New(c)
	if err != nil {
		r.t.Fatal(err)
	}
	return j
}

func (r *rig) flagged() []port.Module {
	var out []port.Module
	for _, m := range port.Modules() {
		if _, err := r.dst.Store(m).Get(ctx, maintenance.Key); err == nil {
			out = append(out, m)
		}
	}
	return out
}

var admin = audit.Workload("lambda:admin")

func TestMaintenanceIsSetBeforeTheFirstWriteAndClearedAfterACleanFinish(t *testing.T) {
	r := newRig(t)
	res, err := r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin, By: "ada", Note: "disaster drill"})
	if err != nil || res.Outcome != restorejob.OutcomeCompleted {
		t.Fatalf("%+v, %v", res, err)
	}
	if len(r.writes) == 0 {
		t.Fatal("nothing was restored")
	}
	for k, n := range r.flagsAtput {
		if n != len(port.Modules()) {
			t.Errorf("at the write of %s only %d of %d modules were under maintenance", k, n, len(port.Modules()))
		}
	}
	// The wait for the modules' cache came before the first write.
	if len(r.slept) != 1 || r.slept[0] < maintenance.DefaultTTL {
		t.Errorf("waited %v, want at least %v once", r.slept, maintenance.DefaultTTL)
	}
	if f := r.flagged(); len(f) != 0 {
		t.Errorf("maintenance is still set on %v", f)
	}
	if res.Run.Maintenance != restorejob.MaintenanceCleared || res.Run.State != restorejob.StateCompleted || res.Run.By != "ada" {
		t.Errorf("run %+v", res.Run)
	}
	if got := r.audit.Actions(); !slices.Equal(got, []string{"roster.restore.started", "roster.restore.completed"}) {
		t.Errorf("audit %v", got)
	}
	if _, err := r.dst.Store(port.ModuleGitHub).Get(ctx, "gh.org.acme"); err != nil {
		t.Errorf("gh.org.acme was not restored: %v", err)
	}
	st, err := r.job().Status(ctx)
	if err != nil || st.LastCompleted == nil || st.Unfinished != nil || len(st.Maintenance) != 0 {
		t.Errorf("status %+v, %v", st, err)
	}
}

func TestOnlyOneRestoreRunsAtATime(t *testing.T) {
	r := newRig(t)
	other := &rails.Leases{State: r.dst.Store(port.ModuleBackup), Holder: "someone-else"}
	lease, err := other.Acquire(ctx, restorejob.LeaseKind, restorejob.LeaseTarget)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
	if err != nil || res.Outcome != restorejob.OutcomeContended {
		t.Fatalf("%+v, %v", res, err)
	}
	if len(r.flagged()) != 0 || len(r.writes) != 0 || len(r.audit.Records()) != 0 {
		t.Error("a refused start changed something")
	}
	if st, _ := r.job().Status(ctx); st.Latest != nil {
		t.Errorf("a refused start left a record: %+v", st.Latest)
	}
	if err := lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if res, err = r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin}); err != nil || res.Outcome != restorejob.OutcomeCompleted {
		t.Fatalf("after the release: %+v, %v", res, err)
	}
	// The lease is released when the restore returns.
	if _, err := other.Acquire(ctx, restorejob.LeaseKind, restorejob.LeaseTarget); err != nil {
		t.Errorf("the lease is still held: %v", err)
	}
}

func TestAnUnknownBackupStartsNothing(t *testing.T) {
	r := newRig(t)
	res, err := r.job().Start(ctx, restorejob.Request{BackupID: "nope", Actor: admin})
	if err != nil || res.Outcome != restorejob.OutcomeRefused || res.Reason != restorejob.ReasonBackup {
		t.Fatalf("%+v, %v", res, err)
	}
	if len(r.flagged()) != 0 || len(r.audit.Records()) != 0 {
		t.Error("a refused start set maintenance or recorded")
	}
	if res, _ = r.job().Start(ctx, restorejob.Request{Actor: admin}); res.Outcome != restorejob.OutcomeIdle {
		t.Errorf("a resume with nothing to continue: %+v", res)
	}
}

func TestMaintenanceStaysSetOnEveryFailure(t *testing.T) {
	t.Run("not empty", func(t *testing.T) {
		r := newRig(t)
		if _, err := r.dst.Store(port.ModuleGitHub).Put(ctx, "gh.org.acme", []byte(`{"id":"different"}`), 0); err != nil {
			t.Fatal(err)
		}
		res, err := r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
		check(t, r, res, err, restorejob.ReasonNotEmpty)
		if len(r.writes) != 0 {
			t.Errorf("a refused restore wrote %v", r.writes)
		}
	})
	t.Run("verify", func(t *testing.T) {
		r := newRig(t)
		r.dropKey = "gh.org.acme"
		res, err := r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
		check(t, r, res, err, restorejob.ReasonVerify)
	})
	t.Run("archive", func(t *testing.T) {
		r := newRig(t)
		w, err := backup.NewWriter(ctx, r.archive, r.key, backup.Params{Installation: "example", ID: "bad", Layout: backup.LayoutV5,
			Creator: "t", Now: func() time.Time { return epoch }, ChunkBytes: 4096})
		if err != nil {
			t.Fatal(err)
		}
		rec, _ := json.Marshal(export.State{V: export.RecordVersion, T: export.TypeState, Key: "gh.org.acme", Value: []byte(`{}`)})
		if err := w.Add(ctx, backup.State, port.ModuleSlack, rec); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Close(ctx); err != nil {
			t.Fatal(err)
		}
		res, err := r.job().Start(ctx, restorejob.Request{BackupID: "bad", Actor: admin})
		check(t, r, res, err, restorejob.ReasonArchive)
	})
	t.Run("a flag that cannot be set", func(t *testing.T) {
		r := newRig(t)
		res, err := r.job(func(c *restorejob.Config) {
			c.Flags = func(m port.Module) port.State {
				if m == port.ModuleSlack {
					return refusing{r.dst.Store(m)}
				}
				return r.dst.Store(m)
			}
		}).Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
		if err != nil || res.Outcome != restorejob.OutcomeFailed || res.Run.Reason != restorejob.ReasonMaintenance {
			t.Fatalf("%+v, %v", res, err)
		}
		if len(r.writes) != 0 {
			t.Errorf("a restore wrote with a module still open: %v", r.writes)
		}
	})
}

type refusing struct{ port.State }

func (refusing) Put(context.Context, string, []byte, time.Duration) (port.Revision, error) {
	return "", errors.New("access denied")
}

// check holds the shape every failure has: the run says why, the audit trail
// has the failure, and every module is still under maintenance.
func check(t *testing.T, r *rig, res restorejob.Result, err error, reason string) {
	t.Helper()
	if err != nil || res.Outcome != restorejob.OutcomeFailed || res.Run == nil || res.Run.Reason != reason {
		t.Fatalf("%+v, %v; want a failure with reason %s", res, err, reason)
	}
	if res.Run.State != restorejob.StateFailed || res.Run.Error == "" {
		t.Errorf("run %+v", res.Run)
	}
	if reason != restorejob.ReasonMaintenance && len(r.flagged()) != len(port.Modules()) {
		t.Errorf("maintenance is set on %v, want every module", r.flagged())
	}
	if len(r.flagged()) == 0 {
		t.Error("maintenance was lifted by a failure")
	}
	if res.Run.Maintenance != restorejob.MaintenanceOn {
		t.Errorf("the run says maintenance is %q", res.Run.Maintenance)
	}
	if got := r.audit.Actions(); len(got) != 2 || got[1] != "roster.restore.failed" {
		t.Errorf("audit %v", got)
	}
	st, serr := r.job().Status(ctx)
	if serr != nil || st.Latest == nil || st.Latest.State != restorejob.StateFailed || len(st.Maintenance) == 0 {
		t.Errorf("status %+v, %v", st, serr)
	}
}

func TestACancelledSliceIsContinuedByTheNextCall(t *testing.T) {
	r := newRig(t)
	cctx, cancel := context.WithCancel(ctx)
	r.cancel, r.cancelAt = cancel, 5
	res, err := r.job().Start(cctx, restorejob.Request{BackupID: "b1", Actor: admin})
	if err != nil || res.Outcome != restorejob.OutcomePaused || res.Run.State != restorejob.StatePaused {
		t.Fatalf("%+v, %v", res, err)
	}
	if len(r.flagged()) != len(port.Modules()) {
		t.Errorf("a paused restore lifted maintenance: %v", r.flagged())
	}
	done := len(r.writes)
	if done != 4 {
		t.Fatalf("%d writes before the pause", done)
	}
	// A call with no backup id continues it. A different id is not needed and
	// overwrite is the run's own.
	res, err = r.job().Start(ctx, restorejob.Request{Actor: admin})
	if err != nil || res.Outcome != restorejob.OutcomeCompleted || res.ID != "20261010T030000Z-aaaaaa" || res.Run.Slices != 2 {
		t.Fatalf("%+v, %v", res, err)
	}
	if len(r.flagged()) != 0 {
		t.Errorf("maintenance is still set on %v", r.flagged())
	}
	seen := map[string]bool{}
	for _, w := range r.writes {
		if seen[w] {
			t.Errorf("%q written twice", w)
		}
		seen[w] = true
	}
	// One start, one completion: a pause is not a new restore.
	if got := r.audit.Actions(); !slices.Equal(got, []string{"roster.restore.started", "roster.restore.completed"}) {
		t.Errorf("audit %v", got)
	}
}

func TestARunOutOfTimeIsPausedWithoutAWrite(t *testing.T) {
	r := newRig(t)
	dctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	res, err := r.job(func(c *restorejob.Config) { c.Margin = time.Hour }).Start(dctx, restorejob.Request{BackupID: "b1", Actor: admin})
	if err != nil || res.Outcome != restorejob.OutcomePaused || res.Run.Reason != "time" {
		t.Fatalf("%+v, %v", res, err)
	}
	if len(r.writes) != 0 || len(r.flagged()) != len(port.Modules()) {
		t.Errorf("writes %v, flagged %v", r.writes, r.flagged())
	}
}

func TestOverwriteHappensOnlyWhenAsked(t *testing.T) {
	r := newRig(t)
	if _, err := r.dst.Store(port.ModuleGitHub).Put(ctx, "gh.org.acme", []byte(`{"id":"different"}`), 0); err != nil {
		t.Fatal(err)
	}
	res, _ := r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
	if res.Outcome != restorejob.OutcomeFailed {
		t.Fatalf("%+v", res)
	}
	rec, _ := r.dst.Store(port.ModuleGitHub).Get(ctx, "gh.org.acme")
	if string(rec.Value) != `{"id":"different"}` {
		t.Fatalf("the destination was overwritten without being asked: %s", rec.Value)
	}
	// Asking again with overwrite starts a new restore, which completes.
	res, err := r.job(func(c *restorejob.Config) { c.Suffix = func() string { return "bbbbbb" } }).
		Start(ctx, restorejob.Request{BackupID: "b1", Overwrite: true, Actor: admin})
	if err != nil || res.Outcome != restorejob.OutcomeCompleted || !res.Run.Overwrite || res.Run.Overwritten == 0 {
		t.Fatalf("%+v, %v", res, err)
	}
	rec, _ = r.dst.Store(port.ModuleGitHub).Get(ctx, "gh.org.acme")
	if string(rec.Value) != `{"id":"gh.org.acme"}` {
		t.Errorf("the overwrite did not happen: %s", rec.Value)
	}
	if len(r.flagged()) != 0 {
		t.Errorf("maintenance is still set on %v", r.flagged())
	}
}

func TestAnotherBackupSupersedesAPausedRestore(t *testing.T) {
	r := newRig(t)
	cctx, cancel := context.WithCancel(ctx)
	r.cancel, r.cancelAt = cancel, 3
	if res, _ := r.job().Start(cctx, restorejob.Request{BackupID: "b1", Actor: admin}); res.Outcome != restorejob.OutcomePaused {
		t.Fatalf("%+v", res)
	}
	res, err := r.job(func(c *restorejob.Config) { c.Suffix = func() string { return "cccccc" } }).
		Start(ctx, restorejob.Request{BackupID: "b2", Actor: admin})
	if err != nil || res.Outcome != restorejob.OutcomeCompleted || res.Run.BackupID != "b2" {
		t.Fatalf("%+v, %v", res, err)
	}
	st, _ := r.job().Status(ctx)
	if st.Unfinished != nil || st.LastCompleted == nil {
		t.Errorf("status %+v", st)
	}
	if got := r.audit.Find("roster.restore.failed"); len(got) != 1 {
		t.Errorf("the superseded restore was not recorded as failed: %v", r.audit.Actions())
	}
}

func TestPreviewWritesNothing(t *testing.T) {
	r := newRig(t)
	rep, err := r.job().Preview(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if tot := rep.Totals(); tot.Create == 0 {
		t.Errorf("a preview of an empty destination should create: %+v", tot)
	}
	if len(r.writes) != 0 || len(r.flagged()) != 0 || len(r.audit.Records()) != 0 || len(r.slept) != 0 {
		t.Errorf("a preview changed something: writes %v flagged %v", r.writes, r.flagged())
	}
	if st, _ := r.job().Status(ctx); st.Latest != nil {
		t.Errorf("a preview left a record: %+v", st.Latest)
	}
	if _, err := r.dst.Store(port.ModuleBackup).Get(ctx, "lease.restore:run"); err == nil {
		t.Error("a preview took the lease")
	}
}
