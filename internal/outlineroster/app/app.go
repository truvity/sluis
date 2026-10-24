// Package app assembles the Outline module from its own document
// (`sluis-outline/v1`): the controller, which reads the console's API with its
// own proof as the roster controllers' workload, and the console service the
// issuer calls for the Outline pages. The module is the only holder of the
// workspaces' API keys (docs/decisions/0077).
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/truvity/sluis/blobprefix"
	"github.com/truvity/sluis/gen/directoryroster/v1/directoryrosterv1connect"
	"github.com/truvity/sluis/internal/adaptercheck"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/consoleauth"
	"github.com/truvity/sluis/internal/egress"
	"github.com/truvity/sluis/internal/health"
	"github.com/truvity/sluis/internal/modcall"
	outlineconsole "github.com/truvity/sluis/internal/outlineroster/console"
	"github.com/truvity/sluis/internal/outlineroster/controller"
	"github.com/truvity/sluis/internal/outlineroster/outlineapi"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/telemetry"
	"github.com/truvity/sluis/internal/version"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// Config is what a deployment decides.
type Config struct {
	stores     store.Config
	policy     *config.PolicyDocument
	console    string
	tokenFile  string
	consoleAWS string
	interval   time.Duration
	shareScan  time.Duration
	enabled    map[string]bool
	logLevel   slog.Level
	probes     string
	serve      *config.ModuleServe
	audit      audit.Config
}

// Load reads the module's document and the policy it names.
func Load(file string) (Config, error) {
	c, err := config.LoadConfig[config.SluisOutline](file, nil)
	if err != nil {
		return Config{}, err
	}
	return FromConfig(c.Service, c.Policy)
}

// FromConfig builds the settings from a document already read.
func FromConfig(f *config.SluisOutline, p *config.PolicyDocument) (Config, error) {
	if p == nil {
		return Config{}, errors.New("policy.file is required: the bindings are the policy document's outline table")
	}
	c := Config{
		stores:    store.FromRoster(&f.Roster).As(port.ModuleOutline),
		policy:    p,
		console:   strings.TrimSuffix(f.ConsoleURL, "/"),
		tokenFile: f.TokenFile,
		interval:  controller.DefaultInterval,
		shareScan: controller.DefaultShareScan,
		enabled:   map[string]bool{},
		probes:    ":7070",
		audit:     audit.Config{Version: version.String()},
	}
	if c.tokenFile == "" {
		c.tokenFile = config.OutlineTokenFile
	}
	if f.Console != nil && f.Console.Auth != nil && f.Console.Auth.AWS != nil {
		if c.consoleAWS = strings.TrimSpace(f.Console.Auth.AWS.Audience); c.consoleAWS == "" {
			return Config{}, errors.New("console.auth.aws.audience is required")
		}
	}
	if f.Probes != nil && f.Probes.Address != "" {
		c.probes = f.Probes.Address
	}
	if f.Outline != nil {
		c.serve = f.Outline.Serve
		if f.Outline.ShareScan != nil {
			c.shareScan = f.Outline.ShareScan.D()
		}
	}
	if f.Interval != nil {
		c.interval = f.Interval.D()
	}
	c.audit.Instance, _ = os.Hostname()
	c.stores.SetSecrets(f.Secrets)
	if a := f.Audit; a != nil {
		c.audit.Writer, c.audit.TokenFile = a.Writer, a.TokenFile
	}
	level := "info"
	if f.Log != nil && f.Log.Level != "" {
		level = f.Log.Level
	}
	if err := c.logLevel.UnmarshalText([]byte(level)); err != nil {
		return Config{}, fmt.Errorf("log.level: %w", err)
	}
	switch {
	case c.interval <= 0:
		return Config{}, fmt.Errorf("interval %q is not a positive duration", c.interval)
	case c.shareScan <= 0:
		return Config{}, fmt.Errorf("outline.shareScan %q is not a positive duration", c.shareScan)
	case c.console == "":
		return Config{}, errors.New("consoleURL is required: who holds a group is the console's to answer")
	}
	for _, ws := range p.EnabledOutlineWorkspaces() {
		if ws = strings.TrimSpace(ws); ws != "" {
			c.enabled[ws] = true
		}
	}
	return c, nil
}

// LogLevel is the level the process logs at.
func (c Config) LogLevel() slog.Level { return c.logLevel }

// Serve is where the module answers the console's calls on Kubernetes; nil
// serves nothing.
func (c Config) Serve() *config.ModuleServe { return c.serve }

// Declares says the policy names the workspace key.
func (c Config) Declares(target string) bool {
	_, declared := c.policy.Policy.Outline.Workspaces[target]
	return declared
}

// Enabled are the workspaces the controller acts in, sorted.
func (c Config) Enabled() []string { return slices.Sorted(maps.Keys(c.enabled)) }

// check is what New refuses before it opens anything.
func (c Config) check() (*policy.Set, error) {
	set, err := policy.NewSet(c.policy.Policy)
	if err != nil {
		return nil, fmt.Errorf("the policy: %w", err)
	}
	for ws := range c.enabled {
		if _, declared := c.policy.Policy.Outline.Workspaces[ws]; !declared {
			return nil, fmt.Errorf("controllers.outline.enabledWorkspaces names %s, which the policy does not declare", ws)
		}
	}
	return set, nil
}

