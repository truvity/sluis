// Package app assembles the Outline controller from the service document's
// `controllers.outline`, the policy's outline table and the process it runs
// in: the controller is hosted by the issuer (docs/decisions/0077), so it
// keeps its records on the issuer's ports, asks the console in that process
// and records to its audit trail.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/truvity/sluis/blobprefix"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/egress"
	"github.com/truvity/sluis/internal/outlineroster/controller"
	"github.com/truvity/sluis/internal/outlineroster/outlineapi"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// Config is what a deployment decides.
type Config struct {
	policy    *config.PolicyDocument
	interval  time.Duration
	shareScan time.Duration
	enabled   map[string]bool
}

// FromService builds the settings from the service document's
// `controllers.outline` and the policy it names.
func FromService(s *config.Sluis, p *config.PolicyDocument) (Config, error) {
	if s.Controllers == nil || s.Controllers.Outline == nil {
		return Config{}, errors.New("controllers.outline is not set")
	}
	if p == nil {
		return Config{}, errors.New("policy.file is required: the bindings are the policy document's outline table")
	}
	o := s.Controllers.Outline
	c := Config{policy: p, interval: controller.DefaultInterval, shareScan: controller.DefaultShareScan, enabled: map[string]bool{}}
	if o.Interval != nil {
		c.interval = o.Interval.D()
	}
	if o.ShareScan != nil {
		c.shareScan = o.ShareScan.D()
	}
	if c.interval <= 0 {
		return Config{}, fmt.Errorf("controllers.outline.interval %q is not a positive duration", c.interval)
	}
	if c.shareScan <= 0 {
		return Config{}, fmt.Errorf("controllers.outline.shareScan %q is not a positive duration", c.shareScan)
	}
	for _, ws := range p.EnabledOutlineWorkspaces() {
		if ws = strings.TrimSpace(ws); ws != "" {
			c.enabled[ws] = true
		}
	}
	for ws := range c.enabled {
		if _, declared := p.Policy.Outline.Workspaces[ws]; !declared {
			return Config{}, fmt.Errorf("controllers.outline.enabledWorkspaces names %s, which the policy does not declare", ws)
		}
	}
	return c, nil
}

// Declares says the policy names the workspace key.
func (c Config) Declares(target string) bool {
	_, declared := c.policy.Policy.Outline.Workspaces[target]
	return declared
}

// Enabled are the workspaces the controller acts in, sorted.
func (c Config) Enabled() []string { return slices.Sorted(maps.Keys(c.enabled)) }

// Deps are what the process the controller runs in gives it.
type Deps struct {
	// Stores are the process's ports, already open: the issuer's.
	Stores *store.Stores
	// Directory is the console in this process.
	Directory controller.Directory
	// Audit is the process's audit trail. Nil records nothing.
	Audit audit.Recorder
	// HTTP calls Outline. Nil keeps private addresses and refuses loopback
	// and link-local ones.
	HTTP *http.Client
}

// App is an assembled controller.
type App struct {
	cfg         Config
	controller  *controller.Controller
	log         *slog.Logger
	sharedLease bool
}

// New assembles the controller over the process's stores.
func New(ctx context.Context, cfg Config, deps Deps, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	declared := cfg.policy.Policy
	set, err := policy.NewSet(declared)
	if err != nil {
		return nil, fmt.Errorf("the policy: %w", err)
	}
	st := deps.Stores
	if st == nil || st.Adapter == store.AdapterLegacy {
		return nil, errors.New("controllers.outline needs a storage adapter other than legacy: the workspaces' connections are on the State port")
	}
	base := portstore.New(st.Ports).WithV5(st.V5)
	if err = base.RequireSecrets(); err != nil {
		return nil, fmt.Errorf("ports.adapter %s: %w", st.Adapter, err)
	}
	leaseState, shared := st.LeaseState()
	web := deps.HTTP
	if web == nil {
		web = &http.Client{Timeout: 30 * time.Second, Transport: egress.Policy{AllowPrivate: true}.Transport()}
	}
	if len(declared.Outline.Workspaces) == 0 {
		log.WarnContext(ctx, "the policy declares no Outline workspace: every pass will do nothing")
	}
	a := &App{cfg: cfg, log: log, sharedLease: shared}
	a.controller = controller.New(controller.Config{Interval: cfg.interval, Enabled: cfg.enabled, ShareScan: cfg.shareScan}, controller.Deps{
		Log: log, Directory: deps.Directory, Audit: deps.Audit,
		Status:  rails.NewBlobReports(st.Ports.Blob, blobprefix.OutlineReports),
		Records: portstore.NewOutlineWorkspaces(base),
		Policy:  declared, Digest: set.Digest(),
		Outline: func(url, key string) *outlineapi.Client {
			return outlineapi.New(url, key, outlineapi.WithHTTPClient(web))
		},
		Leases: &rails.Leases{State: leaseState, Holder: rails.NewHolder(), Log: log, Maintenance: st.Maintenance()},
	})
	log.InfoContext(ctx, "the Outline controller is assembled", slog.Int("workspaces", len(declared.Outline.Workspaces)),
		slog.Any("enabled", cfg.Enabled()), slog.Duration("interval", cfg.interval), slog.String("policy", set.Digest()))
	return a, nil
}

// RunLoop passes until the context ends.
func (a *App) RunLoop(ctx context.Context) error { return a.controller.Run(ctx) }

// Pass runs one workspace's pass under its lease: false, with no error, when
// another runner holds it. It is what a Lambda invocation runs.
func (a *App) Pass(ctx context.Context, target string, unsafeLocal bool) (bool, error) {
	if err := store.RequireSharedLease(a.sharedLease, unsafeLocal); err != nil {
		return false, err
	}
	targets := a.controller.Targets()
	i := slices.Index(targets, target)
	if i < 0 {
		return false, controller.ErrUnknownTarget
	}
	ran, _, err := a.controller.RunTarget(ctx, targets[i])
	if err == nil && !ran {
		a.log.InfoContext(ctx, "the Outline workspace is leased to another runner: nothing to do", logattr.SafeString("workspace", target))
	}
	return ran, err
}

// Close has nothing to close: the audit trail is the process's.
func (a *App) Close() error { return nil }
