// Package backup is the backup role: a scheduled export of the installation
// into an encrypted archive (docs/decisions/0071, decision 9), as a process of
// its own. It has no loop and no listener. `sluis backup run` is the Kubernetes
// CronJob's command and the operator's; on Lambda the function takes the
// schedule's {"kind":"backup"} event (internal/lambdaapp).
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup/app"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/module"
)

// The targets of a tick.
const (
	// TargetRun starts a backup, or continues a paused one.
	TargetRun = "run"
	// TargetResume continues a paused backup and starts none.
	TargetResume = "resume"
	// TargetPrune applies the retention rule.
	TargetPrune = "prune"
)

// Module is the backup role. Log receives the process's log lines; os.Stderr
// when nil, so that stdout stays what a command prints.
type Module struct{ Log io.Writer }

var _ module.Module = Module{}

// Name implements [module.Module].
func (Module) Name() string { return "backup" }

// Open assembles the module from file. The returned function releases it and
// flushes the telemetry.
func (m Module) Open(ctx context.Context, file string) (*app.App, *slog.Logger, func(), error) {
	cfg, err := app.Load(file)
	if err != nil {
		return nil, nil, nil, err
	}
	w := m.Log
	if w == nil {
		w = os.Stderr
	}
	log, flush, err := module.Logger(ctx, w, "sluis-backup", cfg.LogLevel())
	if err != nil {
		return nil, nil, nil, err
	}
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		flush()
		return nil, nil, nil, err
	}
	return a, log, func() {
		module.CloseEmitter(log, closer{a})
		flush()
	}, nil
}

// closer adapts Close to the emitter closer.
type closer struct{ a *app.App }

func (c closer) Close() error { return c.a.Close() }

// Run implements [module.Module]: one backup, then it returns. A scheduler
// calls it again; there is nothing to serve.
func (m Module) Run(ctx context.Context, file string) error {
	return m.Tick(ctx, file, TargetRun, false)
}

// Tick implements [module.Module]: the target is [TargetRun], [TargetResume] or
// [TargetPrune]. The leases are in the State all runners share, so unsafeLocal
// changes nothing.
func (m Module) Tick(ctx context.Context, file, target string, _ bool) error {
	a, log, closeApp, err := m.Open(ctx, file)
	if err != nil {
		return err
	}
	defer closeApp()
	switch target {
	case TargetRun, TargetResume:
		res, err := a.Run(ctx, job.Request{Trigger: job.TriggerCLI, Actor: audit.System(), ResumeOnly: target == TargetResume})
		if err != nil {
			return err
		}
		log.InfoContext(ctx, "backup run done", slog.String("outcome", res.Outcome), slog.String("id", res.ID))
		return nil
	case TargetPrune:
		_, outcome, err := a.Prune(ctx, audit.System(), false)
		if err != nil {
			return err
		}
		log.InfoContext(ctx, "backup prune done", slog.String("outcome", outcome))
		return nil
	}
	return fmt.Errorf("%w: the backup targets are %s, %s and %s", errors.ErrUnsupported, TargetRun, TargetResume, TargetPrune)
}
