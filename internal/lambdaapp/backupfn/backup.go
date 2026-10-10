// Package backupfn is the backup module's Lambda function, and the restore
// function of the same zip (`backup.role: restore`). It registers itself with
// internal/lambdaapp; cmd/sluis-backup and the lambda build of cmd/sluis import it.
package backupfn

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/truvity/sluis/internal/audit"
	backupapp "github.com/truvity/sluis/internal/backup/app"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/lambdaapp"
)

func init() { lambdaapp.Register(lambdaapp.ModuleBackup, openBackup) }

// openBackup assembles the function that runs the backup module: it takes the
// schedule's {"kind":"backup"} events and other modules' calls
// (internal/backup/rpc), and has no HTTP surface. With `backup.role: restore` it
// is the restore function instead, which takes {"kind":"restore"} events. The issuer is not assembled
// here, so this function can never answer as it.
func openBackup(ctx context.Context, file string) (*lambdaapp.Function, error) {
	cfg, err := backupapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := lambdaapp.Logger(ctx, "sluis-backup", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	a, err := backupapp.New(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	notFound := http.NotFoundHandler()
	h := lambdaapp.NewHTTP(notFound, nil, log).WithRPC(a.RPC())
	if cfg.Role() == config.BackupRoleRestore {
		// The restore function answers the restore event and `restore.status`,
		// and nothing of the backup's.
		api, err := openLambdaAPI(ctx)
		if err != nil {
			_ = a.Close()
			return nil, err
		}
		h.WithRestore(func(ctx context.Context, ev lambdaapp.RestoreEvent) (lambdaapp.RestoreResult, error) {
			return restoreEvent(ctx, a, api, log, ev)
		})
	} else {
		h.WithBackup(func(ctx context.Context, resume bool) (lambdaapp.BackupResult, error) {
			res, err := a.Run(ctx, job.Request{Trigger: job.TriggerSchedule, Actor: audit.System(), ResumeOnly: resume})
			out := lambdaapp.BackupResult{Outcome: res.Outcome, ID: res.ID}
			if res.Run != nil {
				out.Done, out.Units = res.Run.Done, res.Run.Units
			}
			return out, err
		})
	}
	return &lambdaapp.Function{
		Handler: h,
		Flush: func(ctx context.Context) {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := a.Flush(fctx); err != nil {
				log.WarnContext(ctx, "audit records were not delivered before the response", slog.Any("error", err))
			}
			flush(ctx)
		},
		Close: func() { _ = a.Close() },
	}, nil
}
