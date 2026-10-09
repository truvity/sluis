// Package github is the GitHub role: the GitHub controller's loop, as a process of its own.
package github

import (
	"context"
	"log/slog"
	"os"

	app "github.com/truvity/sluis/internal/githubroster/app"
	"github.com/truvity/sluis/internal/module"
)

// Module is the GitHub role. Deprecated names the command that runs the loop
// under its old name and says so at start; empty for `sluis github`.
type Module struct{ Deprecated string }

// Name implements [module.Module].
func (Module) Name() string { return "github" }

// Run implements [module.Module].
func (m Module) Run(ctx context.Context, file string) error {
	cfg, err := app.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := module.Logger(ctx, os.Stdout, "github-roster", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()
	if m.Deprecated != "" {
		log.WarnContext(ctx, "running a controller as a process of its own is deprecated and goes in the next release: "+
			"`sluis serve` runs the controllers its document names under `controllers` (apiVersion sluis.truvity.github.io/sluis/v3)",
			slog.String("command", m.Deprecated))
	}

	controller, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer module.CloseEmitter(log, controller)
	return controller.Run(ctx)
}

// Tick implements [module.Module]: one GitHub target, once, under its lease.
func (Module) Tick(ctx context.Context, file, target string, unsafeLocal bool) error {
	cfg, err := app.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := module.Logger(ctx, os.Stdout, "github-roster", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()

	controller, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer module.CloseEmitter(log, controller)
	return controller.Tick(ctx, target, unsafeLocal)
}
