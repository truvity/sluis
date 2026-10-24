package job_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/attest"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	statemem "github.com/truvity/sluis/storage/state/memory"
)

var ctx = context.Background()

// clock is a settable clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// recipient is the identity every test backup is encrypted to.
var recipient = func() *age.X25519Identity {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		panic(err)
	}
	return id
}()

func identities(context.Context) ([]age.Identity, error) { return []age.Identity{recipient}, nil }

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

// failing is a blob that refuses writes (of names with the suffix, when one
// is set) once armed, refuses deletes of names with lock, and records the
// order of its deletes.
type failing struct {
	port.Blob
	fail    bool
	suffix  string
	lock    string
	deletes []string
}

func (f *failing) Write(c context.Context, name string, body []byte) (string, error) {
	if f.fail && strings.HasSuffix(name, f.suffix) {
		return "", errors.New("the bucket refused the write")
	}
	return f.Blob.Write(c, name, body)
}

func (f *failing) Delete(c context.Context, name string) error {
	if f.lock != "" && strings.Contains(name, f.lock) {
		return errors.New("AccessDenied: object is WORM protected")
	}
	f.deletes = append(f.deletes, name)
	return f.Blob.Delete(c, name)
}

type rig struct {
	t       *testing.T
	clk     *clock
	mods    *memory.Modules
	archive *failing
	audit   *audittest.Recorder
	n       int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	clk := &clock{t: time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)}
	r := &rig{t: t, clk: clk, mods: memory.NewModules(memory.WithClock(clk.now)),
		archive: &failing{Blob: memory.New().Blobs()}, audit: audittest.New(t)}
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
	// The restore identity is the backup module's own secret, never exported.
	if _, err := secrets.Backup().RestoreIdentity("").Put(ctx, []byte(recipient.String()), ""); err != nil {
		r.t.Fatal(err)
	}
	r.n++
	c := job.Config{
		Installation: "example", Creator: "sluis-backup test",
		State:   r.mods.Store(port.ModuleBackup),
		Source:  export.Source{State: fanout{r.mods}, Index: fanout{r.mods}, Secrets: secrets, Blob: memory.New().Blobs()},
		Archive: r.archive, Recipients: []string{recipient.Recipient().String()}, TempDir: r.t.TempDir(),
		Keep: 2, MaxAge: 48 * time.Hour,
		Audit: r.audit, Now: r.clk.now, Log: slog.New(slog.DiscardHandler), Holder: fmt.Sprintf("runner-%d", r.n),
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
	rd, err := backup.Open(ctx, r.archive, identities, "example", res.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	if names, _ := r.archive.List(ctx, "backup/"); len(names) != 2 ||
		names[0] != backup.SidecarName("example", res.ID) || names[1] != backup.ObjectName("example", res.ID) {
		t.Errorf("objects %v", names)
	}
	if rd.Manifest().Creator != "sluis-backup test" || rd.Manifest().Layout != backup.LayoutV5 {
		t.Errorf("manifest %+v", rd.Manifest())
	}
	if res.Run.Records < 14 || res.Run.Modules != 6 || res.Run.Chunks != 1 || res.Run.Size == 0 {
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
	if err != nil || len(list) != 1 || list[0].ID != res.ID || list[0].Records != res.Run.Records ||
		list[0].Size != res.Run.Size {
		t.Errorf("list %+v, %v", list, err)
	}
	// The run's own records and the restore identity are not in the backup.
	if rd.Manifest().Module(port.ModuleBackup) != nil {
		t.Error("the backup holds the backup module's records or secrets")
	}
}

// The record of a completed backup carries what a restore holds the archive's
// copy to: the verifier accepts the backup the record describes, and nothing
// else.
func TestTheRecordOfACompletedBackupIsWhatARestoreVerifiesItAgainst(t *testing.T) {
	r := newRig(t)
	res := r.run(r.job())
	recs := r.audit.Find("roster.backup.completed")
	if len(recs) != 1 {
		t.Fatalf("audit %v", r.audit.Actions())
	}
	claim := attest.ClaimOf(recs[0])
	claim.Sealed, claim.SealKey = true, "seals/x.jws"
	f, err := backup.FetchAll(ctx, r.archive, "example", res.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"object_key", "object_size", "object_sha256", "sidecar_sha256", "recipients"} {
		if _, ok := recs[0].GetData().GetFields()[field]; !ok {
			t.Errorf("the record lacks %s: %v", field, recs[0].GetData())
		}
	}
	v := backup.Verifier{Recipients: []string{recipient.Recipient().String()},
		Attestor: backup.AttestorFunc(func(context.Context, string, string) ([]backup.Claim, error) { return []backup.Claim{claim}, nil })}
	if ver, err := v.Check(ctx, f); err != nil || ver.Unverified {
		t.Fatalf("%+v, %v", ver, err)
	}
	// The same record does not vouch for a sidecar changed by a byte.
	f.SidecarSHA256 = strings.Repeat("0", 64)
	if _, err := v.Check(ctx, f); !errors.Is(err, backup.ErrMismatch) {
		t.Errorf("a changed sidecar: %v", err)
	}
	// The recipients are named by their hash, not in the clear.
	for _, fp := range recs[0].GetData().GetFields()["recipients"].GetListValue().GetValues() {
		if strings.HasPrefix(fp.GetStringValue(), "age1") || fp.GetStringValue() != backup.Fingerprint(recipient.Recipient().String()) {
			t.Errorf("recipient %q", fp.GetStringValue())
		}
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

// The resume schedule of an earlier release still fires: it is idle and
// touches nothing, not even a run left running.
func TestAResumeOnlyRunIsIdle(t *testing.T) {
	r := newRig(t)
	stale := `{"id":"20261009T020000Z-aaaaaa","state":"paused","trigger":"schedule","creator":"x",` +
		`"started":"2026-10-09T02:00:00Z","updated":"2026-10-09T02:00:00Z"}`
	if _, err := r.mods.Store(port.ModuleBackup).Put(ctx, job.RunPrefix+"20261009T020000Z-aaaaaa", []byte(stale), 0); err != nil {
		t.Fatal(err)
	}
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
	j := r.job()
	r.archive.fail = true
	res, err := j.Run(ctx, job.Request{Trigger: job.TriggerSchedule})
	if err == nil || res.Outcome != job.StateFailed {
		t.Fatalf("%+v, %v", res, err)
	}
	st, _ := j.Status(ctx)
	if st.Latest == nil || st.Latest.State != job.StateFailed || st.Latest.Reason != job.ReasonUpload || st.Latest.Error == "" {
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
	stale := `{"id":"20261009T020000Z-aaaaaa","state":"running","trigger":"schedule","creator":"x",` +
		`"started":"2026-10-09T02:00:00Z","updated":"2026-10-09T02:00:00Z"}`
	key := job.RunPrefix + "20261009T020000Z-aaaaaa"
	if _, err := r.mods.Store(port.ModuleBackup).Put(ctx, key, []byte(stale), 0); err != nil {
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

// A run whose sidecar write fails leaves an object and no sidecar: it is not
// a backup, the list does not show it, and a prune removes it once old.
func TestARunThatFailsBeforeItsSidecarLeavesNoBackup(t *testing.T) {
	r := newRig(t)
	j := r.job()
	r.archive.fail, r.archive.suffix = true, backup.SidecarSuffix
	res, err := j.Run(ctx, job.Request{Trigger: job.TriggerSchedule})
	if err == nil || res.Outcome != job.StateFailed {
		t.Fatalf("%+v, %v", res, err)
	}
	names, _ := r.archive.List(ctx, "backup/")
	if len(names) != 1 || names[0] != backup.ObjectName("example", res.ID) {
		t.Errorf("objects %v", names)
	}
	if list, _ := j.List(ctx); len(list) != 0 {
		t.Errorf("listed %+v", list)
	}
	if _, err := backup.Open(ctx, r.archive, identities, "example", res.ID, t.TempDir()); !errors.Is(err, backup.ErrFormat) {
		t.Errorf("open: %v", err)
	}
	r.archive.fail = false
	r.clk.advance(job.OrphanAge + time.Hour)
	pass, _, err := j.Prune(ctx, audit.System(), false)
	if err != nil || len(pass.Orphans) != 1 || pass.Orphans[0] != res.ID {
		t.Fatalf("%+v %v", pass, err)
	}
	if names, _ := r.archive.List(ctx, "backup/"); len(names) != 0 {
		t.Errorf("left %v", names)
	}
}

func TestPruneKeepsTheNewestAndTheYoungAndDeletesTheSidecarFirst(t *testing.T) {
	r := newRig(t)
	j := r.job(func(c *job.Config) { c.Keep = 100 }) // the runs do not prune
	var ids []string
	for range 4 {
		ids = append(ids, r.run(j).ID)
		r.clk.advance(30 * time.Hour)
	}
	j = r.job()
	// 4 backups, 30 h apart; keep 2, max age 48 h: the two oldest are beyond
	// keep and older than 48 h. The clock is now 30 h after the last.
	pass, outcome, err := j.Prune(ctx, audit.System(), true)
	if err != nil || outcome != "pruned" || !pass.DryRun || len(pass.Removed) != 2 || len(r.archive.deletes) != 0 {
		t.Fatalf("dry run %+v %q %v, deleted %v", pass, outcome, err, r.archive.deletes)
	}
	pass, _, err = j.Prune(ctx, audit.System(), false)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := j.List(ctx)
	if len(after) != 2 || after[0].ID != ids[3] || after[1].ID != ids[2] {
		t.Errorf("kept %+v, want %s and %s", after, ids[3], ids[2])
	}
	var want []string
	for _, id := range []string{ids[1], ids[0]} { // newest first, each sidecar before its object
		want = append(want, backup.SidecarName("example", id), backup.ObjectName("example", id))
	}
	if strings.Join(r.archive.deletes, " ") != strings.Join(want, " ") {
		t.Errorf("deletes %v, want %v", r.archive.deletes, want)
	}
	if pass.Kept != 2 {
		t.Errorf("pass %+v", pass)
	}
	st, _ := j.Status(ctx)
	if st.Retention == nil || st.Retention.Keep != 2 {
		t.Errorf("retention record %+v", st.Retention)
	}
}

// Object Lock refuses the delete: it is counted and reported, not retried,
// and the backup stays listed.
func TestARefusedDeleteIsCountedAndTheBackupStays(t *testing.T) {
	r := newRig(t)
	j := r.job(func(c *job.Config) { c.Keep = 100 })
	var ids []string
	for range 3 {
		ids = append(ids, r.run(j).ID)
		r.clk.advance(30 * time.Hour)
	}
	r.clk.advance(30 * time.Hour)
	r.archive.lock = ids[0]
	pass, _, err := r.job(func(c *job.Config) { c.Keep = 1 }).Prune(ctx, audit.System(), false)
	if err == nil || !strings.Contains(err.Error(), "Object Lock") {
		t.Errorf("err %v", err)
	}
	if len(pass.Failed) != 1 || pass.Failed[0] != ids[0] || len(pass.Removed) != 1 || pass.Removed[0] != ids[1] {
		t.Errorf("pass %+v", pass)
	}
	if list, _ := j.List(ctx); len(list) != 2 {
		t.Errorf("listed %+v", list)
	}
	if o, err := r.archive.Read(ctx, backup.ObjectName("example", ids[0])); err != nil || len(o.Body) == 0 {
		t.Error("the object of a backup whose sidecar stands was deleted")
	}
}

func TestPruneRemovesTheObjectOfARunThatNeverWroteASidecar(t *testing.T) {
	r := newRig(t)
	j := r.job()
	old := backup.ObjectName("example", "20260901T020000Z-dead01")
	young := backup.ObjectName("example", "20261009T020000Z-dead02")
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

func TestTheConfigIsChecked(t *testing.T) {
	r := newRig(t)
	good := func() job.Config {
		return job.Config{Installation: "example", State: r.mods.Store(port.ModuleBackup), Archive: r.archive,
			Source: export.Source{State: fanout{r.mods}}, Keep: 1, Recipients: []string{recipient.Recipient().String()}}
	}
	if _, err := job.New(good()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*job.Config){
		"no installation": func(c *job.Config) { c.Installation = "" },
		"bad name":        func(c *job.Config) { c.Installation = "a/b" },
		"no recipients":   func(c *job.Config) { c.Recipients = nil },
		"an identity":     func(c *job.Config) { c.Recipients = []string{recipient.String()} },
	} {
		c := good()
		mutate(&c)
		if _, err := job.New(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
