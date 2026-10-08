// Package rosterapp assembles the whole of sluis as ONE process:
// the directory connectors, the snapshot and its refresher, the policy,
// the OpenID provider, the login page and the console and, when the service
// document names them (`controllers`), the GitHub and Slack controllers, which
// run their loops beside them and answer to the same probes.
//
// It exists because the split into two services stopped earning its keep.
// The hub was built as a directory of record that several
// things could ask; the issuer became its only consumer, and the split
// then cost — on every single login — a ConnectRPC call, a TokenReview,
// a NetworkPolicy hop, a second store, and a class of failure where the
// two halves disagree about the same person.
//
// What this package does is wiring, and deliberately nothing else. The
// two halves are still assembled by their own packages, with their own
// decisions and their own tests; this joins them: the issuer's directory
// is a function call, the console is mounted on the issuer's origin, one
// /readyz answers for both stores, and one process runs the loops.
package rosterapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/app"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	"github.com/truvity/sluis/internal/health"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/hublocal"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/server"
	slackapp "github.com/truvity/sluis/internal/slackroster/app"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/policy"
)

// Config is both halves' configuration. Both are built from the service
// document the process is given and the one policy document it names, so
// neither half grows a second way to be configured, and the two cannot read the
// same key two ways.
type Config struct {
	Directory app.Config
	Issuer    issuerapp.Config
	// Stores says which adapter backs the storage ports and how to reach it.
	Stores store.Config
	// GitHub and Slack are the controllers the document runs beside the
	// service (`controllers.github`, `controllers.slack`); nil is off. A
	// function that runs a controller one pass per invocation takes them and
	// clears them before [New], which would run their loops.
	GitHub *githubapp.Config
	Slack  *slackapp.Config
	// Cloudflare is sluis as the STS for Cloudflare tokens and R2 credentials
	// (`cloudflare`); nil is off. Grants are the policy's, Instance names the
	// tokens it mints.
	Cloudflare *config.Cloudflare
	Grants     *config.PolicyCloudflare
	Instance   string
	// CloudflareDial opens a Cloudflare account; nil is the real client.
	CloudflareDial minter.Dialer
}

// LogLevel is the level the process should log at. It is the issuer's,
// because both read the same log.level and the issuer is the half that
// owns the origin.
func (c Config) LogLevel() slog.Level { return c.Issuer.LogLevel() }

