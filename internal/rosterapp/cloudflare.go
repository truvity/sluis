//nolint:lll // messages and fixtures are prose and one-line tables
package rosterapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare/cfapi"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/store"
)

// cloudflareInterval is how often a process looks at the presets. A look at a
// credential that is not due costs one read of the secrets store and nothing at
// Cloudflare, so a minute is the granularity of a rotation (an EventBridge
// schedule's, on Lambda).
const cloudflareInterval = time.Minute

func newCloudflare(cfg Config, stores *store.Stores, rec audit.Recorder, log *slog.Logger) (*minter.Minter, error) {
	if stores.V4 == nil && stores.V5 == nil {
		return nil, errors.New("cloudflare: the minter credential and the stored credentials live in the SSM secrets store (layout v4 or v5), so `secrets.source: ssm` is required")
	}
	dial := cfg.CloudflareDial
	if dial == nil {
		dial = cfapi.Dial()
	}
	state, _ := stores.LeaseState()
	if stores.Ports.Module != "" {
		// A lease of the minter is the cloudflare module's, in its own table.
		own, err := stores.ForModule(port.ModuleCloudflare)
		if err != nil {
			return nil, fmt.Errorf("cloudflare: %w", err)
		}
		state = own.State
	}
	mc := minter.Config{
		Instance: cfg.Instance, Cloudflare: cfg.Cloudflare, Grants: cfg.Grants,
		Dial: dial, Audit: rec, Log: log,
		Lock: &rails.Leases{State: state, Holder: rails.NewHolder(), Log: log},
	}
	if stores.V5 != nil {
		mc.V5 = stores.V5
	} else {
		mc.Internal, mc.External = stores.V4.Internal, stores.V4.External
	}
	return minter.New(mc)
}

// Cloudflare is the minter of Cloudflare credentials, or nil when the service
// document has no `cloudflare` section. The on-demand exchange, sluisctl and
// the console call it.
func (a *App) Cloudflare() *minter.Minter { return a.cloudflare }

// TickCloudflare runs the schedule over every preset once. A function with no
// loop runs it on a schedule ({"kind":"cloudflare"}).
func (a *App) TickCloudflare(ctx context.Context) (minter.TickResult, error) {
	if a.cloudflare == nil {
		return minter.TickResult{}, errors.New("this service has no cloudflare section")
	}
	res := a.cloudflare.Tick(ctx)
	return res, res.Err()
}

// runCloudflare ticks the presets until ctx ends.
func (a *App) runCloudflare(ctx context.Context) {
	// Once at start, so an active or forbidden prototype is in the log as the
	// service starts. Not fatal: Cloudflare being unreachable must not stop
	// sign-in, and the mint refuses the same prototype every time. A function
	// does not do this at each cold start.
	for preset, err := range a.cloudflare.Check(ctx) {
		a.log.WarnContext(ctx, "a Cloudflare preset's prototype is not usable", slog.String("preset", preset), slog.Any("error", err))
	}
	t := time.NewTicker(cloudflareInterval)
	defer t.Stop()
	for {
		if res := a.cloudflare.Tick(ctx); res.Failed() > 0 {
			a.log.WarnContext(ctx, "some Cloudflare presets did not rotate; the next tick retries", slog.Int("failed", res.Failed()))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
