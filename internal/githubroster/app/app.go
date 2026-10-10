// Package app assembles the GitHub controller from its configuration: the
// policy's GitHub table, the console it reads with its own ServiceAccount
// token, the report it writes, and the credentials mounted beside it.
//
// A package rather than the body of main, for the reason the service's
// equivalents are: this is where the decisions a deployment can get wrong
// are made, and main() cannot be tested.
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

	"connectrpc.com/connect"

	"github.com/truvity/sluis/gen/directoryroster/v1/directoryrosterv1connect"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/consoleauth"
	"github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/health"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/telemetry"
	"github.com/truvity/sluis/internal/version"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// Config is what a deployment decides. It is built from the configuration
// file, which is what the chart renders.
type Config struct {
	release string
	// stores says which adapter backs the storage ports.
	stores store.Config
	// policy is the policy document: the bindings are its github table,
	// and its controllers.github section is what this controller changes.
	policy    *config.PolicyDocument
	console   string
	tokenFile string
	// consoleAWS, when set, replaces the token file with the function role's
	// AWS web identity token, for this audience.
	consoleAWS string
	appsDir    string
	recordsDir string
	interval   time.Duration
	enabled    map[string]bool
	logLevel   slog.Level
	// probes is where /healthz and /readyz answer.
	probes string
	// audit is the audit installation the controller records to, with its
	// own identity; without one it only logs what it did.
	audit audit.Config
}

// LogLevel is the level the process should log at.
func (c Config) LogLevel() slog.Level { return c.logLevel }

// ConsoleURL is the console's API the controller reads.
func (c Config) ConsoleURL() string { return c.console }

// Load reads the configuration file, holds it to its schema, and builds the
// settings from it.
//
// The file is either the controller's own document (`sluis controller github`,
// deprecated) or the one service document, whose `controllers.github` section
// is this controller: a deployment that still runs the controller apart reads
// the document the process it was folded into reads.
func Load(file string) (Config, error) {
	if config.IsSluis(file) {
		c, err := config.LoadConfig[config.Sluis](file, nil)
		if err != nil {
			return Config{}, err
		}
		return FromService(c.Service, c.Policy)
	}
	c, err := config.LoadConfig[config.ControllerGitHub](file, nil)
	if err != nil {
		return Config{}, err
	}
	return FromConfig(c.Service, c.Policy)
}

// FromConfig builds the settings from a configuration already read. What a
// schema cannot say is checked here, before anything starts.
func FromConfig(f *config.ControllerGitHub, p *config.PolicyDocument) (Config, error) {
	c := Config{
		release:    orDefault(f.Release, "sluis"),
		stores:     store.FromRoster(&f.Roster).As(port.ModuleGitHub),
		policy:     p,
		console:    strings.TrimSuffix(f.ConsoleURL, "/"),
		tokenFile:  orDefault(f.TokenFile, "/var/run/secrets/github-roster/token"),
		appsDir:    orDefault(f.AppsDir, "/var/run/github-roster/apps"),
		recordsDir: orDefault(f.RecordsDir, "/var/run/github-roster/records"),
		enabled:    map[string]bool{},
		probes:     ":7070",
		// The instance is the pod, which is its hostname in a cluster.
		audit: audit.Config{Version: version.String()},
	}
	if f.Console != nil && f.Console.Auth != nil && f.Console.Auth.AWS != nil {
		if c.consoleAWS = strings.TrimSpace(f.Console.Auth.AWS.Audience); c.consoleAWS == "" {
			return Config{}, errors.New("console.auth.aws.audience is required")
		}
	}
	if f.Probes != nil && f.Probes.Address != "" {
		c.probes = f.Probes.Address
	}
	c.audit.Instance, _ = os.Hostname()
	if a := f.Audit; a != nil {
		c.audit.Writer = a.Writer
		c.audit.TokenFile = a.TokenFile
	}
	for _, org := range p.EnabledOrgs() {
		if org = strings.TrimSpace(org); org != "" {
			c.enabled[org] = true
		}
	}
	c.interval = 15 * time.Minute
	if f.Interval != nil {
		c.interval = f.Interval.D()
	}
	if c.interval <= 0 {
		return Config{}, fmt.Errorf("interval %q is not a positive duration", c.interval)
	}
	level := "info"
	if f.Log != nil && f.Log.Level != "" {
		level = f.Log.Level
	}
	if err := c.logLevel.UnmarshalText([]byte(level)); err != nil {
		return Config{}, fmt.Errorf("log.level: %w", err)
	}
	switch {
	case c.policy == nil:
		return Config{}, errors.New("policy.file is required: the bindings are the policy document's github table")
	case c.console == "":
		return Config{}, errors.New("consoleURL is required: who holds a group is the console's to answer")
	}
	return c, nil
}

