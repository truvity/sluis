package rpc

import (
	"context"

	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/modcall"
)

// The restore function's module and method. It is the same zip as the backup
// module's, deployed a second time (docs/decisions/0072), and answers one call:
// the console and the administrator's tools read how the restore stands. Nothing
// here starts, resumes or previews a restore: that is an invocation of the
// function itself, which only an administrator or the break-glass role may make
// (internal/lambdaapp).
const (
	RestoreModule       = "restore"
	MethodRestoreStatus = "status"
)

// RestoreService is what the restore methods call. *App of
// internal/backup/app is one.
type RestoreService interface {
	RestoreStatus(ctx context.Context) (restorejob.Status, error)
}

// RegisterRestore makes svc answer `restore.status` on s: the console, an
// administrator and the break-glass role may read it, and no caller may do more.
func RegisterRestore(s *modcall.Server, svc RestoreService) {
	modcall.Handle(s, MethodRestoreStatus, func(ctx context.Context, _ StatusRequest) (restorejob.Status, error) {
		return svc.RestoreStatus(ctx)
	}, modcall.Allow(CallerConsole, CallerAdmin, CallerBreakglass))
}
