// Package issuer is the issuer role: today the one process of `sluis serve`,
// which also carries the directory hub, the console and the controllers its
// document names, until those roles are split out.
package issuer

import (
	"context"
	"os"

	"github.com/truvity/sluis/internal/module"
	"github.com/truvity/sluis/internal/rosterapp"
)

// Module is the issuer role.
type Module struct{}

// Name implements [module.Module].
func (Module) Name() string { return "issuer" }

// Run implements [module.Module].
func (Module) Run(ctx context.Context, file string) error {
	cfg, err := rosterapp.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := module.Logger(ctx, os.Stdout, "access-issuer", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()

	service, err := rosterapp.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer service.Close()
	return service.Run(ctx)
}

// Tick implements [module.Module]: the issuer's passes run inside its loop.
func (Module) Tick(context.Context, string, string, bool) error {
	return module.ErrNoTick
}
