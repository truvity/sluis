package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/storage/logattr"
)

// The states of a run.
const (
	StateRunning   = "running"
	StatePaused    = "paused"
	StateCompleted = "completed"
	StateFailed    = "failed"
)

// The triggers of a run.
const (
	TriggerSchedule = "schedule"
	TriggerRun      = "run"
	TriggerCLI      = "cli"
)

// The outcomes of [Job.Run].
const (
	// OutcomeCompleted is a backup written and sealed.
	OutcomeCompleted = "completed"
	// OutcomePaused is a run that stopped at a unit boundary for want of time;
	// the next run continues it.
	OutcomePaused = "paused"
	// OutcomeIdle is a resume-only run with nothing to continue.
	OutcomeIdle = "idle"
	// OutcomeContended is a run another runner holds the lease of.
	OutcomeContended = "contended"
	// OutcomeMaintenance is a run skipped because the module is under
	// maintenance (a restore is writing).
	OutcomeMaintenance = "maintenance"
)

// The failure words of a run, short and without a value in them.
const (
	ReasonKey       = "key"
	ReasonExport    = "export"
	ReasonCanceled  = "canceled"
	ReasonAbandoned = "abandoned"
)

// The State keys of the module's own records (internal/port/layout5.go).
const (
	// RunPrefix is the prefix of the status records; the id follows.
	RunPrefix = "rec.backup.run."
	// RetentionKey is the record of the last retention pass.
	RetentionKey = "rec.backup.retention"
	// LeaseKind and LeaseTarget are the lease a run and a prune hold:
	// `lease.backup:run`.
	LeaseKind   = "backup"
	LeaseTarget = "run"
)

// Timings.
const (
	// Margin is how much of an invocation's deadline must be left to start the
	// next unit of the export; with less, the run pauses.
	Margin = 3 * time.Minute
	// ResumeWindow is how old an unfinished run may be and still be continued.
	ResumeWindow = 24 * time.Hour
	// OrphanAge is how old the chunks of a run with no manifest must be before
	// a prune deletes them.
	OrphanAge = 7 * 24 * time.Hour
	// KeepRuns is how many status records a prune leaves.
	KeepRuns = 100
	maxError = 300
)

// Run is a run's status record.
type Run struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Trigger string `json:"trigger"`
	Creator string `json:"creator"`
	// Started is when the run began; Updated the last write of this record;
	// Finished when it completed or failed.
	Started  time.Time `json:"started"`
	Updated  time.Time `json:"updated"`
	Finished time.Time `json:"finished,omitzero"`
	// Modules, Records and Chunks are the backup's, set when it completed.
	Modules int   `json:"modules,omitempty"`
	Records int64 `json:"records,omitempty"`
	Chunks  int   `json:"chunks,omitempty"`
	// Resumed is whether the run continued an interrupted one.
	Resumed bool `json:"resumed,omitempty"`
	// Reason is the failure word, Error a one-line summary of the cause.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
	// Checkpoint is what a resume needs; gone once the run completed.
	Checkpoint *export.Checkpoint `json:"checkpoint,omitempty"`
}

