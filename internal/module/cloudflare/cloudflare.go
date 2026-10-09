// Package cloudflare is the Cloudflare role: the STS minter's scheduled
// rotation (docs/decisions/0070), as a process of its own.
package cloudflare

import (
	"context"
	"os"

	app "github.com/truvity/sluis/internal/cloudflare/app"
	"github.com/truvity/sluis/internal/module"
)

// Module is the Cloudflare role.
type Module struct{}

// Name implements [module.Module].
func (Module) Name() string { return "cloudflare" }

// Run implements [module.Module]: the rotation loop, one look a minute.
func (Module) Run(ctx context.Context, file string) error {
	a, flush, err := assemble(ctx, file)
	if err != nil {
		return err
	}
	defer flush()
	return a.Run(ctx)
}

// Tick implements [module.Module]: one preset, once, under its lease.
func (Module) Tick(ctx context.Context, file, target string, unsafeLocal bool) error {
	a, flush, err := assemble(ctx, file)
	if err != nil {
		return err
	}
	defer flush()
	return a.Tick(ctx, target, unsafeLocal)
}

// assemble loads the file and builds the module; the returned function closes
// it and flushes telemetry.
func assemble(ctx context.Context, file string) (*app.App, func(), error) {
	cfg, err := app.Load(file)
	if err != nil {
		return nil, nil, err
	}
	log, flush, err := module.Logger(ctx, os.Stdout, "cloudflare-minter", cfg.LogLevel())
	if err != nil {
		return nil, nil, err
	}
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		flush()
		return nil, nil, err
	}
	return a, func() {
		module.CloseEmitter(log, a)
		flush()
	}, nil
}
