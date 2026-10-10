package restorejob

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
	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/storage/logattr"
)

// The states of a restore.
const (
	StateRunning   = "running"
	StatePaused    = "paused"
	StateCompleted = "completed"
	StateFailed    = "failed"
)

// The outcomes of [Job.Start].
const (
	// OutcomeCompleted is a restore that verified and lifted maintenance.
	OutcomeCompleted = "completed"
	// OutcomePaused is a restore that stopped for want of time; the next call
	// continues it and maintenance stays set.
	OutcomePaused = "paused"
	// OutcomeFailed is a restore that failed; maintenance stays set and the
	// record says why.
	OutcomeFailed = "failed"
	// OutcomeContended is a start while another runner holds the lease.
	OutcomeContended = "contended"
	// OutcomeIdle is a resume with nothing to continue.
	OutcomeIdle = "idle"
	// OutcomeRefused is a start that changed nothing: no such backup, or an
	// archive the key does not open.
	OutcomeRefused = "refused"
)

// The failure words of a restore, short and without a value in them.
const (
	ReasonNotEmpty    = "not-empty"
	ReasonVerify      = "verify"
	ReasonArchive     = "archive"
	ReasonIntegrity   = "integrity"
	ReasonKey         = "key"
	ReasonBackup      = "backup"
	ReasonMaintenance = "maintenance"
	ReasonClear       = "clear"
	ReasonRestore     = "restore"
	ReasonSuperseded  = "superseded"
)

// Maintenance values of [Run.Maintenance].
const (
	MaintenanceOn      = "on"
	MaintenanceCleared = "cleared"
)

const (
	// RunPrefix is the prefix of the status records; the id follows.
	RunPrefix = "rec.backup.restore."
	// LeaseKind and LeaseTarget are the lease a restore holds:
	// `lease.restore:run`, in the backup module's table.
	LeaseKind   = "restore"
	LeaseTarget = "run"

	// Margin is how much of an invocation's deadline is kept back from Apply for
	// writing the record and the audit trail.
	Margin = 90 * time.Second
	// Settle is how long after the flags are set the writes wait: the modules
	// cache the flag for [maintenance.DefaultTTL], and this is that and a margin.
	Settle = maintenance.DefaultTTL + 5*time.Second

	maxError = 300
	maxNote  = 120
)

