package job_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/keys"
	"github.com/truvity/sluis/storage/keys/local"
	"github.com/truvity/sluis/storage/state"
	statemem "github.com/truvity/sluis/storage/state/memory"
)

var ctx = context.Background()

// clock is a settable clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func archiveKey(t *testing.T) *keys.Key {
	t.Helper()
	a := sha256.Sum256([]byte("job test root"))
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

// fanout reads every module's table, as the router does.
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

// failing is a blob that refuses writes once armed.
type failing struct {
	port.Blob
	fail bool
}

func (f *failing) Write(c context.Context, name string, body []byte) (string, error) {
	if f.fail {
		return "", errors.New("the bucket refused the write")
	}
	return f.Blob.Write(c, name, body)
}

type rig struct {
	t       *testing.T
	clk     *clock
	mods    *memory.Modules
	archive *failing
	audit   *audittest.Recorder
	key     *keys.Key
	n       int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	clk := &clock{t: time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)}
	r := &rig{t: t, clk: clk, mods: memory.NewModules(memory.WithClock(clk.now)),
		archive: &failing{Blob: memory.New().Blobs()}, audit: audittest.New(t), key: archiveKey(t)}
	for i := range 12 {
		if _, err := r.mods.Store(port.ModuleOIDC).Put(ctx, fmt.Sprintf("tok.j%02d", i), []byte(`{"v":1}`), time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.mods.Store(port.ModuleGitHub).Put(ctx, "gh.org.acme", []byte(`{"login":"acme"}`), 0); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) job(mutate ...func(*job.Config)) *job.Job {
	r.t.Helper()
	root := statemem.New()
	secrets := secretstore.FromStoreV5(root, "")
	doc, err := state.Raw().Marshal([]byte("a secret value"))
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := secrets.InternalStore(secretstore.Module(port.ModuleOIDC)).Put(ctx, "state-secret", doc, ""); err != nil {
		r.t.Fatal(err)
	}
	r.n++
	c := job.Config{
		Installation: "example", Creator: "sluis-backup test",
		State:   r.mods.Store(port.ModuleBackup),
		Source:  export.Source{State: fanout{r.mods}, Index: fanout{r.mods}, Secrets: secrets, Blob: memory.New().Blobs()},
		Archive: r.archive, Key: r.key, Keep: 2, MaxAge: 48 * time.Hour,
		Audit: r.audit, Now: r.clk.now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Holder: fmt.Sprintf("runner-%d", r.n),
		Suffix: func() string { return fmt.Sprintf("%06x", r.n*1000+int(r.clk.now().Unix()%1000)) },
	}
	for _, m := range mutate {
		m(&c)
	}
	j, err := job.New(c)
	if err != nil {
		r.t.Fatal(err)
	}
	return j
}

func (r *rig) run(j *job.Job) job.Result {
	r.t.Helper()
	res, err := j.Run(ctx, job.Request{Trigger: job.TriggerSchedule})
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

func TestARunWritesAVerifiableBackupAndItsRecords(t *testing.T) {
	r := newRig(t)
	j := r.job()
	res := r.run(j)
	if res.Outcome != job.OutcomeCompleted || res.Run == nil || res.Run.State != job.StateCompleted {
		t.Fatalf("result %+v", res)
	}
	if !strings.HasPrefix(res.ID, "20261010T020000Z-") || !backup.ValidName(res.ID) {
		t.Errorf("id %q is not a path-safe timestamped id", res.ID)
	}
	rd, err := backup.Open(ctx, r.archive, r.key, "example", res.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	if err := rd.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if rd.Manifest().Creator != "sluis-backup test" || rd.Manifest().Layout != backup.LayoutV5 {
		t.Errorf("manifest %+v", rd.Manifest())
	}
	if res.Run.Records < 14 || res.Run.Modules != 2 || res.Run.Chunks == 0 {
		t.Errorf("counts %+v", res.Run)
	}
	st, err := j.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Latest == nil || st.Latest.ID != res.ID || st.LastCompleted == nil || st.Unfinished != nil || st.Retention == nil {
		t.Errorf("status %+v", st)
	}
	if got := r.audit.Actions(); len(got) != 1 || got[0] != "roster.backup.completed" {
		t.Errorf("audit %v", got)
	}
	list, err := j.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != res.ID || list[0].Records != res.Run.Records {
		t.Errorf("list %+v, %v", list, err)
	}
	// The run's own records are not in the backup.
	if rd.Manifest().Part(port.ModuleBackup, backup.State).Records != 0 {
		t.Error("the backup holds the backup module's run records")
	}
}

func TestASecondRunWhileTheLeaseIsHeldIsRefused(t *testing.T) {
	r := newRig(t)
	if _, err := r.mods.Store(port.ModuleBackup).Create(ctx, rails.Key(job.LeaseKind, job.LeaseTarget), []byte("another"), time.Minute); err != nil {
		t.Fatal(err)
	}
	res := r.run(r.job())
	if res.Outcome != job.OutcomeContended || res.ID != "" {
		t.Errorf("result %+v", res)
	}
	if got := r.audit.Actions(); len(got) != 0 {
		t.Errorf("audit %v", got)
	}
	if names, _ := r.archive.List(ctx, "backup/"); len(names) != 0 {
		t.Errorf("a refused run wrote %v", names)
	}
}

func TestARunUnderMaintenanceDoesNothing(t *testing.T) {
	r := newRig(t)
	flag := `{"state":"restoring","since":"2026-10-10T01:00:00Z"}`
	if _, err := r.mods.Store(port.ModuleBackup).Put(ctx, maintenance.Key, []byte(flag), 0); err != nil {
		t.Fatal(err)
	}
	gate := maintenance.New(r.mods.Store(port.ModuleBackup))
	res := r.run(r.job(func(c *job.Config) { c.Maintenance = gate }))
	if res.Outcome != job.OutcomeMaintenance {
		t.Errorf("result %+v", res)
	}
}

func TestARunThatRunsOutOfTimePausesAndTheNextOneContinuesIt(t *testing.T) {
	r := newRig(t)
	j := r.job(func(c *job.Config) { c.ChunkBytes = 64 })
	short, cancel := context.WithTimeout(ctx, time.Minute) // less than the margin
	defer cancel()
	res, err := j.Run(short, job.Request{Trigger: job.TriggerSchedule})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != job.OutcomePaused || res.Run.State != job.StatePaused || res.Run.Units == 0 || res.Run.Done == 0 || res.Run.Done >= res.Run.Units {
		t.Fatalf("result %+v", res.Run)
	}
	r.clk.advance(time.Hour)
	// A resume-only run (the cheap schedule) finishes it, in as many passes as
	// the short deadline allows: here none, there is no deadline.
	again, err := j.Run(ctx, job.Request{Trigger: job.TriggerSchedule, ResumeOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Outcome != job.OutcomeCompleted || again.ID != res.ID || !again.Run.Resumed {
		t.Fatalf("resumed %+v", again)
	}
	rd, err := backup.Open(ctx, r.archive, r.key, "example", res.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	if err := rd.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	// The same installation in one pass gives the same counts.
	whole := r.run(r.job(func(c *job.Config) { c.ChunkBytes = 64 }))
	if whole.Run.Records != again.Run.Records || whole.Run.Modules != again.Run.Modules {
		t.Errorf("resumed %d records, whole %d", again.Run.Records, whole.Run.Records)
	}
	if got := r.audit.Actions(); got[0] != "roster.backup.completed" || len(got) < 2 {
		t.Errorf("audit %v", got)
	}
}

func TestAResumeOnlyRunWithNothingToContinueIsIdle(t *testing.T) {
	r := newRig(t)
	res, err := r.job().Run(ctx, job.Request{ResumeOnly: true})
	if err != nil || res.Outcome != job.OutcomeIdle {
		t.Errorf("%+v, %v", res, err)
	}
	if names, _ := r.archive.List(ctx, "backup/"); len(names) != 0 {
		t.Errorf("an idle run wrote %v", names)
	}
}

func TestAFailedRunIsRecordedAndTheNextOneStartsAgain(t *testing.T) {
	r := newRig(t)
	j := r.job(func(c *job.Config) { c.ChunkBytes = 64 })
	r.archive.fail = true
	res, err := j.Run(ctx, job.Request{Trigger: job.TriggerSchedule})
	if err == nil || res.Outcome != job.StateFailed {
		t.Fatalf("%+v, %v", res, err)
	}
	st, _ := j.Status(ctx)
	if st.Latest == nil || st.Latest.State != job.StateFailed || st.Latest.Reason != job.ReasonExport || st.Latest.Error == "" {
		t.Errorf("status %+v", st.Latest)
	}
	if got := r.audit.Actions(); len(got) != 1 || got[0] != "roster.backup.failed" {
		t.Errorf("audit %v", got)
	}
	r.archive.fail = false
	r.clk.advance(time.Hour)
	ok := r.run(j)
	if ok.Outcome != job.OutcomeCompleted || ok.ID == res.ID {
		t.Errorf("next %+v", ok)
	}
}

func TestAnInterruptedRunWithoutACheckpointIsClosedAsAbandoned(t *testing.T) {
	r := newRig(t)
	j := r.job()
	stale := `{"id":"20261009T020000Z-aaaaaa","state":"running","trigger":"schedule","creator":"x","started":"2026-10-09T02:00:00Z","updated":"2026-10-09T02:00:00Z"}`
	if _, err := r.mods.Store(port.ModuleBackup).Put(ctx, job.RunPrefix+"20261009T020000Z-aaaaaa", []byte(stale), 0); err != nil {
		t.Fatal(err)
	}
	res := r.run(j)
	if res.Outcome != job.OutcomeCompleted {
		t.Fatalf("%+v", res)
	}
	runs, _ := j.Runs(ctx, 0)
	if len(runs) != 2 || runs[1].State != job.StateFailed || runs[1].Reason != job.ReasonAbandoned {
		t.Errorf("runs %+v", runs)
	}
}

func TestPruneKeepsTheNewestAndTheYoungAndDeletesTheManifestFirst(t *testing.T) {
	r := newRig(t)
	j := r.job()
	var ids []string
	for range 4 {
		ids = append(ids, r.run(j).ID)
		r.clk.advance(30 * time.Hour)
	}
	// 4 backups, 30 h apart; keep 2, max age 48 h: the two oldest are beyond
	// keep, and only those older than 48 h go. The clock is now 30 h after the last.
	list, _ := j.List(ctx)
	if len(list) != 2 {
		// Each completed run already pruned.
		t.Logf("after the runs: %d", len(list))
	}
	pass, outcome, err := j.Prune(ctx, audit.System(), true)
	if err != nil || outcome != "pruned" || !pass.DryRun {
		t.Fatalf("%+v %q %v", pass, outcome, err)
	}
	before, _ := j.List(ctx)
	pass, _, err = j.Prune(ctx, audit.System(), false)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := j.List(ctx)
	if len(after) != 2 || after[0].ID != ids[3] || after[1].ID != ids[2] {
		t.Errorf("kept %+v (before %d), want %s and %s", after, len(before), ids[3], ids[2])
	}
	for _, id := range ids[:2] {
		if names, _ := r.archive.List(ctx, backup.Prefix("example", id)); len(names) != 0 {
			t.Errorf("%s still has %v", id, names)
		}
	}
	if pass.Kept != 2 {
		t.Errorf("pass %+v", pass)
	}
	st, _ := j.Status(ctx)
	if st.Retention == nil || st.Retention.Keep != 2 {
		t.Errorf("retention record %+v", st.Retention)
	}
}

func TestPruneRemovesTheChunksOfARunThatNeverWroteAManifest(t *testing.T) {
	r := newRig(t)
	j := r.job()
	old := backup.ChunkName("example", "20260901T020000Z-dead01", backup.State, port.ModuleOIDC, 0)
	young := backup.ChunkName("example", "20261009T020000Z-dead02", backup.State, port.ModuleOIDC, 0)
	foreign := "backup/example/not-ours/state/oidc/0"
	for _, n := range []string{old, young, foreign} {
		if _, err := r.archive.Write(ctx, n, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	pass, _, err := j.Prune(ctx, audit.System(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Orphans) != 1 || pass.Orphans[0] != "20260901T020000Z-dead01" {
		t.Errorf("orphans %v", pass.Orphans)
	}
	left, _ := r.archive.List(ctx, "backup/")
	if len(left) != 2 {
		t.Errorf("left %v", left)
	}
	if got := r.audit.Actions(); len(got) != 1 || got[0] != "roster.backup.pruned" {
		t.Errorf("audit %v", got)
	}
}

func TestThePruneNeverDeletesTheRunItsRecordSaysIsOpen(t *testing.T) {
	r := newRig(t)
	j := r.job(func(c *job.Config) { c.ChunkBytes = 64 })
	short, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := j.Run(short, job.Request{})
	if err != nil || res.Outcome != job.OutcomePaused {
		t.Fatalf("%+v %v", res, err)
	}
	// A unit that had no record writes no chunk, so plant one the run "wrote".
	chunk := backup.ChunkName("example", res.ID, backup.State, port.ModuleOIDC, 0)
	if _, err := r.archive.Write(ctx, chunk, []byte("x")); err != nil {
		t.Fatal(err)
	}
	r.clk.advance(10 * 24 * time.Hour) // far past the orphan age
	if _, _, err := j.Prune(ctx, audit.System(), false); err != nil {
		t.Fatal(err)
	}
	if names, _ := r.archive.List(ctx, backup.Prefix("example", res.ID)); len(names) == 0 {
		t.Error("the prune deleted the chunks of a paused run")
	}
}

func TestTheConfigIsChecked(t *testing.T) {
	for name, c := range map[string]job.Config{
		"no installation": {},
		"bad name":        {Installation: "a/b"},
	} {
		if _, err := job.New(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
