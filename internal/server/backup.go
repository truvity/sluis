package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/backup/rpc"
	"github.com/truvity/sluis/storage/logattr"
)

// BackupModule is the backup module and the restore function as the console
// calls them (*rpc.Client of internal/backup/rpc, which calls them as the
// caller class `console`). It reads, previews a restore and starts a backup for
// a named person; it starts and resumes no restore, and the modules would
// refuse it.
type BackupModule interface {
	Status(ctx context.Context) (job.Status, error)
	List(ctx context.Context, limit int) ([]job.Info, error)
	RestoreStatus(ctx context.Context) (restorejob.Status, error)
	StartRun(ctx context.Context, by string) error
	PreviewRestore(ctx context.Context, backupID string) (rpc.PreviewResponse, error)
}

// BackupRunners says whether a holder of groups may start a backup: the
// policy's `backup.runGroups` ([config.PolicyBackup] is one).
type BackupRunners interface {
	MayRun(groups []string) bool
}

// UseBackupRunners sets who may start a backup. Without it nobody may.
func (s *ConsoleServer) UseBackupRunners(r BackupRunners) {
	s.console.deps.BackupRunners = r
}

// UseBackup connects the Backups page to the modules, with the same timing as
// [ConsoleServer.UseCloudflare]. Without it the console has no Backups page.
func (s *ConsoleServer) UseBackup(m BackupModule) {
	s.console.deps.Backup = m
}

// backupCallTimeout bounds one call to a module: the page makes three, and a
// slow cold start of one must not hold the others.
const backupCallTimeout = 20 * time.Second

// backupRunTimeout bounds a backup run and a restore preview, each of which is
// one synchronous call that Lambda stops at 15 minutes.
const backupRunTimeout = 16 * time.Minute

// backupError is a module that could not answer. The message is the transport's
// and holds no record value.
func backupError(what string, err error) error {
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s: %s", what, logattr.Error(err)))
}

// GetBackupStatus implements the administrator's contract.
func (c *Console) GetBackupStatus(
	ctx context.Context, _ *connect.Request[directoryrosterv1.GetBackupStatusRequest],
) (*connect.Response[directoryrosterv1.GetBackupStatusResponse], error) {
	if _, err := requireRole(ctx, access.RoleViewer); err != nil {
		return nil, err
	}
	if c.deps.Backup == nil {
		return connect.NewResponse(&directoryrosterv1.GetBackupStatusResponse{}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, backupCallTimeout)
	defer cancel()
	st, err := c.deps.Backup.Status(ctx)
	if err != nil {
		return nil, backupError("the backup module did not answer", err)
	}
	out := &directoryrosterv1.GetBackupStatusResponse{
		Available: true, Latest: backupRun(st.Latest), LastCompleted: backupRun(st.LastCompleted), Unfinished: backupRun(st.Unfinished),
	}
	if p := st.Retention; p != nil {
		out.Retention = &directoryrosterv1.BackupRetention{
			At: stamp(p.At), Keep: int32(p.Keep), MaxAge: p.MaxAge, Kept: int32(p.Kept),
			Removed: int32(len(p.Removed)), Cleaned: int32(len(p.Orphans)), Refused: int32(len(p.Failed)),
		}
	}
	return connect.NewResponse(out), nil
}

// ListBackups implements the administrator's contract.
func (c *Console) ListBackups(
	ctx context.Context, req *connect.Request[directoryrosterv1.ListBackupsRequest],
) (*connect.Response[directoryrosterv1.ListBackupsResponse], error) {
	if _, err := requireRole(ctx, access.RoleViewer); err != nil {
		return nil, err
	}
	if req.Msg.GetLimit() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("limit cannot be negative"))
	}
	if c.deps.Backup == nil {
		return connect.NewResponse(&directoryrosterv1.ListBackupsResponse{}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, backupCallTimeout)
	defer cancel()
	all, err := c.deps.Backup.List(ctx, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, backupError("the backup module did not answer", err)
	}
	// The module answers newest first; the order is the page's contract, so it
	// is not left to the module alone.
	slices.SortStableFunc(all, func(a, b job.Info) int { return b.Created.Compare(a.Created) })
	out := &directoryrosterv1.ListBackupsResponse{Available: true}
	for i := range all {
		b := &all[i]
		out.Backups = append(out.Backups, &directoryrosterv1.BackupInfo{
			Id: b.ID, Created: stamp(b.Created), Creator: b.Creator, Format: int32(b.Format), Layout: b.Layout,
			Modules: int32(b.Modules), Records: b.Records, Chunks: int32(b.Chunks), Error: b.Error,
		})
	}
	return connect.NewResponse(out), nil
}

