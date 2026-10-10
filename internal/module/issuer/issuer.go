// Package issuer is the issuer module (docs/decisions/0072): the issuer, the
// signer (internal/signer, in this process; no separate signer binary exists)
// and the console in one process. It also carries the directory hub and the
// providers' controllers its document names, until each provider is released
// as a process of its own. `sluis serve` is the deprecated name of this
// command.
package issuer

import (
	"context"
	"log/slog"
	"os"

	"github.com/truvity/sluis/internal/module"
	"github.com/truvity/sluis/internal/rosterapp"
)

// Module is the issuer module. Deprecated names the command that runs it under
// an old name, and is said in the log.
type Module struct{ Deprecated string }

// Name implements [module.Module].
func (Module) Name() string { return "issuer" }

// Run implements [module.Module].
func (m Module) Run(ctx context.Context, file string) error {
	cfg, err := rosterapp.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := module.Logger(ctx, os.Stdout, "access-issuer", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()
	if m.Deprecated != "" {
		log.WarnContext(ctx, "`"+m.Deprecated+"` is deprecated and goes in the next release: the command is `sluis issuer`, with the same --config",
			slog.String("command", m.Deprecated))
	}

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
