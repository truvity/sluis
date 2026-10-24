// Package outline is the Outline role: the Outline controller's loop and the
// console's Outline calls, as a process of its own (docs/decisions/0077).
package outline

import (
	"context"
	"os"

	"golang.org/x/sync/errgroup"

	"github.com/truvity/sluis/internal/modserve"
	"github.com/truvity/sluis/internal/module"
	app "github.com/truvity/sluis/internal/outlineroster/app"
)

// Module is the Outline role.
type Module struct{}

// Name implements [module.Module].
func (Module) Name() string { return "outline" }

// Run implements [module.Module]: the controller's loop and, with
// `outline.serve`, the listener the console calls, behind a TokenReview.
// Either ending stops the process, which the platform restarts.
func (Module) Run(ctx context.Context, file string) error {
	cfg, err := app.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := module.Logger(ctx, os.Stdout, "outline-controller", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer module.CloseEmitter(log, a)
	if cfg.Serve() == nil {
		return a.Run(ctx)
	}
	verify, err := modserve.NewVerifier(modserve.Audience("outline", cfg.Serve()))
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return a.Run(gctx) })
	g.Go(func() error { return modserve.Serve(gctx, cfg.Serve(), a.RPC(), verify, log) })
	return g.Wait()
}

// Tick implements [module.Module]: one Outline workspace, once, under its lease.
func (Module) Tick(ctx context.Context, file, target string, unsafeLocal bool) error {
	cfg, err := app.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := module.Logger(ctx, os.Stdout, "outline-controller", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer module.CloseEmitter(log, a)
	return a.Tick(ctx, target, unsafeLocal)
}