// Load reads the service document and the policy document it names, holds
// each to its schema and checks, and builds both halves' settings from them.
// A document that names no policy decides by the built-in one (the
// demonstration's under `demo`).
func Load(file string) (Config, error) {
	svc, err := config.Load[config.Sluis](file)
	if err != nil {
		return Config{}, err
	}
	fallback, err := app.FallbackPolicy(svc.Demo)
	if err != nil {
		return Config{}, err
	}
	p, err := config.PolicyOf(svc, &fallback)
	if err != nil {
		return Config{}, err
	}
	cfg, err := FromConfig(&svc.Serve, p)
	if err != nil {
		return Config{}, err
	}
	if cfg.GitHub, cfg.Slack, err = controllersOf(svc, p); err != nil {
		return Config{}, err
	}
	// The secrets the document names are read through one source, opened
	// here and handed to both halves with the storage ports. Opening connects
	// to nothing: a secret is read when it is used.
	var clients []string
	if p != nil {
		for id := range p.Policy.Clients {
			if p.Policy.Clients[id].Kind == policy.KindConfidential {
				clients = append(clients, secrets.ClientSecret(id))
			}
		}
	}
	if cfg.Stores.Secrets, err = secrets.Open(context.Background(), &svc.Serve, clients...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// controllersOf builds the settings of the controllers the document names.
// They decide by the policy the document names, never by the built-in one: a
// controller that acts on a policy nobody wrote is not one to start.
func controllersOf(svc *config.Sluis, p *config.PolicyDocument) (*githubapp.Config, *slackapp.Config, error) {
	if svc.Controllers == nil {
		return nil, nil, nil
	}
	if svc.Policy == nil || svc.Policy.File == "" {
		return nil, nil, errors.New("controllers: the controllers act on the policy document, so `policy.file` is required")
	}
	var gh *githubapp.Config
	var sl *slackapp.Config
	if svc.Controllers.GitHub != nil {
		c, err := githubapp.FromService(svc, p)
		if err != nil {
			return nil, nil, fmt.Errorf("controllers.github: %w", err)
		}
		gh = &c
	}
	if svc.Controllers.Slack != nil {
		c, err := slackapp.FromService(svc, p)
		if err != nil {
			return nil, nil, fmt.Errorf("controllers.slack: %w", err)
		}
		sl = &c
	}
	return gh, sl, nil
}

// FromConfig builds both halves' settings from the documents already read.
func FromConfig(f *config.Serve, p *config.PolicyDocument) (Config, error) {
	directory, err := app.FromConfig(f, p)
	if err != nil {
		return Config{}, err
	}
	issuer, err := issuerapp.FromConfig(f, p)
	if err != nil {
		return Config{}, err
	}
	stores, err := store.FromServe(f)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{Directory: directory, Issuer: issuer, Stores: stores}
	if f.Cloudflare != nil || (p != nil && len(p.Cloudflare().Grants) > 0) {
		// Grants for presets nobody declared would read as rights nobody can use.
		if err = config.CheckCloudflare(f.Cloudflare, p); err != nil {
			return Config{}, err
		}
		cfg.Cloudflare, cfg.Grants = f.Cloudflare, p.Cloudflare()
		cfg.Instance = f.Instance
		if cfg.Instance == "" {
			cfg.Instance = f.Release
		}
		if cfg.Instance == "" {
			cfg.Instance = "sluis"
		}
	}
	return cfg, nil
}

// App is the assembled service.
type App struct {
	directory *app.App
	issuer    *issuerapp.App
	stores    *store.Stores
	log       *slog.Logger
	// github and slack are the controllers that run beside the service; nil
	// when the document names none.
	github *githubapp.App
	slack  *slackapp.App
	// consoles are where the controllers read the console, which is this very
	// process: a controller starts its passes once its console answers.
	consoles map[string]string
	// cloudflare mints Cloudflare credentials; nil when `cloudflare` is absent.
	cloudflare *minter.Minter
}

// Handler is everything served on the public port: the OpenID surface
// and the login page at the origin root, the console under /console/ —
// all of it under one reading of each request for the audit trail, so an
// exchange at /token records where it came from as a console call does.
func (a *App) Handler() http.Handler { return a.issuer.Handler() }

// HealthHandler is liveness and readiness for both halves.
func (a *App) HealthHandler() http.Handler { return a.issuer.HealthHandler() }

// Policy is the policy the service decides by, loaded once and shared by both
// halves.
func (a *App) Policy() *policy.Set { return a.directory.Policy() }

// Trigger is the Trigger the console notifies, as the plan chose it.
func (a *App) Trigger() port.Trigger { return a.stores.Ports.Trigger }

// ReconcileClientSecrets makes sure every client whose secret the issuer
// generates has it stored. New runs it once; a function with no loop runs it
// on a schedule, beside the directory refresh.
func (a *App) ReconcileClientSecrets(ctx context.Context) clientcreds.Result {
	return a.issuer.ReconcileClientSecrets(ctx)
}

// UseRequestRefresh makes the directory refresh a snapshot that is due from the
// request that finds it, for a function that has no background loop. See
// [hub.Hub.UseRequestRefresh].
func (a *App) UseRequestRefresh(timeout time.Duration) {
	a.directory.Hub().UseRequestRefresh(timeout)
}

// RefreshDirectory runs one refresh pass over every connected workspace, under
// the refresh lease. It is what a function runs on a schedule in place of the
// loop [App.Run] starts.
func (a *App) RefreshDirectory(ctx context.Context) (hub.RefreshResult, error) {
	return a.directory.Hub().RefreshPass(ctx)
}

// Settle waits for the work a request left running after its response, which
// a function that is frozen between invocations would otherwise never finish.
func (a *App) Settle() { a.directory.Hub().Wait() }

// FlushAudit waits until the audit records still queued are delivered, or ctx
// ends. A Lambda function calls it before each invocation returns: the process
// is frozen afterwards and the queue would wait for the next one.
func (a *App) FlushAudit(ctx context.Context) error {
	if f, ok := a.directory.Audit().(interface{ Flush(context.Context) error }); ok {
		return f.Flush(ctx)
	}
	return nil
}

// Close releases what New opened.
func (a *App) Close() {
	a.closeControllers()
	if a.directory != nil {
		a.directory.Close()
	}
	a.stores.Close()
}

func (a *App) closeControllers() {
	closeEmitter := func(err error) {
		if err != nil {
			a.log.Warn("the audit emitter could not be closed cleanly; what its queue held is dropped", "error", err)
		}
	}
	if a.github != nil {
		closeEmitter(a.github.Close())
	}
	if a.slack != nil {
		closeEmitter(a.slack.Close())
	}
}

// New assembles the service. The directory half is built first: the
// issuer is given a door to it rather than an address for it, so there
// is no issuer to build until the directory exists.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	// The storage ports are built once, here, and handed to both halves.
	stores, err := store.Open(ctx, cfg.Stores, log)
	if err != nil {
		return nil, err
	}
	directory, err := app.New(ctx, cfg.Directory, stores, log)
	if err != nil {
		stores.Close()
		return nil, err
	}
	// From here a failure closes what is open through the App itself.
	a := &App{directory: directory, stores: stores, log: log, consoles: map[string]string{}}
	// The controllers are assembled before the issuer, whose readiness is
	// theirs too: a controller that failed to start (a refused audit
	// catalogue, an enabled organisation the policy does not bind) is a
	// process that does not start, as it is when the controller runs apart.
	var controllerReady []health.Dependency
	if cfg.GitHub != nil {
		if a.github, err = githubapp.New(ctx, *cfg.GitHub, log); err != nil {
			a.Close()
			return nil, fmt.Errorf("controllers.github: %w", err)
		}
		a.consoles["github"] = cfg.GitHub.ConsoleURL()
		controllerReady = append(controllerReady, a.github.Readiness())
	}
	if cfg.Slack != nil {
		if a.slack, err = slackapp.New(ctx, *cfg.Slack, log); err != nil {
			a.Close()
			return nil, fmt.Errorf("controllers.slack: %w", err)
		}
		a.consoles["slack"] = cfg.Slack.ConsoleURL()
		controllerReady = append(controllerReady, a.slack.Readiness())
	}
	// Freshness stays the hub's own decision, which is why no maximum age
	// is passed: a login that forced a live read on every sign-in would
	// turn one corporate directory's slowness into everybody's.
	deps := issuerapp.Deps{
		Stores:    stores,
		Directory: hublocal.New(directory.Hub(), 0),
		Console:   directory.ConsoleHandler(),
		Ready:     append([]health.Dependency{directory.Readiness()}, controllerReady...),
		// The SAME policy, loaded once. Both halves read policyDir, so
		// they would ordinarily agree — but their fallbacks differ, and
		// two halves that can disagree about the policy is the class of
		// failure this merge existed to end. Found by running it: with
		// `demo` the directory built a demonstration policy and the
		// issuer refused to start on an empty path.
		Policy: directory.Policy(),
		// And the console learns who is signed in from the issuer's own
		// browser session, once the issuer exists. Without this the
		// merged deployment would need either a proxy in front of the
		// console — running an OpenID flow against a service in this very
		// process — or the console's own login, which is the second door
		// an installation with a gateway deliberately turns off.
		UseSignedIn:    directory.ConsoleServer().UseSignedIn,
		UseSignInEntry: directory.ConsoleServer().UseSignInEntry,
		UseIssuerURL:   directory.ConsoleServer().UseIssuerURL,
		// A controller beside the issuer reads the console's API with its
		// own ServiceAccount token, verified by the issuer's cluster key
		// sets. The console is built before the issuer exists, so it is
		// handed the reader rather than building one.
		UseWorkloads: directory.ConsoleServer().UseWorkloads,
		// One audit trail for both halves: a sign-in and the connect that
		// made it possible belong to one history.
		Audit: directory.Audit(),
		// What an audit event keeps of a request goes into its context at
		// the outermost handler — the one the issuer's listener serves.
		// Wrapping a handler only this package returned left the served
		// one bare, and every event on a deployment without its request.
		Around: directory.AuditRequests,
	}
	// Installation tokens for catalogue Apps are minted at the issuer's
	// token endpoint, from the keys the console created each App with:
	// the same catalogue, and the same Secret, in the same process. The
	// ring of recent requests is one more thing both halves share: this
	// one writes it, the console's Apps pages read it.
	apps := issuer.GitHubApps{Catalogue: directory.GitHubCatalogue(), Recent: directory.GitHubMints()}
	if store := directory.GitHubCatalogueApps(); store != nil {
		apps.Store = store
	}
	deps.GitHubApps = &apps
	assembled, err := issuerapp.New(ctx, cfg.Issuer, deps, log)
	if err != nil {
		a.Close()
		return nil, err
	}
	// The console's Audit page reads the audit installation's query
	// service as the person signed in, with a token minted here for the
	// installation's audience. Only now can it be: the issuer that mints
	// it did not exist when the console was built.
	if queryURL, audience := directory.AuditQuery(); queryURL != "" {
		target, err := url.Parse(queryURL)
		if err != nil || target.Scheme == "" || target.Host == "" {
			a.Close()
			return nil, fmt.Errorf("sluis: audit.queryURL %q is not a URL", queryURL)
		}
		directory.ConsoleServer().UseAuditQuery(&server.AuditQuery{
			URL:   target,
			Token: auditToken(assembled, audience),
		})
	}
	if cfg.Cloudflare != nil && len(cfg.Cloudflare.Presets) > 0 {
		a.issuer = assembled
		if a.cloudflare, err = newCloudflare(cfg, stores, directory.Audit(), log); err != nil {
			a.Close()
			return nil, err
		}
		// The on-demand exchange and the grants listing at the issuer.
		assembled.Issuer().UseCloudflare(a.cloudflare)
	}
	log.InfoContext(ctx, "sluis assembled as one service: a login makes no network "+
		"call except to the corporate directory", "controllers", len(a.consoles))
	a.issuer = assembled
	return a, nil
}

// Run serves the listeners and drives the directory's loops until the
// context is done.
func (a *App) Run(ctx context.Context) error {
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() error { return a.issuer.Run(gctx) })
	group.Go(func() error { return a.directory.RunLoops(gctx) })
	// The controllers' loops end with the service, and a controller that fails
	// ends it: the one process is as healthy as its parts. Each waits for the
	// console, which is this process, to answer before its first pass.
	if a.github != nil {
		group.Go(func() error {
			return a.github.RunLoop(gctx, func(ctx context.Context) { awaitConsole(ctx, a.log, "github", a.consoles["github"]) })
		})
	}
	if a.slack != nil {
		group.Go(func() error {
			return a.slack.RunLoop(gctx, func(ctx context.Context) { awaitConsole(ctx, a.log, "slack", a.consoles["slack"]) })
		})
	}
	if a.cloudflare != nil {
		group.Go(func() error { a.runCloudflare(gctx); return nil })
	}
	if err := group.Wait(); err != nil {
		return fmt.Errorf("sluis: %w", err)
	}
	return nil
}

// auditTokenLifetime is how long a token the console mints for the audit
// trail lives. Short, because the console keeps it for as long as it lives:
// a person taken out of the group that admits them to the trail stops
// reading it within this, not within the issuer's hour.
const auditTokenLifetime = 5 * time.Minute

// auditToken mints the Audit page's tokens: for a person, for the audit
// installation's audience, as a token exchange would decide it. Somebody the
// audience does not admit, and a sign-in with no address (a recovery, a
// workload), may not read the trail through the console.
func auditToken(assembled *issuerapp.App, audience string) func(context.Context, access.Identity) (string, time.Time, error) {
	return func(ctx context.Context, id access.Identity) (string, time.Time, error) {
		if id.Email == "" {
			return "", time.Time{}, server.ErrNotAdmitted
		}
		token, expires, err := assembled.MintFor(ctx, id.Email, audience, auditTokenLifetime)
		if errors.Is(err, issuer.ErrRefused) || errors.Is(err, issuer.ErrUnknownTarget) {
			return "", time.Time{}, fmt.Errorf("%w: %w", server.ErrNotAdmitted, err)
		}
		return token, expires, err
	}
}
