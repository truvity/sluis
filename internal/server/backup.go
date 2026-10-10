package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/storage/logattr"
)

// BackupModule is the backup module and the restore function as the console
// reads them (*rpc.Client of internal/backup/rpc, which calls them as the
// caller class `console`). It only reads: the console starts neither a backup
// nor a restore, and the modules would refuse it.
type BackupModule interface {
	Status(ctx context.Context) (job.Status, error)
	List(ctx context.Context, limit int) ([]job.Info, error)
	RestoreStatus(ctx context.Context) (restorejob.Status, error)
}

// UseBackup connects the Backups page to the modules, with the same timing as
// [ConsoleServer.UseCloudflare]. Without it the console has no Backups page.
func (s *ConsoleServer) UseBackup(m BackupModule) {
	s.console.deps.Backup = m
}

// backupCallTimeout bounds one call to a module: the page makes three, and a
// slow cold start of one must not hold the others.
const backupCallTimeout = 20 * time.Second

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
	for _, b := range all {
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