// Run is a restore's status record.
type Run struct {
	ID       string `json:"id"`
	BackupID string `json:"backup"`
	State    string `json:"state"`
	// Overwrite is whether the admin asked for a different value in the
	// destination to be replaced.
	Overwrite bool `json:"overwrite,omitempty"`
	// By and Note say who asked and why, as the caller declared them. IAM
	// decided who may invoke the function; these are for the people reading.
	By      string    `json:"by,omitempty"`
	Note    string    `json:"note,omitempty"`
	Started time.Time `json:"started"`
	Updated time.Time `json:"updated"`
	// Finished is when it completed or failed.
	Finished time.Time `json:"finished,omitzero"`
	// FlagsSet is when every module's maintenance flag was set.
	FlagsSet time.Time `json:"flagsSet,omitzero"`
	// Maintenance is "on" from the first flag written until it is lifted.
	Maintenance string `json:"maintenance,omitempty"`
	// Slices is how many invocations worked on it.
	Slices int `json:"slices,omitempty"`
	// Create, Overwritten and Same are the counts of the last slice's pass, which
	// on a completed run is the whole restore.
	Create      int `json:"create,omitempty"`
	Overwritten int `json:"overwritten,omitempty"`
	Same        int `json:"same,omitempty"`
	// Reason is the failure word (or "time" and "canceled" on a pause), Error a
	// one-line summary of the cause.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Config is what a [Job] is made of.
type Config struct {
	// Installation is the archive path segment, the instance name.
	Installation string
	// State is the backup module's State: the records and the lease.
	State port.State
	// Modules are the modules that have a table, and Flags the State of a
	// module's OWN table, where its maintenance flag is.
	Modules []port.Module
	Flags   func(port.Module) port.State
	// Target is what the archive is written to.
	Target restore.Target
	// Archive and Key open the backups.
	Archive port.Blob
	Key     backup.Key
	// Audit records the outcomes; nil records nothing.
	Audit audit.Recorder
	Log   *slog.Logger
	// Holder names this runner in the lease; rails.NewHolder when empty.
	Holder string
	Now    func() time.Time
	// Margin and Settle override the defaults, for tests.
	Margin, Settle time.Duration
	// Sleep waits d or until ctx ends; a timer when nil.
	Sleep func(ctx context.Context, d time.Duration) error
	// MaxItems bounds the names a preview lists per section.
	MaxItems int
	// Suffix makes the random suffix of an id; for tests.
	Suffix func() string
}

// Job is the restore function's work.
type Job struct {
	c      Config
	leases *rails.Leases
	log    *slog.Logger
}

// New builds a job.
func New(c Config) (*Job, error) {
	switch {
	case !backup.ValidName(c.Installation):
		return nil, errors.New("restore: the installation name must be one path segment of letters, digits, '.', '-' and '_'")
	case c.State == nil || c.Archive == nil || c.Key == nil:
		return nil, errors.New("restore: the job needs a State, an archive Blob and a key")
	case len(c.Modules) == 0 || c.Flags == nil:
		return nil, errors.New("restore: the job needs the modules' tables to set the maintenance flag in")
	case c.Target.Table == nil || c.Target.Secrets == nil || c.Target.Blob == nil:
		return nil, errors.New("restore: the job needs a target installation")
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
	if c.Margin == 0 {
		c.Margin = Margin
	}
	if c.Settle == 0 {
		c.Settle = Settle
	}
	if c.Sleep == nil {
		c.Sleep = sleep
	}
	// No Maintenance on the leases: the restore sets the flag itself, and its own
	// table's flag must not stop it from continuing.
	return &Job{c: c, log: c.Log, leases: &rails.Leases{State: c.State, Holder: c.Holder, Log: c.Log}}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (j *Job) now() time.Time { return j.c.Now().UTC() }

// Request is one call of [Job.Start].
type Request struct {
	// BackupID is the backup to restore from. Empty continues the unfinished
	// restore and starts none.
	BackupID string
	// Overwrite lets the restore replace a different value in the destination.
	// Pass it only when the admin asked for it.
	Overwrite bool
	// Actor is who asked, for the audit record.
	Actor audit.Actor
	// By and Note are the caller's words for the status record and the flag.
	By, Note string
}

// Result is what a call of [Job.Start] did.
type Result struct {
	Outcome string `json:"outcome"`
	// ID is the restore's, empty when none ran.
	ID  string `json:"id,omitempty"`
	Run *Run   `json:"run,omitempty"`
	// Reason and Error say why a start was refused.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Preview verifies the archive and reports what a restore would do. It takes no
// lease, sets no flag and writes nothing.
func (j *Job) Preview(ctx context.Context, backupID string) (*restore.Report, error) {
	r, err := backup.Open(ctx, j.c.Archive, j.c.Key, j.c.Installation, backupID)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return restore.Preview(ctx, r, j.c.Target, restore.Options{Now: j.c.Now, MaxItems: j.c.MaxItems})
}

// Start takes the lease and starts a restore, or continues the unfinished one.
// A restore that failed or paused is a result and its record says so; an error
// is the store, which the caller retries.
func (j *Job) Start(ctx context.Context, req Request) (Result, error) {
	if req.Actor.Kind == "" {
		req.Actor = audit.System()
	}
	req.By, req.Note = clip(req.By), clip(req.Note)
	var res Result
	var runErr error
	ran, err := j.leases.Do(ctx, LeaseKind, LeaseTarget, func(ctx context.Context) {
		res, runErr = j.locked(ctx, req)
	})
	switch {
	case err != nil:
		return Result{}, err
	case !ran:
		return Result{Outcome: OutcomeContended}, nil
	}
	return res, runErr
}

func (j *Job) locked(ctx context.Context, req Request) (Result, error) {
	open, err := j.unfinished(ctx)
	if err != nil {
		return Result{}, err
	}
	var run *recorded
	switch {
	case req.BackupID == "" && open == nil:
		return Result{Outcome: OutcomeIdle}, nil
	case req.BackupID == "":
		run = open
	case !backup.ValidName(req.BackupID):
		return Result{Outcome: OutcomeRefused, Reason: ReasonBackup, Error: "the backup id is not one path segment"}, nil
	}
	if run == nil && open != nil && open.run.BackupID == req.BackupID && open.run.Overwrite == req.Overwrite {
		run = open
	}
	// The archive is opened before anything changes: a backup that is not there,
	// or a key that does not open it, starts nothing.
	id := req.BackupID
	if run != nil {
		id = run.run.BackupID
	}
	r, err := backup.Open(ctx, j.c.Archive, j.c.Key, j.c.Installation, id)
	if err != nil {
		reason := ReasonBackup
		if errors.Is(err, backup.ErrKey) {
			reason = ReasonKey
		}
		return Result{Outcome: OutcomeRefused, Reason: reason, Error: oneLine(err)}, nil
	}
	defer r.Close()
	if run == nil {
		if open != nil {
			// Another backup, or another overwrite choice: the one before is
			// closed. Its maintenance flags stay, since this one covers them.
			if err := j.fail(ctx, &open.run, open.rev, ReasonSuperseded, errors.New("a newer restore was started"), req.Actor, len(r.Manifest().Modules)); err != nil {
				return Result{}, err
			}
		}
		now := j.now()
		run = &recorded{run: Run{ID: j.newID(now), BackupID: id, State: StateRunning, Overwrite: req.Overwrite, By: req.By, Note: req.Note,
			Started: now, Updated: now}}
		if run.rev, err = j.create(ctx, run.run); err != nil {
			return Result{}, err
		}
		j.record(ctx, audit.RestoreStarted(req.Actor, id, j.audit(len(r.Manifest().Modules), "")))
		j.log.InfoContext(ctx, "a restore started; every module goes under maintenance", slog.String("id", run.run.ID), slog.String("backup", id))
	} else {
		run.run.State, run.run.Reason, run.run.Error = StateRunning, "", ""
	}
	return j.execute(ctx, req, r, &run.run, run.rev)
}

func (j *Job) execute(ctx context.Context, req Request, r *backup.Reader, run *Run, rev port.Revision) (Result, error) {
	mods := len(r.Manifest().Modules)
	var err error
	// 1. Every module under maintenance before the first write.
	if err = j.setFlags(ctx, run); err != nil {
		return j.failed(ctx, req, run, rev, mods, ReasonMaintenance, err)
	}
	if run.FlagsSet.IsZero() {
		run.FlagsSet = j.now()
	}
	run.Updated, run.Slices = j.now(), run.Slices+1
	if rev, err = j.update(ctx, *run, rev); err != nil {
		return Result{}, err
	}
	// 2. The modules read the flag through a cache; wait it out.
	if wait := run.FlagsSet.Add(j.c.Settle).Sub(j.now()); wait > 0 {
		if err = j.c.Sleep(ctx, wait); err != nil {
			return j.paused(ctx, run, rev, "canceled", nil)
		}
	}
	// 3. Apply, under what is left of the invocation's time.
	actx, cancel := ctx, context.CancelFunc(func() {})
	if d, ok := ctx.Deadline(); ok {
		actx, cancel = context.WithDeadline(ctx, d.Add(-j.c.Margin))
	}
	defer cancel()
	rep, err := restore.Apply(actx, r, j.c.Target, restore.Options{Overwrite: run.Overwrite, Now: j.c.Now, MaxItems: 1})
	if rep != nil {
		t := rep.Totals()
		run.Create, run.Overwritten, run.Same = t.Create, t.Overwrite, t.Same
	}
	switch {
	case err == nil:
		return j.finish(ctx, req, run, rev, mods)
	case actx.Err() != nil || ctx.Err() != nil:
		reason := "time"
		if ctx.Err() != nil {
			reason = "canceled"
		}
		return j.paused(ctx, run, rev, reason, err)
	}
	return j.failed(ctx, req, run, rev, mods, classify(err), err)
}

// classify is the failure word of an error of [restore.Apply].
func classify(err error) string {
	switch {
	case errors.Is(err, restore.ErrNotEmpty):
		return ReasonNotEmpty
	case errors.Is(err, restore.ErrVerify):
		return ReasonVerify
	case errors.Is(err, restore.ErrArchive):
		return ReasonArchive
	case errors.Is(err, backup.ErrIntegrity), errors.Is(err, backup.ErrFormat):
		return ReasonIntegrity
	case errors.Is(err, backup.ErrKey):
		return ReasonKey
	}
	return ReasonRestore
}

// setFlags writes the flag in every module's table. It stops at the first
// failure; what was written stays.
func (j *Job) setFlags(ctx context.Context, run *Run) error {
	f := maintenance.Flag{State: maintenance.StateRestoring, Since: run.Started, By: run.By,
		Reason: "restore from backup " + run.BackupID}
	for _, m := range j.c.Modules {
		if err := maintenance.Write(ctx, j.c.Flags(m), f); err != nil {
			return fmt.Errorf("set the maintenance flag of %s: %w", m, err)
		}
		run.Maintenance = MaintenanceOn
	}
	return nil
}

// finish lifts maintenance after a restore that read back clean.
func (j *Job) finish(ctx context.Context, req Request, run *Run, rev port.Revision, mods int) (Result, error) {
	// The restore is done and read back; the invocation's own deadline must not
	// leave the modules closed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	for _, m := range j.c.Modules {
		if err := maintenance.Clear(ctx, j.c.Flags(m)); err != nil {
			return j.failed(ctx, req, run, rev, mods, ReasonClear, fmt.Errorf("lift the maintenance flag of %s: %w", m, err))
		}
	}
	run.State, run.Updated, run.Finished = StateCompleted, j.now(), j.now()
	run.Maintenance, run.Reason, run.Error = MaintenanceCleared, "", ""
	if _, err := j.update(ctx, *run, rev); err != nil {
		return Result{}, err
	}
	j.record(ctx, audit.RestoreCompleted(req.Actor, run.BackupID, j.audit(mods, "")))
	j.log.InfoContext(ctx, "a restore completed and verified; maintenance is lifted", slog.String("id", run.ID), slog.String("backup", run.BackupID))
	return Result{Outcome: OutcomeCompleted, ID: run.ID, Run: run}, nil
}

func (j *Job) paused(ctx context.Context, run *Run, rev port.Revision, reason string, cause error) (Result, error) {
	// ctx may be done; the record is written all the same.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	run.State, run.Updated, run.Reason, run.Error = StatePaused, j.now(), reason, ""
	if cause != nil {
		run.Error = oneLine(cause)
	}
	if _, err := j.update(wctx, *run, rev); err != nil {
		return Result{}, err
	}
	j.log.InfoContext(ctx, "a restore paused; the next call continues it and maintenance stays set", slog.String("id", run.ID), slog.String("reason", reason))
	return Result{Outcome: OutcomePaused, ID: run.ID, Run: run}, nil
}

// failed closes the run as failed, with maintenance left as it is.
func (j *Job) failed(ctx context.Context, req Request, run *Run, rev port.Revision, mods int, reason string, cause error) (Result, error) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := j.fail(wctx, run, rev, reason, cause, req.Actor, mods); err != nil {
		return Result{}, errors.Join(cause, err)
	}
	return Result{Outcome: OutcomeFailed, ID: run.ID, Run: run}, nil
}

func (j *Job) fail(ctx context.Context, run *Run, rev port.Revision, reason string, cause error, actor audit.Actor, mods int) error {
	run.State, run.Updated, run.Finished = StateFailed, j.now(), j.now()
	run.Reason, run.Error = reason, oneLine(cause)
	if _, err := j.update(ctx, *run, rev); err != nil {
		return err
	}
	j.record(ctx, audit.RestoreFailed(actor, run.BackupID, j.audit(mods, reason), run.Error))
	j.log.ErrorContext(ctx, "a restore failed; the modules stay under maintenance", slog.String("id", run.ID),
		slog.String("reason", reason), logattr.SafeError("error", cause))
	return nil
}

func (j *Job) audit(mods int, reason string) audit.Restore {
	return audit.Restore{Installation: j.c.Installation, Modules: mods, Reason: reason}
}

func (j *Job) record(ctx context.Context, r *record.Record) {
	if j.c.Audit != nil {
		j.c.Audit.Record(ctx, r)
	}
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

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxNote {
		s = s[:maxNote]
	}
	return s
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
		return "", fmt.Errorf("restore %s: the run's record moved under it, so another runner has taken over: %w", run.ID, rails.ErrLost)
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
			return nil, fmt.Errorf("list the restores: %w", err)
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

// unfinished is the newest running or paused restore. An older one is closed
// as superseded the next time one starts.
func (j *Job) unfinished(ctx context.Context) (*recorded, error) {
	rs, err := j.records(ctx)
	if err != nil {
		return nil, err
	}
	for i := len(rs) - 1; i >= 0; i-- {
		if s := rs[i].run.State; s == StateRunning || s == StatePaused {
			return rs[i], nil
		}
	}
	return nil, nil
}

// ModuleFlag is the maintenance flag of one module, as `restore.status` shows it.
type ModuleFlag struct {
	Module port.Module `json:"module"`
	State  string      `json:"state,omitempty"`
	Since  time.Time   `json:"since,omitzero"`
	By     string      `json:"by,omitempty"`
	// Unreadable is a flag that could not be read.
	Unreadable bool `json:"unreadable,omitempty"`
}

// Status is what `restore.status` answers.
type Status struct {
	// Latest is the newest restore, whatever its state.
	Latest *Run `json:"latest,omitempty"`
	// Unfinished is a restore that is running or paused.
	Unfinished *Run `json:"unfinished,omitempty"`
	// LastCompleted is the newest completed restore.
	LastCompleted *Run `json:"lastCompleted,omitempty"`
	// Maintenance lists the modules whose flag is set, or cannot be read.
	Maintenance []ModuleFlag `json:"maintenance,omitempty"`
}

// Status reads the latest records and the flag of every module's table.
func (j *Job) Status(ctx context.Context) (Status, error) {
	rs, err := j.records(ctx)
	if err != nil {
		return Status{}, err
	}
	var s Status
	for i := len(rs) - 1; i >= 0; i-- {
		v := rs[i].run
		if s.Latest == nil {
			s.Latest = &v
		}
		switch v.State {
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
	for _, m := range j.c.Modules {
		rec, err := j.c.Flags(m).Get(ctx, maintenance.Key)
		switch {
		case errors.Is(err, port.ErrNotFound):
		case err != nil:
			s.Maintenance = append(s.Maintenance, ModuleFlag{Module: m, Unreadable: true})
		default:
			var f maintenance.Flag
			if json.Unmarshal(rec.Value, &f) != nil {
				s.Maintenance = append(s.Maintenance, ModuleFlag{Module: m, Unreadable: true})
			} else if f.Active() {
				s.Maintenance = append(s.Maintenance, ModuleFlag{Module: m, State: f.State, Since: f.Since, By: f.By})
			}
		}
	}
	return s, nil
}
