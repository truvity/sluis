// Package rpc is the backup module as another module sees it
// (docs/decisions/0071): the status of the backups, the list of what the
// archive holds, and a backup now, as methods of the `backup` module over
// internal/modcall. The restore function's methods are its own (U26).
package rpc

import (
	"context"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/modcall"
)

// Module is the name of the module and the methods it answers.
const (
	Module       = "backup"
	MethodStatus = "status"
	MethodList   = "list"
	MethodRun    = "run"
)

// Caller classes (the alias a call arrives through, `live-<class>`): the console
// shows the status and the list; an administrator or the break-glass role also
// starts a backup. The console has no button for a restore, and none to start
// a backup.
const (
	CallerConsole    = "console"
	CallerAdmin      = "admin"
	CallerBreakglass = "breakglass"
)

// Service is what the methods call. *App of internal/backup/app is one.
type Service interface {
	Status(ctx context.Context) (job.Status, error)
	List(ctx context.Context) ([]job.Info, error)
	Run(ctx context.Context, req job.Request) (job.Result, error)
}

// StatusRequest has nothing in it.
type StatusRequest struct{}

// ListRequest asks for the newest Limit backups; 0 is all.
type ListRequest struct {
	Limit int `json:"limit,omitempty"`
}

// ListResponse is `backup.list`.
type ListResponse struct {
	Backups []job.Info `json:"backups"`
}

// RunRequest starts a backup, or with Resume only continues a paused one.
type RunRequest struct {
	Resume bool `json:"resume,omitempty"`
}

// Register makes svc answer the module's methods on s.
func Register(s *modcall.Server, svc Service) {
	modcall.Handle(s, MethodStatus, func(ctx context.Context, _ StatusRequest) (job.Status, error) {
		return svc.Status(ctx)
	}, modcall.Allow(CallerConsole, CallerAdmin, CallerBreakglass))
	modcall.Handle(s, MethodList, func(ctx context.Context, r ListRequest) (ListResponse, error) {
		all, err := svc.List(ctx)
		if err != nil {
			return ListResponse{}, err
		}
		if r.Limit > 0 && len(all) > r.Limit {
			all = all[:r.Limit]
		}
		return ListResponse{Backups: all}, nil
	}, modcall.Allow(CallerConsole, CallerAdmin, CallerBreakglass))
	modcall.Handle(s, MethodRun, func(ctx context.Context, r RunRequest) (job.Result, error) {
		// The caller class is the transport's to say; the actor of the record is
		// the workload that class is, since an alias is not a person.
		who := audit.Workload("lambda:" + modcall.CallerOf(ctx))
		return svc.Run(ctx, job.Request{Trigger: job.TriggerRun, Actor: who, ResumeOnly: r.Resume})
	}, modcall.Allow(CallerAdmin, CallerBreakglass))
}
