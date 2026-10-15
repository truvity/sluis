package rpc

import (
	"context"

	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/modcall"
)

// Client is the backup and restore modules as the console calls them: through a
// [modcall.Caller] whose transport says the caller class is `console`. It reads,
// previews a restore and starts a backup for a named person; it has no method
// that starts or resumes a restore, because the modules do not let that class.
type Client struct{ c modcall.Caller }

// NewClient calls the modules through c.
func NewClient(c modcall.Caller) *Client { return &Client{c: c} }

// Status is `backup.status`.
func (c *Client) Status(ctx context.Context) (job.Status, error) {
	return modcall.Do[StatusRequest, job.Status](ctx, c.c, Module, MethodStatus, StatusRequest{})
}

// List is `backup.list`: the newest limit backups, all when 0.
func (c *Client) List(ctx context.Context, limit int) ([]job.Info, error) {
	out, err := modcall.Do[ListRequest, ListResponse](ctx, c.c, Module, MethodList, ListRequest{Limit: limit})
	return out.Backups, err
}

// RestoreStatus is `restore.status`.
func (c *Client) RestoreStatus(ctx context.Context) (restorejob.Status, error) {
	return modcall.Do[StatusRequest, restorejob.Status](ctx, c.c, RestoreModule, MethodRestoreStatus, StatusRequest{})
}

// StartRun starts `backup.run` for the person by, whom the module records as the
// actor, and returns once the module has been handed the call: a backup runs for
// minutes, and its record is read back with [Client.Status].
func (c *Client) StartRun(ctx context.Context, by string) error {
	return modcall.Start(ctx, c.c, Module, MethodRun, RunRequest{By: by})
}

// PreviewRestore is `restore.preview`: what restoring the backup would do, as counts.
func (c *Client) PreviewRestore(ctx context.Context, backupID string) (PreviewResponse, error) {
	return modcall.Do[PreviewRequest, PreviewResponse](ctx, c.c, RestoreModule, MethodRestorePreview, PreviewRequest{Backup: backupID})
}
