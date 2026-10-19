// Package issuer is the issuer module (docs/decisions/0072): the issuer, the
// signer (internal/signer, in this process; no separate signer binary exists)
// and the console in one process. It also carries the directory hub and the
// providers' controllers its document names, until each provider is released
// as a process of its own.
package issuer

import (
	"context"
	"os"

	"github.com/truvity/sluis/internal/module"
	"github.com/truvity/sluis/internal/rosterapp"
)

// Module is the issuer module.
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