// FromService builds the settings of the GitHub controller that the one service
// document runs (`controllers.github`), from the document and the policy it
// names. The controller shares the process's release, policy, ports, adapters,
// audit installation and log level, and, with the `ssm` secrets source, its
// secrets settings (root, layout, KMS key, region, endpoint, grace).
func FromService(s *config.Sluis, p *config.PolicyDocument) (Config, error) {
	f := s.GitHubController()
	if f == nil {
		return Config{}, errors.New("controllers.github is not set")
	}
	c, err := FromConfig(f, p)
	if err != nil {
		return Config{}, err
	}
	c.stores.SetSecrets(s.Secrets)
	return c, nil
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// App is an assembled controller.
type App struct {
	// maint is the gate over the module's maintenance flag.
	maint *maintenance.Gate
	// targets are the targets the policy declares: a pass runs a target by the
	// policy's own spelling of it, never by a string a caller made.
	targets    []string
	controller *controller.Controller
	trail      *audit.Trail
	log        *slog.Logger
	// sharedLease is whether the tick leases are held in a State shared with
	// every other runner.
	sharedLease bool
	// ready is closed until New has succeeded and Run has begun: the probe's
	// answer to "did this pod start".
	ready  *health.Gate
	probes string
	// fatal carries the one error that ends the process from outside a
	// pass: the audit installation refusing the catalogue after the start.
	fatal chan error
}

// Close closes the audit emitter, which delivers what its queue holds within
// its timeout and drops the rest, saying so. Nothing survives the process.
func (a *App) Close() error { return a.trail.Close() }

// The report store reads back what it wrote, so a restarted controller does
// not record every held and reported row again.
var _ controller.StatusReader = (*rails.BlobReports)(nil)

// New assembles the controller.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	declared := cfg.policy.Policy
	// Validated with the service's own loader: a controller acting on a
	// policy the service would refuse is acting on a different model.
	set, err := policy.NewSet(declared)
	if err != nil {
		return nil, fmt.Errorf("the policy: %w", err)
	}
	// The catalogue's grants are consumers too: the policy document carries
	// them, so a group that exists only to let some identity mint an App's
	// token is not reported as unconsumed.
	if unconsumed := cfg.policy.Unconsumed(); len(unconsumed) > 0 {
		log.WarnContext(ctx, "internal groups are declared but nothing consumes them",
			slog.Any("groups", unconsumed))
	}
	if len(declared.GitHub) == 0 {
		log.WarnContext(ctx, "the policy binds no GitHub organisation: every pass will do nothing")
	}
	for org := range cfg.enabled {
		if _, bound := declared.GitHub[org]; !bound {
			return nil, fmt.Errorf("controllers.github.enabledOrgs names %s, which the policy does not bind", org)
		}
	}

	stores, err := store.Open(ctx, cfg.stores, log)
	if err != nil {
		return nil, err
	}
	// A person's link is still a Secret entry the domain store keeps; the
	// memory adapter has none, and the controller checks no link then.
	var links controller.LinkStore
	var appSource controller.AppSource
	switch {
	case stores.Adapter != store.AdapterLegacy:
		// Any adapter but `legacy` keeps the links, the organisations'
		// credentials and the operators' requests on the State and Secrets ports,
		// and the controller reads them there.
		base := portstore.New(stores.Ports).WithV4(stores.V4).WithV5(stores.V5)
		if err = base.RequireSecrets(); err != nil {
			stores.Close()
			return nil, fmt.Errorf("ports.adapter %s: %w", stores.Adapter, err)
		}
		links, appSource = portstore.NewGitHubLinks(base), portstore.NewGitHubOrgs(base)
	default:
		links = clusterLinks(stores)
	}

	// Every call to the console carries the controller's proof: this pod's own
	// projected token, read afresh each time (the kubelet rotates it under the
	// pod), or on Lambda the function role's web identity token.
	var proof consoleauth.Source = consoleauth.File(cfg.tokenFile)
	if cfg.consoleAWS != "" {
		aws, err := consoleauth.NewAWS(ctx, cfg.consoleAWS)
		if err != nil {
			stores.Close()
			return nil, err
		}
		proof = aws
	}
	bearer := consoleauth.Interceptor(proof)
	// And one client span per call, with the traceparent carried to the
	// console, so a tick's trace continues into the console's own spans.
	console := append([]connect.ClientOption{bearer}, telemetry.ConnectClientOptions()...)
	web := &http.Client{Timeout: 30 * time.Second}

	log.InfoContext(ctx, "the GitHub controller is assembled",
		slog.Int("organisations", len(declared.GitHub)), slog.Any("enabled", keys(cfg.enabled)), slog.Duration("interval", cfg.interval),
		slog.String("console", cfg.console),
		slog.String("policy", set.Digest()))
	// A refusal found after the start is fatal the way one at the start is:
	// the process ends rather than running with records nobody accepts.
	fatal := make(chan error, 1)
	cfg.audit.Log = log
	cfg.audit.OnFatal = func(err error) {
		select {
		case fatal <- err:
		default:
		}
	}
	// The audit concern of the resolved table decides the sink; the legacy
	// `audit.writer` is what `connect` means.
	if cfg.audit, err = audit.FromPlan(cfg.audit, stores.Plan); err != nil {
		return nil, err
	}
	trail, err := audit.Open(ctx, cfg.audit)
	if err != nil {
		return nil, err
	}
	leaseState, shared := stores.LeaseState()
	if !shared {
		log.InfoContext(ctx, "no shared state backs the tick leases, so they are held in this process: run one replica",
			slog.String("adapter", stores.Adapter))
	}
	return &App{
		targets:     append(slices.Collect(maps.Keys(declared.GitHub)), controller.LinksTarget),
		log:         log,
		sharedLease: shared,
		maint:       stores.Maintenance(),
		ready:       health.NewGate("the controller"),
		probes:      cfg.probes,
		trail:       trail,
		fatal:       fatal,
		controller: controller.New(controller.Config{Interval: cfg.interval, Enabled: cfg.enabled, AppsDir: cfg.appsDir, RecordsDir: cfg.recordsDir}, controller.Deps{
			Log:      log,
			GitHub:   web,
			Access:   directoryrosterv1connect.NewAccessServiceClient(web, cfg.console, console...),
			Audit:    trail,
			Console:  directoryrosterv1connect.NewGitHubServiceClient(web, cfg.console, console...),
			Policy:   set.Digest(),
			Status:   rails.NewBlobReports(stores.Ports.Blob, "reports/github/"),
			Leases:   &rails.Leases{State: leaseState, Holder: rails.NewHolder(), Log: log, Maintenance: stores.Maintenance()},
			Trigger:  stores.Ports.Trigger,
			Links:    links,
			Apps:     appSource,
			Bindings: declared.GitHub,
		}),
	}, nil
}

