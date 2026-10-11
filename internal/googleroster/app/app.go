// Package app assembles the Google module from its own document (apiVersion
// sluis-google/v1): the hub's refresh writer. It holds the workspaces' keys and
// the Google client, writes each workspace's snapshot to the hub cache in the
// blob store, and takes the refresh lease in the google table.
//
// It is the ONLY writer of those snapshots (D-v176-6 a): the issuer reads the
// snapshot that is there, and the schedule that runs this module keeps it
// fresh. The module has no listener but its probes and serves no request.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/backend/google"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/connector"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	"github.com/truvity/sluis/internal/health"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/hubwire"
	"github.com/truvity/sluis/internal/lazy"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/version"
	"github.com/truvity/sluis/storage/logattr"
)

// ErrUnknownWorkspace is the answer of a tick for a workspace the module does
// not hold.
var ErrUnknownWorkspace = errors.New("the module holds no such workspace")

// secretReadTimeout bounds the lazy read of the OAuth client, which has no
// request to run under.
const secretReadTimeout = 15 * time.Second

// Config is what the module is made of, built from its document.
type Config struct {
	doc      *config.SluisGoogle
	stores   store.Config
	audit    audit.Config
	hub      hub.Config
	logLevel slog.Level
	probes   string

	// Test seams: the readers that open a workspace, in place of Google.
	openDeclared func(ctx context.Context, d *hub.Declared) (backend.Backend, error)
	openStored   func(ctx context.Context, kind string, cred backend.Credential) (backend.Backend, error)
}

// LogLevel is the level the process should log at.
func (c Config) LogLevel() slog.Level { return c.logLevel }

// Load reads the module's document and builds the settings. Opening connects to
// nothing.
func Load(file string) (Config, error) {
	doc, err := config.LoadGoogle(file)
	if err != nil {
		return Config{}, err
	}
	return FromDocument(doc)
}

// FromDocument builds the settings from the document already read.
func FromDocument(doc *config.SluisGoogle) (Config, error) {
	serve := doc.Serve()
	stores, err := store.FromServe(serve)
	if err != nil {
		return Config{}, err
	}
	// The module's own table holds its workspaces and the refresh lease; it
	// reads no peer's. The schedule's trigger has no function to start here.
	stores = stores.As(port.ModuleGoogle)
	stores.NoTrigger = true
	if stores.Secrets, err = secrets.Open(context.Background(), serve); err != nil {
		return Config{}, err
	}
	c := Config{doc: doc, stores: stores, probes: ":7070", audit: audit.Config{Version: version.String()}}
	// The audit instance is the pod, which is its hostname in a cluster.
	c.audit.Instance, _ = os.Hostname()
	if a := doc.Audit; a != nil {
		c.audit.Writer = a.Writer
		c.audit.TokenFile = a.TokenFile
	}
	if doc.Probes != nil && doc.Probes.Address != "" {
		c.probes = doc.Probes.Address
	}
	c.hub.RefreshInterval, c.hub.FreshnessWindow, c.hub.ProbeInterval = doc.Google.Intervals()
	level := "info"
	if doc.Log != nil && doc.Log.Level != "" {
		level = doc.Log.Level
	}
	if err = c.logLevel.UnmarshalText([]byte(level)); err != nil {
		return Config{}, fmt.Errorf("log.level: %w", err)
	}
	return c, nil
}

// App is the assembled module.
type App struct {
	hub    *hub.Hub
	stores *store.Stores
	log    *slog.Logger
	ready  *health.Gate
	probes string
	// maint is the gate over the module's maintenance flag; nil never refuses.
	maint *maintenance.Gate
	// sharedLease is whether the refresh lease is held in a State shared with
	// every other runner.
	sharedLease bool
	// trail is where the module records what it did for a console; the console
	// service and its server answer other modules' calls.
	trail   *audit.Trail
	console *googleconsole.Service
	rpc     *modcall.Server
}

