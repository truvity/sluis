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
	StateRunning = "running"
	// StatePaused is a run of an earlier release that paused; it is closed
	// as abandoned.
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
	// OutcomeIdle is the answer to a resume-only call: a run is one
	// invocation since format 2 and there is never anything to continue.
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
	ReasonUpload    = "upload"
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
	// OrphanAge is how old the object of a run with no sidecar must be before
	// a prune deletes it.
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
	// Modules and Records are the backup's, set when it completed; Chunks is
	// the number of archive objects (1 since format 2) and Size the bytes of
	// the encrypted object.
	Modules int   `json:"modules,omitempty"`
	Records int64 `json:"records,omitempty"`
	Chunks  int   `json:"chunks,omitempty"`
	Size    int64 `json:"size,omitempty"`
	// Resumed is whether a run of an earlier release continued an
	// interrupted one; a run is one invocation since format 2.
	Resumed bool `json:"resumed,omitempty"`
	// Reason is the failure word, Error a one-line summary of the cause.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// View is a run as the CLI and the module's methods show it.
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
	Size     int64     `json:"size,omitempty"`
	Resumed  bool      `json:"resumed,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Error    string    `json:"error,omitempty"`
	// Units and Done were the progress of a paused run; a run is one
	// invocation since format 2 and they are always 0.
	Units int `json:"units,omitempty"`
	Done  int `json:"done,omitempty"`
}

// View is the run as shown.
func (r Run) View() View {
	v := View{ID: r.ID, State: r.State, Trigger: r.Trigger, Creator: r.Creator, Started: r.Started, Updated: r.Updated,
		Finished: r.Finished, Modules: r.Modules, Records: r.Records, Chunks: r.Chunks, Size: r.Size, Resumed: r.Resumed,
		Reason: r.Reason, Error: r.Error}
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

// Info is one backup in the archive, as its sidecar says. The sidecar is not
// verified: a listing is for showing, and a restore verifies the object.
type Info struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Creator string    `json:"creator"`
	Format  int       `json:"format"`
	Layout  string    `json:"layout"`
	Modules int       `json:"modules"`
	Records int64     `json:"records"`
	Chunks  int       `json:"chunks"`
	// Size is the encrypted object's.
	Size int64 `json:"size,omitempty"`
	// Error is why the sidecar could not be read, when it could not.
	Error string `json:"error,omitempty"`
}

// Config is what a [Job] is made of.
type Config struct {
	// Installation is the archive path segment, the instance name.
	Installation string
	// Creator names the writer in each manifest, "sluis-backup 1.76.1".
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
	// Recipients are the age X25519 public keys (age1...) every backup is
	// encrypted to; at least one. The job holds nothing that decrypts.
	Recipients []string
	// TempDir holds the spool and the encrypted file of a run; os.TempDir()
	// (/tmp on Lambda) when empty.
	TempDir string
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
	case c.State == nil || c.Archive == nil:
		return nil, errors.New("backup: the job needs a State and an archive Blob")
	case c.Source.State == nil:
		return nil, errors.New("backup: the job needs an export source")
	case c.Keep < 1:
		return nil, errors.New("backup: retention keep must be at least 1")
	}
	if _, err := backup.ParseRecipients(c.Recipients); err != nil {
		return nil, err
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
	// ResumeOnly was the schedule that continued a paused run. A run is one
	// invocation since format 2: it answers [OutcomeIdle] and touches nothing.
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

// Run takes the lease and runs one backup. A failed run is an error and its
// record says so; a run that is contended, skipped or idle is a result.
func (j *Job) Run(ctx context.Context, req Request) (Result, error) {
	if req.ResumeOnly {
		// The schedules of an earlier release still send the resume event.
		return Result{Outcome: OutcomeIdle}, nil
	}
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
	run, rev, err := j.start(ctx, req)
	if err != nil {
		return Result{}, err
	}
	return j.execute(ctx, req, run, rev)
}

// start closes every unfinished run (under the lease, a run still marked
// running is one whose invocation died) and writes the record of a new one.
func (j *Job) start(ctx context.Context, req Request) (*Run, port.Revision, error) {
	runs, err := j.records(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, r := range runs {
		if r.run.State == StateRunning || r.run.State == StatePaused {
			if err := j.fail(ctx, &r.run, r.rev, ReasonAbandoned, errors.New("the run was interrupted"), req.Actor); err != nil {
				return nil, "", err
			}
		}
	}
	now := j.now()
	run := Run{ID: j.newID(now), State: StateRunning, Trigger: req.Trigger, Creator: j.c.Creator, Started: now, Updated: now}
	rev, err := j.create(ctx, run)
	if err != nil {
		return nil, "", err
	}
	return &run, rev, nil
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
	return backup.Params{Instance: j.c.Installation, ID: run.ID, Layout: backup.LayoutV5, Creator: run.Creator,
		Recipients: j.c.Recipients, Now: func() time.Time { return run.Started }, TempDir: j.c.TempDir}
}

// execute exports into a local encrypted file, uploads it, and writes the
// sidecar last.
func (j *Job) execute(ctx context.Context, req Request, run *Run, rev port.Revision) (Result, error) {
	w, err := backup.NewWriter(j.params(run))
	if err != nil {
		return j.failed(ctx, req, run, rev, ReasonKey, err)
	}
	a, err := export.Run(ctx, w, j.c.Source, export.Options{Now: j.c.Now, Log: j.log})
	if err != nil {
		reason := ReasonExport
		if ctx.Err() != nil {
			reason = ReasonCanceled
		}
		return j.failed(ctx, req, run, rev, reason, err)
	}
	defer a.Remove()
	if err := backup.Publish(ctx, j.c.Archive, a); err != nil {
		reason := ReasonUpload
		if ctx.Err() != nil {
			reason = ReasonCanceled
		}
		return j.failed(ctx, req, run, rev, reason, err)
	}
	return j.completed(ctx, req, run, rev, a)
}

func (j *Job) completed(ctx context.Context, req Request, run *Run, rev port.Revision, a *backup.Archive) (Result, error) {
	run.State, run.Updated, run.Finished = StateCompleted, j.now(), j.now()
	run.Reason, run.Error = "", ""
	run.Modules, run.Records = len(a.Sidecar.Modules), a.Sidecar.Records()
	run.Chunks, run.Size = 1, a.Sidecar.Object.Size
	if _, err := j.update(ctx, *run, rev); err != nil {
		return Result{}, err
	}
	j.record(ctx, audit.BackupCompleted(req.Actor, run.ID, j.audit(run, "")))
	j.log.InfoContext(ctx, "a backup was written", slog.String("id", run.ID), slog.Int("modules", run.Modules),
		slog.Int64("records", run.Records), slog.Int64("bytes", run.Size))
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
func (j *Job) records(ctx context.Context) ([]*recorded, error) {
	var out []*recorded
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
			out = append(out, &recorded{r, rec.Revision})
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
	// Unfinished is a run that is running.
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