// GetRestoreStatus implements the administrator's contract.
func (c *Console) GetRestoreStatus(
	ctx context.Context, _ *connect.Request[directoryrosterv1.GetRestoreStatusRequest],
) (*connect.Response[directoryrosterv1.GetRestoreStatusResponse], error) {
	if _, err := requireRole(ctx, access.RoleViewer); err != nil {
		return nil, err
	}
	if c.deps.Backup == nil {
		return connect.NewResponse(&directoryrosterv1.GetRestoreStatusResponse{}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, backupCallTimeout)
	defer cancel()
	st, err := c.deps.Backup.RestoreStatus(ctx)
	if err != nil {
		return nil, backupError("the restore function did not answer", err)
	}
	out := &directoryrosterv1.GetRestoreStatusResponse{
		Available: true, Latest: restoreRun(st.Latest), Unfinished: restoreRun(st.Unfinished), LastCompleted: restoreRun(st.LastCompleted),
	}
	for _, m := range st.Maintenance {
		out.Maintenance = append(out.Maintenance, &directoryrosterv1.ModuleMaintenance{
			Module: string(m.Module), State: m.State, Since: stamp(m.Since), By: m.By, Unreadable: m.Unreadable,
		})
	}
	return connect.NewResponse(out), nil
}

// backupRunThrottle is how long a person waits between two backups they start.
// The lease and the module's unfinished run refuse a second run whoever asks;
// this is for the moments before the first one has a record.
const backupRunThrottle = 2 * time.Minute

// The outcomes of RunBackup.
const (
	runStarted   = "started"
	runContended = "contended"
	runIdle      = "idle"
)

// RunBackup implements the runner's contract: a person whose groups the policy
// names starts a backup, and the module records them as who asked. It answers
// when the module has been handed the run, not when the run ends: the caller
// reads the run with GetBackupStatus.
func (c *Console) RunBackup(
	ctx context.Context, req *connect.Request[directoryrosterv1.RunBackupRequest],
) (*connect.Response[directoryrosterv1.RunBackupResponse], error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
	}
	// The group is the gate, and nothing else opens it: not the operator role,
	// which only the groups the policy names are asked to stand in for.
	if c.deps.BackupRunners == nil || !c.deps.BackupRunners.MayRun(id.Groups) {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("starting a backup needs a group that the policy's backup.runGroups names"))
	}
	if c.deps.Backup == nil {
		return connect.NewResponse(&directoryrosterv1.RunBackupResponse{}), nil
	}
	// By is the address, or for a workload the subject: what the audit record
	// names. A caller with neither is refused rather than recorded as nobody.
	by := id.Who()
	if by == "" || id.Source == access.SourceRecovery {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("this sign-in cannot be named in the audit record of a backup"))
	}
	answer := func(outcome string, at time.Time) *connect.Response[directoryrosterv1.RunBackupResponse] {
		return connect.NewResponse(&directoryrosterv1.RunBackupResponse{Available: true, Outcome: outcome, Started: stamp(at)})
	}
	// A run is one invocation since format 2, so a resume has nothing to continue.
	if req.Msg.GetResume() {
		return answer(runIdle, time.Time{}), nil
	}
	now := time.Now()
	if !c.reserveRun(by, now) {
		return answer(runContended, time.Time{}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, backupCallTimeout)
	defer cancel()
	// A run already going holds the lease, and another would only find it held.
	st, err := c.deps.Backup.Status(ctx)
	if err != nil {
		c.releaseRun(by)
		return nil, backupError("the backup module did not answer", err)
	}
	if st.Unfinished != nil && st.Unfinished.State == job.StateRunning {
		return answer(runContended, time.Time{}), nil
	}
	c.log().InfoContext(ctx, "a backup was requested through the console", slog.String("by", logattr.Safe(by)))
	if err = c.deps.Backup.StartRun(ctx, by); err != nil {
		c.releaseRun(by)
		return nil, backupError("the backup module did not take the run", err)
	}
	return answer(runStarted, now), nil
}