// New assembles the module: the ports, the hub over the module's own table and
// the snapshot cache, and every workspace's reader.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	stores, err := store.Open(ctx, cfg.stores, log)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*App, error) {
		stores.Close()
		return nil, err
	}
	// A refusal found after the start is logged, and the next record says it again.
	cfg.audit.Log = log
	if cfg.audit, err = audit.FromPlan(cfg.audit, stores.Plan); err != nil {
		return fail(err)
	}
	trail, err := audit.Open(ctx, cfg.audit)
	if err != nil {
		return fail(err)
	}
	fail = func(err error) (*App, error) {
		_ = trail.Close()
		stores.Close()
		return nil, err
	}
	base := portstore.New(stores.Ports).WithV4(stores.V4).WithV5(stores.V5)
	if err = base.RequireSecrets(); err != nil {
		return fail(fmt.Errorf("ports.adapter %s: %w", stores.Adapter, err))
	}
	workspaces, credentials := portstore.NewWorkspaces(base), portstore.NewCredentials(base)

	// The snapshots and the refresh lease: the hub cache in the blob store (its
	// prefix is the hub's, which the issuer reads through too) and the lease in
	// this module's table.
	// TODO(blobprefix): the prefix is hub.SnapshotBlobPrefix until the shared
	// blobprefix package (rc.11: `google/cache/`) is on this branch's base; the
	// hub reads it from there then, and this module follows without a change.
	snapshots := hub.NewBlobSnapshots(stores.Ports.Blob, stores.Ports.State)
	directory := hub.New(workspaces, snapshots, cfg.hub, log)
	directory.UseCredentials(credentials)
	maint := stores.Maintenance()
	if maint != nil {
		// A restore is writing the snapshots: a refresh stands still.
		directory.UseMaintenance(maint)
	}

	// The one connector: it reopens a stored credential, finishes a consent,
	// takes an uploaded key and signs somebody in, with the one OAuth client the
	// module holds the secret of.
	conn := connector.NewGoogle(oauthClient(cfg.doc.Google.OAuthClient, cfg.doc.Google.PublicURL, stores.Secrets))
	opener := cfg.openStored
	if opener == nil {
		opener = func(ctx context.Context, kind string, cred backend.Credential) (backend.Backend, error) {
			if kind != "google" {
				return nil, fmt.Errorf("unknown backend %q; this module reads google", kind)
			}
			return conn.OpenStored(ctx, cred)
		}
	}
	directory.UseReopener(func(ctx context.Context, ws hub.Workspace, cred backend.Credential) (backend.Backend, error) {
		return opener(ctx, ws.Backend, cred)
	})
	declaredOpener := cfg.openDeclared
	if declaredOpener == nil {
		declaredOpener = openGoogle
	}
	adopted, err := hubwire.AdoptDeclared(ctx, directory, workspaces, cfg.doc.Google.Workspaces, stores.Secrets, declaredOpener, log)
	if err != nil {
		return fail(err)
	}
	if err = hubwire.ReopenStored(ctx, directory, workspaces, credentials, adopted, opener, log); err != nil {
		return fail(err)
	}

	_, shared := stores.LeaseState()
	if !shared {
		log.InfoContext(ctx, "no shared state backs the refresh lease, so it is held in this process: run one replica",
			slog.String("adapter", stores.Adapter))
	}
	svc := googleconsole.NewService(directory, []googleconsole.Connector{conn}, trail, log)
	return &App{hub: directory, stores: stores, log: log, ready: health.NewGate("the Google module"),
		probes: cfg.probes, maint: maint, sharedLease: shared, trail: trail, console: svc, rpc: svc.Server()}, nil
}

// Console is the console's Workspace service over the module's hub.
func (a *App) Console() *googleconsole.Service { return a.console }

// RPC is the module's server: the console's methods, answered to the caller
// class `console`, and the sign-in's, answered to `issuer`. What a transport of
// internal/modcall serves.
func (a *App) RPC() *modcall.Server { return a.rpc }

// Flush delivers the audit records the module queued, within ctx.
func (a *App) Flush(ctx context.Context) error { return a.trail.Flush(ctx) }

// Hub is the module's hub, for a caller that drives it directly.
func (a *App) Hub() *hub.Hub { return a.hub }

// Paused says the module is under maintenance, so a pass that did not run was
// skipped for that and not for another runner's lease.
func (a *App) Paused(ctx context.Context) bool { return a.maint != nil && a.maint.Writable(ctx) != nil }