// Verify builds what a pass builds and reaches nothing that mutates
// (adaptercheck).
func (c Config) Verify(ctx context.Context, log *slog.Logger) error {
	if _, err := c.check(); err != nil {
		return err
	}
	st, err := adaptercheck.Verify(ctx, log, c.stores, c.audit, adaptercheck.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if st == nil || st.Adapter == store.AdapterLegacy {
		return errors.New("the Outline module needs a storage adapter other than legacy")
	}
	return portstore.New(st.Ports).WithV5(st.V5).RequireSecrets()
}

// App is an assembled module.
type App struct {
	cfg         Config
	controller  *controller.Controller
	console     *outlineconsole.Service
	rpc         *modcall.Server
	trail       *audit.Trail
	log         *slog.Logger
	sharedLease bool
	ready       *health.Gate
	fatal       chan error
	closeStores func()
}

// New assembles the module.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	set, err := cfg.check()
	if err != nil {
		return nil, err
	}
	stores, err := store.Open(ctx, cfg.stores, log)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*App, error) {
		stores.Close()
		return nil, err
	}
	if stores.Adapter == store.AdapterLegacy {
		return fail(errors.New("the Outline module needs a storage adapter other than legacy: the workspaces' connections are on the State port"))
	}
	base := portstore.New(stores.Ports).WithV5(stores.V5)
	if err = base.RequireSecrets(); err != nil {
		return fail(fmt.Errorf("ports.adapter %s: %w", stores.Adapter, err))
	}
	var proof consoleauth.Source = consoleauth.File(cfg.tokenFile)
	if cfg.consoleAWS != "" {
		aws, err := consoleauth.NewAWS(ctx, cfg.consoleAWS)
		if err != nil {
			return fail(err)
		}
		proof = aws
	}
	console := connect.NewClient(connecthttp.NewTransport(&http.Client{Timeout: 30 * time.Second}, cfg.console, connecthttp.WithReadMaxBytes(0)),
		consoleauth.Interceptor(proof), telemetry.ConnectClientInterceptor())

	fatal := make(chan error, 1)
	cfg.audit.Log = log
	cfg.audit.OnFatal = func(err error) {
		select {
		case fatal <- err:
		default:
		}
	}
	if cfg.audit, err = audit.FromPlan(cfg.audit, stores.Plan); err != nil {
		return fail(err)
	}
	trail, err := audit.Open(ctx, cfg.audit)
	if err != nil {
		return fail(err)
	}
	leaseState, shared := stores.LeaseState()
	web := &http.Client{Timeout: 30 * time.Second, Transport: egress.Policy{AllowPrivate: true}.Transport()}
	records := portstore.NewOutlineWorkspaces(base)
	reports := rails.NewBlobReports(stores.Ports.Blob, blobprefix.OutlineReports)
	a := &App{cfg: cfg, log: log, sharedLease: shared, trail: trail, fatal: fatal, ready: health.NewGate("the Outline module"),
		closeStores: stores.Close}
	a.controller = controller.New(controller.Config{Interval: cfg.interval, Enabled: cfg.enabled, ShareScan: cfg.shareScan}, controller.Deps{
		Log: log, Directory: directoryrosterv1connect.NewAccessServiceClient(console), Audit: trail,
		Status: reports, Records: records, Policy: cfg.policy.Policy, Digest: set.Digest(),
		Outline: func(url, key string) *outlineapi.Client {
			return outlineapi.New(url, key, outlineapi.WithHTTPClient(web))
		},
		Leases: &rails.Leases{State: leaseState, Holder: rails.NewHolder(), Log: log, Maintenance: stores.Maintenance()},
	})
	a.console = outlineconsole.NewService(outlineconsole.Deps{
		Log: log, Policy: set, Workspaces: records, Status: reports, Enabled: cfg.Enabled(), Audit: trail,
	})
	a.rpc = outlineconsole.NewServer(log, func(context.Context) (*outlineconsole.Service, error) { return a.console, nil })
	if len(cfg.policy.Policy.Outline.Workspaces) == 0 {
		log.WarnContext(ctx, "the policy declares no Outline workspace: every pass will do nothing")
	}
	log.InfoContext(ctx, "the Outline module is assembled", slog.Int("workspaces", len(cfg.policy.Policy.Outline.Workspaces)),
		slog.Any("enabled", cfg.Enabled()), slog.Duration("interval", cfg.interval), slog.String("console", cfg.console),
		slog.String("policy", set.Digest()))
	return a, nil
}

// RPC is the module's server of the console's calls.
func (a *App) RPC() *modcall.Server { return a.rpc }

// Console is the module's console service.
func (a *App) Console() *outlineconsole.Service { return a.console }

// Run passes, and serves the probes, until the context ends or the audit
// installation refuses the catalogue.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() {
		if err := health.Serve(ctx, a.cfg.probes, health.Mux(0, a.ready.Dependency()), a.log); err != nil {
			done <- err
		}
	}()
	go func() { done <- a.RunLoop(ctx) }()
	return <-done
}

// RunLoop is the controller's loop alone.
func (a *App) RunLoop(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.ready.Open()
	done := make(chan error, 1)
	go func() { done <- a.controller.Run(ctx) }()
	select {
	case err := <-a.fatal:
		cancel()
		<-done
		return fmt.Errorf("audit: %w", err)
	case err := <-done:
		return err
	}
}

// Tick runs one workspace's pass once, under its lease.
func (a *App) Tick(ctx context.Context, target string, unsafeLocal bool) error {
	_, err := a.Pass(ctx, target, unsafeLocal)
	return err
}

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

// Flush delivers what the audit trail has queued.
func (a *App) Flush(ctx context.Context) error { return a.trail.Flush(ctx) }

// Close closes the audit emitter and the stores.
func (a *App) Close() error {
	err := a.trail.Close()
	if a.closeStores != nil {
		a.closeStores()
	}
	return err
}