// reserveRun records that by is starting a backup at now, unless they started
// one within [backupRunThrottle]. Per process: the lease is the guard across
// replicas.
func (c *Console) reserveRun(by string, now time.Time) bool {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if last, ok := c.runBy[by]; ok && now.Sub(last) < backupRunThrottle {
		return false
	}
	if c.runBy == nil {
		c.runBy = map[string]time.Time{}
	}
	for who, at := range c.runBy {
		if now.Sub(at) >= backupRunThrottle {
			delete(c.runBy, who)
		}
	}
	c.runBy[by] = now
	return true
}

func (c *Console) releaseRun(by string) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	delete(c.runBy, by)
}

// GetRestorePreview implements the operator's contract: counts per section, and
// no record's name or value.
func (c *Console) GetRestorePreview(
	ctx context.Context, req *connect.Request[directoryrosterv1.GetRestorePreviewRequest],
) (*connect.Response[directoryrosterv1.GetRestorePreviewResponse], error) {
	if _, err := requireRole(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	// An id names an object under the installation's archive prefix: one path
	// segment of letters, digits, dot, dash and underscore, never a path.
	if !backup.ValidName(req.Msg.GetBackupId()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("backup_id is not the id of a backup"))
	}
	if c.deps.Backup == nil {
		return connect.NewResponse(&directoryrosterv1.GetRestorePreviewResponse{}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, backupRunTimeout)
	defer cancel()
	res, err := c.deps.Backup.PreviewRestore(ctx, req.Msg.GetBackupId())
	if err != nil {
		return nil, backupError("the restore function did not answer", err)
	}
	out := &directoryrosterv1.GetRestorePreviewResponse{
		Available: true, Outcome: res.Outcome, Error: res.Error, BackupId: res.Backup,
	}
	if r := res.Report; r != nil {
		out.Installation, out.Layout, out.Created = r.Installation, string(r.Layout), r.Created
		out.BackupId = r.ID
		for _, s := range r.Sections {
			out.Sections = append(out.Sections, &directoryrosterv1.RestorePreviewSection{
				Module: string(s.Module), Section: string(s.Section), Create: int32(s.Create), Overwrite: int32(s.Overwrite),
				Same: int32(s.Same), Expired: int32(s.Expired), Regenerated: int32(s.Regenerated),
			})
		}
	}
	return connect.NewResponse(out), nil
}

func backupRun(v *job.View) *directoryrosterv1.BackupRun {
	if v == nil {
		return nil
	}
	return &directoryrosterv1.BackupRun{
		Id: v.ID, State: v.State, Trigger: v.Trigger, Creator: v.Creator,
		Started: stamp(v.Started), Updated: stamp(v.Updated), Finished: stamp(v.Finished),
		Modules: int32(v.Modules), Records: v.Records, Chunks: int32(v.Chunks), Resumed: v.Resumed,
		Reason: v.Reason, Error: v.Error, Units: int32(v.Units), Done: int32(v.Done),
	}
}

func restoreRun(r *restorejob.Run) *directoryrosterv1.RestoreRun {
	if r == nil {
		return nil
	}
	return &directoryrosterv1.RestoreRun{
		Id: r.ID, BackupId: r.BackupID, State: r.State, Overwrite: r.Overwrite, By: r.By, Note: r.Note,
		Started: stamp(r.Started), Updated: stamp(r.Updated), Finished: stamp(r.Finished),
		Maintenance: r.Maintenance, Slices: int32(r.Slices),
		Create: int32(r.Create), Overwritten: int32(r.Overwritten), Same: int32(r.Same), Reason: r.Reason, Error: r.Error,
	}
}