// Run is the module as a process of its own: it serves its probes and runs the
// refresher and the prober until ctx ends. Readiness opens once the loops are
// about to start, after every reader is open.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() {
		if err := health.Serve(ctx, a.probes, health.Mux(0, a.ready.Dependency()), a.log); err != nil {
			done <- err
		}
	}()
	a.ready.Open()
	go func() { done <- a.hub.Run(ctx) }()
	return <-done
}

// Tick refreshes one workspace once, under its lease, and returns: the shape of
// a function that lives for one invocation. A workspace another runner has
// refreshed this interval is left alone, and so is one under maintenance.
func (a *App) Tick(ctx context.Context, workspace string, unsafeLocal bool) error {
	_, err := a.Pass(ctx, workspace, unsafeLocal)
	return err
}

// Pass is [App.Tick] that says what happened: [hub.RefreshContended] and
// [hub.RefreshPaused] come with no error, and a refresh that failed is an error.
func (a *App) Pass(ctx context.Context, workspace string, unsafeLocal bool) (hub.RefreshOutcome, error) {
	if err := store.RequireSharedLease(a.sharedLease, unsafeLocal); err != nil {
		return "", err
	}
	id, err := a.resolve(ctx, workspace)
	if err != nil {
		return "", err
	}
	out := a.hub.RefreshWorkspace(ctx, id)
	a.hub.Wait()
	if out == hub.RefreshFailed {
		return out, fmt.Errorf("the refresh of %s failed; the snapshot it had is untouched (see the log)", workspace)
	}
	return out, nil
}

// RefreshPass runs one refresh pass over every workspace, under each one's
// lease: what a scheduled function runs.
func (a *App) RefreshPass(ctx context.Context) (hub.RefreshResult, error) {
	res, err := a.hub.RefreshPass(ctx)
	a.hub.Wait()
	return res, err
}

// resolve finds the workspace by the spelling the store holds it under.
func (a *App) resolve(ctx context.Context, workspace string) (string, error) {
	ids, err := a.hub.Workspaces(ctx)
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		if strings.EqualFold(id, workspace) {
			return id, nil
		}
	}
	a.log.WarnContext(ctx, "a tick for a workspace the module does not hold", logattr.SafeString("workspace", workspace))
	return "", fmt.Errorf("%w: %s", ErrUnknownWorkspace, workspace)
}

// Close releases what New opened.
func (a *App) Close() error {
	err := a.trail.Close()
	a.stores.Close()
	return err
}

// oauthClient is the client that reopens a workspace connected by consent,
// read from the secrets source when first needed and again after [lazy.TTL].
// The module has no console, so the document is the only place the client is
// declared; none declared is an error naming that, raised by the workspace that
// needs it and by no other.
func oauthClient(o *config.GoogleOAuthClient, publicURL string, src secrets.Source) func() (google.OAuthClient, error) {
	if o == nil || o.Provider == "" {
		return func() (google.OAuthClient, error) {
			return google.OAuthClient{}, errors.New("the document declares no google.oauthClient: " +
				"register one in the document to connect by consent, sign in, or reopen a workspace connected that way")
		}
	}
	value := lazy.New(lazy.TTL, func(ctx context.Context) (google.OAuthClient, error) {
		c := google.OAuthClient{ID: o.ID, BaseURL: strings.TrimSuffix(publicURL, "/")}
		var err error
		if c.ID == "" {
			if c.ID, err = src.Get(ctx, secrets.ProviderClientID(o.Provider)); err != nil {
				return google.OAuthClient{}, fmt.Errorf("oauthClient: %w", err)
			}
		}
		if c.Secret, err = src.Get(ctx, secrets.ProviderClientSecret(o.Provider)); err != nil {
			return google.OAuthClient{}, fmt.Errorf("oauthClient: %w", err)
		}
		if c.ID == "" || c.Secret == "" {
			return google.OAuthClient{}, errors.New("oauthClient: the client id and secret must not be empty")
		}
		return c, nil
	})
	return func() (google.OAuthClient, error) {
		// Opening a reader has no request context; the read is bounded.
		ctx, cancel := context.WithTimeout(context.Background(), secretReadTimeout)
		defer cancel()
		return value.Get(ctx)
	}
}

func openGoogle(ctx context.Context, d *hub.Declared) (backend.Backend, error) {
	if d.Backend != "google" {
		return nil, fmt.Errorf("this module reads google, and %q was declared", d.Backend)
	}
	return google.Open(ctx, d.Key, d.Admin)
}