// View is a run as the CLI and the module's methods show it: no checkpoint,
// only how far the export got.
type View struct {
	ID       string    `json:"id"`
	State    string    `json:"state"`
	Trigger  string    `json:"trigger"`
	Creator  string    `json:"creator"`
	Started  time.Time `json:"started"`
	Updated  time.Time `json:"updated"`
	Finished time.Time `json:"finished,omitzero"`
	Modules  int       `json:"modules,omitempty"`
	Records  int64     `json:"records,omitempty"`
	Chunks   int       `json:"chunks,omitempty"`
	Resumed  bool      `json:"resumed,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Error    string    `json:"error,omitempty"`
	// Units and Done are the export's progress while it is unfinished.
	Units int `json:"units,omitempty"`
	Done  int `json:"done,omitempty"`
}

// View is the run as shown.
func (r Run) View() View {
	v := View{ID: r.ID, State: r.State, Trigger: r.Trigger, Creator: r.Creator, Started: r.Started, Updated: r.Updated,
		Finished: r.Finished, Modules: r.Modules, Records: r.Records, Chunks: r.Chunks, Resumed: r.Resumed,
		Reason: r.Reason, Error: r.Error}
	if r.Checkpoint != nil && (r.State == StateRunning || r.State == StatePaused) {
		v.Units, v.Done = r.Checkpoint.Units, r.Checkpoint.Next
	}
	return v
}

// Pass is the record of the last retention pass.
type Pass struct {
	At      time.Time `json:"at"`
	Keep    int       `json:"keep"`
	MaxAge  string    `json:"maxAge"`
	Kept    int       `json:"kept"`
	Removed []string  `json:"removed,omitempty"`
	Orphans []string  `json:"orphans,omitempty"`
	// Failed lists what a delete refused (Object Lock, a permission), by id.
	Failed []string `json:"failed,omitempty"`
	DryRun bool     `json:"-"`
}

// Rule is the retention in words, for a record.
func (p Pass) Rule() string { return fmt.Sprintf("keep %d, max age %s", p.Keep, p.MaxAge) }

// Info is one backup in the archive, as its manifest says. The manifest is
// not verified: a listing is for showing, and a restore opens with the key.
type Info struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Creator string    `json:"creator"`
	Format  int       `json:"format"`
	Layout  string    `json:"layout"`
	Modules int       `json:"modules"`
	Records int64     `json:"records"`
	Chunks  int       `json:"chunks"`
	// Error is why the manifest could not be read, when it could not.
	Error string `json:"error,omitempty"`
}

// Config is what a [Job] is made of.
type Config struct {
	// Installation is the archive path segment, the instance name.
	Installation string
	// Creator names the writer in each manifest, "sluis-backup 1.75.0".
	Creator string
	// State is the backup module's own State: its records and its lease. On
	// layout v5 the router over the tables routes these keys to that table.
	State port.State
	// Maintenance is asked before a run and a prune; nil never skips.
	Maintenance rails.Pause
	// Source is what the export reads.
	Source export.Source
	// Archive is the bucket the backups are written to.
	Archive port.Blob
	// Key seals each backup's data key.
	Key backup.Key
	// Keep and MaxAge are the retention rule.
	Keep   int
	MaxAge time.Duration
	// Audit records the outcomes; nil records nothing.
	Audit audit.Recorder
	Log   *slog.Logger
	// Holder names this runner in the lease; rails.NewHolder when empty.
	Holder string
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// ChunkBytes overrides the chunk size, for tests.
	ChunkBytes int
	// Suffix makes the random suffix of an id; for tests.
	Suffix func() string
}

// Job is the backup module's work.
type Job struct {
	c      Config
	leases *rails.Leases
	log    *slog.Logger
}

// New builds a job.
func New(c Config) (*Job, error) {
	switch {
	case !backup.ValidName(c.Installation):
		return nil, errors.New("backup: the installation name must be one path segment of letters, digits, '.', '-' and '_'")
	case c.State == nil || c.Archive == nil || c.Key == nil:
		return nil, errors.New("backup: the job needs a State, an archive Blob and a key")
	case c.Source.State == nil:
		return nil, errors.New("backup: the job needs an export source")
	case c.Keep < 1:
		return nil, errors.New("backup: retention keep must be at least 1")
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Holder == "" {
		c.Holder = rails.NewHolder()
	}
	if c.Creator == "" {
		c.Creator = "sluis-backup"
	}
	return &Job{c: c, log: c.Log, leases: &rails.Leases{State: c.State, Holder: c.Holder, Log: c.Log, Maintenance: c.Maintenance}}, nil
}

func (j *Job) now() time.Time { return j.c.Now().UTC() }

// Request is one call of [Job.Run].
type Request struct {
	// Trigger is [TriggerSchedule], [TriggerRun] or [TriggerCLI].
	Trigger string
	// Actor is who asked, for the audit record; the system for a schedule.
	Actor audit.Actor
	// ResumeOnly continues an unfinished run and starts none.
	ResumeOnly bool
}

// Result is what a call of [Job.Run] did.
type Result struct {
	Outcome string `json:"outcome"`
	// ID is the run's, empty when none ran.
	ID string `json:"id,omitempty"`
	// Run is the run's record as shown, absent when none ran.
	Run *View `json:"run,omitempty"`
}

// errPause stops an export at a unit boundary.
var errPause = errors.New("backup: paused for want of time")

// Run takes the lease and runs one backup, or continues an interrupted one.
// A failed run is an error and its record says so; a run that is paused,
// contended, skipped or idle is a result.
func (j *Job) Run(ctx context.Context, req Request) (Result, error) {
	if req.Trigger == "" {
		req.Trigger = TriggerRun
	}
	if req.Actor.Kind == "" {
		req.Actor = audit.System()
	}
	var res Result
	var runErr error
	ran, err := j.leases.Do(ctx, LeaseKind, LeaseTarget, func(ctx context.Context) {
		res, runErr = j.locked(ctx, req)
	})
	switch {
	case err != nil:
		return Result{}, err
	case !ran:
		if j.c.Maintenance != nil && j.c.Maintenance.Writable(ctx) != nil {
			return Result{Outcome: OutcomeMaintenance}, nil
		}
		return Result{Outcome: OutcomeContended}, nil
	}
	return res, runErr
}

func (j *Job) locked(ctx context.Context, req Request) (Result, error) {
	run, rev, resumed, err := j.pickup(ctx, req)
	if err != nil {
		return Result{}, err
	}
	if run == nil {
		return Result{Outcome: OutcomeIdle}, nil
	}
	if resumed {
		run.Resumed, run.State, run.Reason = true, StateRunning, ""
	}
	return j.execute(ctx, req, run, rev)
}

// pickup finds the unfinished run to continue, closing the ones too old or
// without a checkpoint, or starts a new one. nil, with no error, is nothing to
// do (ResumeOnly and no unfinished run).
func (j *Job) pickup(ctx context.Context, req Request) (*Run, port.Revision, bool, error) {
	runs, err := j.records(ctx)
	if err != nil {
		return nil, "", false, err
	}
	var open []recorded
	for _, r := range runs {
		if r.run.State == StateRunning || r.run.State == StatePaused {
			open = append(open, r)
		}
	}
	// The newest unfinished run is the one to continue; any other is closed.
	for i := len(open) - 1; i >= 0; i-- {
		r := open[i]
		if i == len(open)-1 && r.run.Checkpoint != nil && j.now().Sub(r.run.Started) < ResumeWindow {
			return &r.run, r.rev, true, nil
		}
		if err := j.fail(ctx, &r.run, r.rev, ReasonAbandoned, errors.New("the run was interrupted and cannot be continued"), req.Actor); err != nil {
			return nil, "", false, err
		}
	}
	if req.ResumeOnly {
		return nil, "", false, nil
	}
	now := j.now()
	run := Run{ID: j.newID(now), State: StateRunning, Trigger: req.Trigger, Creator: j.c.Creator, Started: now, Updated: now}
	rev, err := j.create(ctx, run)
	if err != nil {
		return nil, "", false, err
	}
	return &run, rev, false, nil
}

func (j *Job) newID(now time.Time) string {
	suffix := ""
	if j.c.Suffix != nil {
		suffix = j.c.Suffix()
	} else {
		var b [3]byte
		_, _ = rand.Read(b[:])
		suffix = hex.EncodeToString(b[:])
	}
	return now.Format("20060102T150405Z") + "-" + suffix
}

func (j *Job) params(run *Run) backup.Params {
	return backup.Params{Installation: j.c.Installation, ID: run.ID, Layout: backup.LayoutV5, Creator: run.Creator,
		Now: j.c.Now, ChunkBytes: j.c.ChunkBytes}
}

func (j *Job) execute(ctx context.Context, req Request, run *Run, rev port.Revision) (Result, error) {
	var w *backup.Writer
	var err error
	if run.Checkpoint != nil {
		w, err = backup.Resume(ctx, j.c.Archive, j.c.Key, j.params(run), run.Checkpoint.Progress)
	} else {
		w, err = backup.NewWriter(ctx, j.c.Archive, j.c.Key, j.params(run))
	}
	if err != nil {
		return j.failed(ctx, req, run, rev, ReasonKey, err)
	}
	opt := export.Options{Now: j.c.Now, Log: j.log, Resume: run.Checkpoint}
	opt.Checkpoint = func(ctx context.Context, cp export.Checkpoint) error {
		run.Checkpoint, run.Updated = &cp, j.now()
		next, err := j.update(ctx, *run, rev)
		if err != nil {
			return err
		}
		rev = next
		if lowOnTime(ctx) {
			return errPause
		}
		return nil
	}
	m, err := export.Run(ctx, w, j.c.Source, opt)
	switch {
	case err == nil:
	case errors.Is(err, errPause):
		return j.paused(ctx, run, rev, "")
	case ctx.Err() != nil:
		// The function's time ran out, or the lease was lost: the last
		// checkpoint stands, and the next run continues from it.
		return j.paused(ctx, run, rev, ReasonCanceled)
	default:
		return j.failed(ctx, req, run, rev, ReasonExport, err)
	}
	return j.completed(ctx, req, run, rev, m)
}

// lowOnTime is whether the invocation has less than [Margin] left.
func lowOnTime(ctx context.Context) bool {
	d, ok := ctx.Deadline()
	return ok && time.Until(d) < Margin
}

func (j *Job) paused(ctx context.Context, run *Run, rev port.Revision, reason string) (Result, error) {
	// ctx may be done; the record is written all the same.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	run.State, run.Updated, run.Reason = StatePaused, j.now(), reason
	if _, err := j.update(wctx, *run, rev); err != nil {
		return Result{}, err
	}
	j.log.InfoContext(ctx, "a backup run paused; the next run continues it", slog.String("id", run.ID), slog.String("reason", reason))
	v := run.View()
	return Result{Outcome: OutcomePaused, ID: run.ID, Run: &v}, nil
}

func (j *Job) completed(ctx context.Context, req Request, run *Run, rev port.Revision, m *backup.Manifest) (Result, error) {
	run.State, run.Updated, run.Finished = StateCompleted, j.now(), j.now()
	run.Checkpoint, run.Reason, run.Error = nil, "", ""
	run.Modules, run.Records = len(m.Modules), 0
	run.Chunks = 0
	for _, e := range m.Modules {
		for _, p := range []backup.Part{e.State, e.Secrets, e.Blobs} {
			run.Records += p.Records
			run.Chunks += len(p.Chunks)
		}
	}
	if _, err := j.update(ctx, *run, rev); err != nil {
		return Result{}, err
	}
	j.record(ctx, audit.BackupCompleted(req.Actor, run.ID, j.audit(run, "")))
	j.log.InfoContext(ctx, "a backup was written", slog.String("id", run.ID), slog.Int("modules", run.Modules),
		slog.Int64("records", run.Records), slog.Int("chunks", run.Chunks))
	// Retention is part of the run, but its failure is not the backup's: the
	// backup exists. The next run prunes again.
	if _, err := j.prune(ctx, req.Actor, false); err != nil {
		j.log.WarnContext(ctx, "the retention pass failed; the next run retries it", logattr.SafeError("error", err))
	}
	v := run.View()
	return Result{Outcome: OutcomeCompleted, ID: run.ID, Run: &v}, nil
}

func (j *Job) failed(ctx context.Context, req Request, run *Run, rev port.Revision, reason string, cause error) (Result, error) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := j.fail(wctx, run, rev, reason, cause, req.Actor); err != nil {
		return Result{}, errors.Join(cause, err)
	}
	v := run.View()
	return Result{Outcome: StateFailed, ID: run.ID, Run: &v}, fmt.Errorf("backup %s: %s: %w", run.ID, reason, cause)
}

// fail closes a run as failed and says so on the trail.
func (j *Job) fail(ctx context.Context, run *Run, rev port.Revision, reason string, cause error, actor audit.Actor) error {
	run.State, run.Updated, run.Finished = StateFailed, j.now(), j.now()
	run.Reason, run.Error = reason, oneLine(cause)
	run.Checkpoint = nil
	if _, err := j.update(ctx, *run, rev); err != nil {
		return err
	}
	j.record(ctx, audit.BackupFailed(actor, run.ID, j.audit(run, reason), run.Error))
	j.log.ErrorContext(ctx, "a backup run failed", slog.String("id", run.ID), slog.String("reason", reason), logattr.SafeError("error", cause))
	return nil
}

func (j *Job) audit(run *Run, reason string) audit.BackupRun {
	return audit.BackupRun{Installation: j.c.Installation, Trigger: run.Trigger, Modules: run.Modules, Records: run.Records,
		Chunks: run.Chunks, Resumed: run.Resumed, Reason: reason}
}

// record hands a record to the trail, which never fails the caller.
func (j *Job) record(ctx context.Context, r *record.Record) {
	if j.c.Audit != nil {
		j.c.Audit.Record(ctx, r)
	}
}

func oneLine(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > maxError {
		s = s[:maxError] + "..."
	}
	return s
}

// --------------------------------------------------------------- records

type recorded struct {
	run Run
	rev port.Revision
}

func (j *Job) create(ctx context.Context, run Run) (port.Revision, error) {
	raw, _ := json.Marshal(run)
	return j.c.State.Create(ctx, RunPrefix+run.ID, raw, 0)
}

func (j *Job) update(ctx context.Context, run Run, rev port.Revision) (port.Revision, error) {
	raw, err := json.Marshal(run)
	if err != nil {
		return "", err
	}
	next, err := j.c.State.Update(ctx, RunPrefix+run.ID, raw, 0, rev)
	if errors.Is(err, port.ErrConflict) || errors.Is(err, port.ErrNotFound) {
		return "", fmt.Errorf("backup %s: the run's record moved under it, so another runner has taken over: %w", run.ID, rails.ErrLost)
	}
	return next, err
}

// records are the status records, oldest first (an id sorts by its time).
func (j *Job) records(ctx context.Context) ([]recorded, error) {
	var out []recorded
	page := ""
	for {
		p, err := j.c.State.List(ctx, RunPrefix, page, 0)
		if err != nil {
			return nil, fmt.Errorf("list the backup runs: %w", err)
		}
		for _, rec := range p.Records {
			var r Run
			if json.Unmarshal(rec.Value, &r) != nil || r.ID == "" {
				continue
			}
			out = append(out, recorded{r, rec.Revision})
		}
		if p.Next == "" {
			break
		}
		page = p.Next
	}
	sort.Slice(out, func(a, b int) bool { return out[a].run.ID < out[b].run.ID })
	return out, nil
}

// Runs are the newest status records, newest first; limit 0 is all.
func (j *Job) Runs(ctx context.Context, limit int) ([]View, error) {
	rs, err := j.records(ctx)
	if err != nil {
		return nil, err
	}
	var out []View
	for i := len(rs) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		out = append(out, rs[i].run.View())
	}
	return out, nil
}

// Status is what `backup.status` answers.
type Status struct {
	// Latest is the newest run, whatever its state.
	Latest *View `json:"latest,omitempty"`
	// LastCompleted is the newest completed run.
	LastCompleted *View `json:"lastCompleted,omitempty"`
	// Unfinished is a run that is running or paused.
	Unfinished *View `json:"unfinished,omitempty"`
	// Retention is the last prune pass.
	Retention *Pass `json:"retention,omitempty"`
}

// Status reads the latest records.
func (j *Job) Status(ctx context.Context) (Status, error) {
	rs, err := j.records(ctx)
	if err != nil {
		return Status{}, err
	}
	var s Status
	for i := len(rs) - 1; i >= 0; i-- {
		v := rs[i].run.View()
		if s.Latest == nil {
			s.Latest = &v
		}
		switch rs[i].run.State {
		case StateCompleted:
			if s.LastCompleted == nil {
				s.LastCompleted = &v
			}
		case StateRunning, StatePaused:
			if s.Unfinished == nil {
				s.Unfinished = &v
			}
		}
	}
	if rec, err := j.c.State.Get(ctx, RetentionKey); err == nil {
		var p Pass
		if json.Unmarshal(rec.Value, &p) == nil {
			s.Retention = &p
		}
	} else if !errors.Is(err, port.ErrNotFound) {
		return Status{}, fmt.Errorf("read the retention record: %w", err)
	}
	return s, nil
}
