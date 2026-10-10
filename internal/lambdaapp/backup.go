package lambdaapp

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/truvity/sluis/internal/audit"
	backupapp "github.com/truvity/sluis/internal/backup/app"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/config"
)

// openBackup assembles the function that runs the backup module: it takes the
// schedule's {"kind":"backup"} events and other modules' calls
// (internal/backup/rpc), and has no HTTP surface. With `backup.role: restore` it
// is the restore function instead, which takes {"kind":"restore"} events. The issuer is not assembled
// here, so this function can never answer as it.
func openBackup(ctx context.Context, file string) (*Function, error) {
	cfg, err := backupapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := logger(ctx, "sluis-backup", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	a, err := backupapp.New(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	notFound := http.NotFoundHandler()
	h := NewHTTP(notFound, nil, log).WithRPC(a.RPC())
	if cfg.Role() == config.BackupRoleRestore {
		// The restore function answers the restore event and `restore.status`,
		// and nothing of the backup's.
		api, err := openLambdaAPI(ctx)
		if err != nil {
			_ = a.Close()
			return nil, err
		}
		h.WithRestore(func(ctx context.Context, ev RestoreEvent) (RestoreResult, error) {
			return restoreEvent(ctx, a, api, log, ev)
		})
	} else {
		h.WithBackup(func(ctx context.Context, resume bool) (BackupResult, error) {
			res, err := a.Run(ctx, job.Request{Trigger: job.TriggerSchedule, Actor: audit.System(), ResumeOnly: resume})
			out := BackupResult{Outcome: res.Outcome, ID: res.ID}
			if res.Run != nil {
				out.Done, out.Units = res.Run.Done, res.Run.Units
			}
			return out, err
		})
	}
	return &Function{
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
