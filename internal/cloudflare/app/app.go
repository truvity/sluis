// Package app assembles the Cloudflare module as a process of its own
// (docs/decisions/0071): the minter of ADR 0070 and its scheduled rotation,
// with the secrets store, the leases and the audit trail they need, and
// nothing of the issuer, the directory hub or another provider.
//
// The on-demand exchange (a caller asks the issuer for a token) still calls the
// minter inside the issuer's process; that path is not here.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare/cfapi"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/version"
)

// Interval is how often the loop looks at the presets. A look at a credential
// that is not due costs one read of the secrets store and nothing at
// Cloudflare.
const Interval = time.Minute

// Config is what the module is made of, built from the service document.
type Config struct {
	stores     store.Config
	audit      audit.Config
	cloudflare *config.Cloudflare
	grants     *config.PolicyCloudflare
	instance   string
	logLevel   slog.Level
	// dial opens a Cloudflare account; nil is the real client.
	dial minter.Dialer
}

// LogLevel is the level the process should log at.
func (c Config) LogLevel() slog.Level { return c.logLevel }

// Load reads the service document, and the policy document it names if any,
// and builds the module's settings. A document with no `cloudflare` presets is
// refused: there is nothing for this module to do.
func Load(file string) (Config, error) {
	svc, err := config.Load[config.Sluis](file)
	if err != nil {
		return Config{}, err
	}
	p, err := config.PolicyOf(svc, nil)
	if err != nil {
		return Config{}, err
	}
	cfg, err := FromService(&svc.Serve, p)
	if err != nil {
		return Config{}, err
	}
	// Opening connects to nothing: a secret is read when it is used.
	if cfg.stores.Secrets, err = secrets.Open(context.Background(), &svc.Serve); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// FromService builds the settings from the documents already read.
func FromService(f *config.Serve, p *config.PolicyDocument) (Config, error) {
	if f.Cloudflare == nil || len(f.Cloudflare.Presets) == 0 {
		return Config{}, errors.New("cloudflare.presets: the document declares no preset, so there is nothing for this module to do")
	}
	if err := config.CheckCloudflare(f.Cloudflare, p); err != nil {
		return Config{}, err
	}
	stores, err := store.FromServe(f)
	if err != nil {
		return Config{}, err
	}
	c := Config{stores: stores, cloudflare: f.Cloudflare, grants: p.Cloudflare(), instance: f.Instance,
		audit: audit.Config{Version: version.String()}}
	if c.instance == "" {
		c.instance = f.Release
	}
	if c.instance == "" {
		c.instance = "sluis"
	}
	// The audit instance is the pod, which is its hostname in a cluster.
	c.audit.Instance, _ = os.Hostname()
	if a := f.Audit; a != nil {
		c.audit.Writer = a.Writer
		c.audit.TokenFile = a.TokenFile
	}
	level := "info"
	if f.Log != nil && f.Log.Level != "" {
		level = f.Log.Level
	}
	if err = c.logLevel.UnmarshalText([]byte(level)); err != nil {
		return Config{}, fmt.Errorf("log.level: %w", err)
	}
	return c, nil
}

// App is the assembled module.
type App struct {
	minter *minter.Minter
	log    *slog.Logger
	stores *store.Stores
	trail  *audit.Trail
	// sharedLease is whether the tick leases are held in a State other replicas
	// see.
	sharedLease bool
}

// New assembles the module: the ports, the audit trail, the minter.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	stores, err := store.Open(ctx, cfg.stores, log)
	if err != nil {
		return nil, err
	}
	if stores.V4 == nil {
		stores.Close()
		return nil, errors.New("cloudflare: the minter credential and the stored credentials live in the layout-v4 secrets store, so `secrets.source: ssm` is required")
	}
	// A refusal found after the start is fatal the way one at the start is, in
	// the controllers: here it is logged, and the next record says it again.
	cfg.audit.Log = log
	if cfg.audit, err = audit.FromPlan(cfg.audit, stores.Plan); err != nil {
		stores.Close()
		return nil, err
	}
	trail, err := audit.Open(ctx, cfg.audit)
	if err != nil {
		stores.Close()
		return nil, err
	}
	dial := cfg.dial
	if dial == nil {
		dial = cfapi.Dial()
	}
	state, shared := stores.LeaseState()
	m, err := minter.New(minter.Config{
		Instance: cfg.instance, Cloudflare: cfg.cloudflare, Grants: cfg.grants,
		Internal: stores.V4.Internal, External: stores.V4.External,
		Dial: dial, Audit: trail, Log: log,
		Lock: &rails.Leases{State: state, Holder: rails.NewHolder(), Log: log},
	})
	if err != nil {
		_ = trail.Close()
		stores.Close()
		return nil, err
	}
	if !shared {
		log.InfoContext(ctx, "no shared state backs the tick leases, so they are held in this process: run one replica",
			slog.String("adapter", stores.Adapter))
	}
	return &App{minter: m, log: log, stores: stores, trail: trail, sharedLease: shared}, nil
}

// Close closes the audit emitter, which delivers what its queue holds within its
// timeout and drops the rest, and the ports.
func (a *App) Close() error {
	err := a.trail.Close()
	a.stores.Close()
	return err
}

// Run rotates the presets that are due, every [Interval], until ctx ends. It
// first reports the prototypes that are not usable; that is not fatal, since
// Cloudflare being unreachable must not stop the loop and a mint refuses the
// same prototype every time.
func (a *App) Run(ctx context.Context) error {
	for preset, err := range a.minter.Check(ctx) {
		a.log.WarnContext(ctx, "a Cloudflare preset's prototype is not usable", slog.String("preset", preset), slog.Any("error", err))
	}
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		if res := a.minter.Tick(ctx); res.Failed() > 0 {
			a.log.WarnContext(ctx, "some Cloudflare presets did not rotate; the next tick retries", slog.Int("failed", res.Failed()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick runs one preset's rotation once, under its lease, and returns: a
// credential that is not due costs no call to Cloudflare, and a preset another
// runner holds is left to it.
func (a *App) Tick(ctx context.Context, target string, unsafeLocal bool) error {
	if err := store.RequireSharedLease(a.sharedLease, unsafeLocal); err != nil {
		return err
	}
	return a.tick(ctx, target)
}

func (a *App) tick(ctx context.Context, preset string) error {
	res := a.minter.TickPreset(ctx, preset)
	a.log.InfoContext(ctx, "a Cloudflare preset was ticked", slog.String("preset", preset), slog.String("outcome", res.Outcome),
		slog.Int("swept", len(res.Swept)))
	return res.Err
}
