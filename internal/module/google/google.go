// Package google is the Google role: the hub's refresh writer, as a process of its own.
package google

import (
	"context"
	"os"

	app "github.com/truvity/sluis/internal/googleroster/app"
	"github.com/truvity/sluis/internal/module"
)

// Module is the Google role.
type Module struct{}

// Name implements [module.Module].
func (Module) Name() string { return "google" }

// Run implements [module.Module]: the refresher and the prober, until ctx ends.
func (Module) Run(ctx context.Context, file string) error {
	cfg, err := app.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := module.Logger(ctx, os.Stdout, "google-roster", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	return a.Run(ctx)
}

// Tick implements [module.Module]: one workspace, once, under its lease.
func (Module) Tick(ctx context.Context, file, target string, unsafeLocal bool) error {
	cfg, err := app.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := module.Logger(ctx, os.Stdout, "google-roster", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	return a.Tick(ctx, target, unsafeLocal)
}