// Run passes until the context is done, or the audit installation refuses
// the catalogue after the start, which ends the process the way a refusal
// at the start would have. It is the controller as a process of its own
// (`sluis controller`, deprecated): it serves its own probes, which in the one
// process are the service's.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	// The probes listen before the loop starts, and readiness opens only now:
	// New has returned, so the policy is loaded, the stores are open and the
	// audit catalogue was accepted (a refusal stops New, and then there is no
	// listener and no Ready pod at all).
	go func() {
		if err := health.Serve(ctx, a.probes, health.Mux(0, a.ready.Dependency()), a.log); err != nil {
			done <- err
		}
	}()
	go func() { done <- a.RunLoop(ctx, nil) }()
	return <-done
}

// Readiness is the controller's answer to "did it start": closed until
// [App.RunLoop] begins. The one process adds it to the readiness it serves.
func (a *App) Readiness() health.Dependency { return a.ready.Dependency() }

// RunLoop is the controller's loop alone, for the one process that serves the
// probes. It opens readiness, waits for ready (when not nil) before the first
// pass, then passes until the context is done, or the audit installation
// refuses the catalogue after the start, which ends it with that error.
func (a *App) RunLoop(ctx context.Context, ready func(context.Context)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.ready.Open()
	if ready != nil {
		ready(ctx)
	}
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

// Tick runs one target's tick once, under its lease, and returns: an
// organisation's login, or controller.LinksTarget. It is what
// `sluis tick github` runs, and the shape of a function that lives
// for one invocation. A target another runner holds is left to it.
func (a *App) Tick(ctx context.Context, target string, unsafeLocal bool) error {
	_, err := a.Pass(ctx, target, unsafeLocal)
	return err
}

// Paused says the module is under maintenance, so a pass that did not run was
// skipped for that and not for another runner's lease.
func (a *App) Paused(ctx context.Context) bool { return a.maint.Writable(ctx) != nil }

// Pass is [App.Tick] that says whether the pass ran: false, with no error, when
// another runner holds the target's lease and the pass is left to it. It is what
// a Lambda invocation runs (cmd/sluis-lambda), which reports a contended
// invocation as such and not as a success.
func (a *App) Pass(ctx context.Context, target string, unsafeLocal bool) (ran bool, err error) {
	if err := store.RequireSharedLease(a.sharedLease, unsafeLocal); err != nil {
		return false, err
	}
	// Resolved to the declared spelling, so that what is logged and keyed below
	// is the policy's and not the caller's.
	i := slices.Index(a.targets, target)
	if i < 0 {
		return false, controller.ErrUnknownTarget
	}
	ran, _, err = a.controller.RunTarget(ctx, a.targets[i])
	if err == nil && !ran {
		a.log.InfoContext(ctx, "the target is leased to another runner: nothing to do", logattr.SafeString("target", target))
	}
	return ran, err
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
