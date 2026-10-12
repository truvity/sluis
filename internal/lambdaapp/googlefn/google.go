// Package googlefn is the Google module's function: the directory refresh, on
// a schedule ({"kind":"refresh"}, every workspace) or for one workspace
// ({"kind":"refresh","workspace":"<id>"}), the check of its own secrets, and the
// module's calls: the console's Workspace service (`console.*`, caller class
// `console`) and the sign-in (`identify`, `sign_in_url`, caller class
// `issuer`). It has no HTTP surface. The issuer reads the snapshot the refresh
// writes (D-v176-6 a) and calls the module for everything else.
//
// The sign-in is a call to this function on every login. It has no provisioned
// concurrency (D-v176-11 a), so the first sign-in after the function has been
// idle pays its cold start: the function opens its storage, its secrets and
// every workspace's reader before it answers. That cost is accepted and
// documented (docs/reference/lambda.md); the issuer shows a clear page when the
// call does not finish. It registers itself with internal/lambdaapp;
// cmd/sluis-google and the lambda build of cmd/sluis import it.
package googlefn

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	googleapp "github.com/truvity/sluis/internal/googleroster/app"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/modcall"
)

func init() { lambdaapp.Register(lambdaapp.ModuleGoogle, open) }

// Kinds are the kinds of secret the module's check reads: the workspaces' keys
// and the OAuth client that reopens a workspace connected by consent. The
// issuer's keys, the sign-in client of the issuer's own document and the
// policy's client secrets are not the module's, and its check does not report
// them.
var Kinds = []string{config.SecretWorkspaceKey, config.SecretSignInClientID, config.SecretSignInClientSecret}

// Handler is the function's entry: refresh events, of every workspace (workspace
// is empty) or of one, and the {"kind":"check"} answer. It answers no tick, no
// run, no module call and no HTTP request.
func Handler(log *slog.Logger, refresh func(ctx context.Context, workspace string) (lambdaapp.RefreshResult, error),
	check func(context.Context) (deploycheck.Report, error)) *lambdaapp.HTTP {
	return lambdaapp.NewHTTP(http.NotFoundHandler(), nil, log).
		WithRefresh(func(ctx context.Context) (lambdaapp.RefreshResult, error) { return refresh(ctx, "") }).
		WithRefreshWorkspace(refresh).
		WithCheck(check)
}

// WithCalls adds the module's calls to a handler: the server answers
// `google.console.*` to the class `console` and `google.identify` and
// `google.sign_in_url` to `issuer`.
func WithCalls(h *lambdaapp.HTTP, s *modcall.Server) *lambdaapp.HTTP { return h.WithRPC(s) }

// calls is the service the module's calls run, assembled by the first call of a
// container and kept for the rest of its life. It is assembled lazily: a
// scheduled refresh opens its own, per invocation, and needs none of this.
type calls struct {
	cfg googleapp.Config
	log *slog.Logger

	mu  sync.Mutex
	app *googleapp.App
}

// service is the console's service, opening the module on the first call. A
// failure is not kept: the next call tries again.
func (c *calls) service(ctx context.Context) (*googleconsole.Service, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.app == nil {
		app, err := googleapp.New(ctx, c.cfg, c.log)
		if err != nil {
			return nil, err
		}
		c.app = app
	}
	return c.app.Console(), nil
}

// flush delivers the audit records the calls queued.
func (c *calls) flush(ctx context.Context) {
	c.mu.Lock()
	app := c.app
	c.mu.Unlock()
	if app == nil {
		return
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := app.Flush(fctx); err != nil {
		c.log.WarnContext(ctx, "audit records were not delivered before the response", slog.Any("error", err))
	}
}

func (c *calls) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.app != nil {
		_ = c.app.Close()
		c.app = nil
	}
}

// Server is the server of the module's calls over the service open assembles.
func Server(log *slog.Logger, open func(context.Context) (*googleconsole.Service, error)) *modcall.Server {
	s := modcall.NewServer(googleconsole.Module)
	googleconsole.RegisterLazy(s, log, open)
	return s
}

// Check is the {"kind":"check"} answer for the document: the secrets it
// declares the module reads, of the module's own kinds, read from SSM.
func Check(doc *config.SluisGoogle) func(context.Context) (deploycheck.Report, error) {
	return func(ctx context.Context) (deploycheck.Report, error) {
		if doc.Secrets == nil {
			return deploycheck.Report{}, errors.New("secrets: required (the section the function reads its secrets store from)")
		}
		return deploycheck.CheckDeclared(ctx, doc.Secrets, deploycheck.Own(doc.DeclaredSecrets(), Kinds...))
	}
}

// CheckFile is the {"kind":"check"} answer of the function whose document is in
// file: the adapters the function builds at start and in a refresh tick (State,
// the blobs, the secrets, the audit sink), built from the document and refused
// with the setting that is missing; then the secrets it declares. Nothing it
// reaches mutates. The document is read again when asked, which is once per
// deploy.
func CheckFile(file string) func(context.Context) (deploycheck.Report, error) {
	return func(ctx context.Context) (deploycheck.Report, error) {
		cfg, err := googleapp.Load(file)
		if err != nil {
			return deploycheck.Report{}, err
		}
		doc, err := config.LoadGoogle(file)
		if err != nil {
			return deploycheck.Report{}, err
		}
		return lambdaapp.CheckAdapters(func(ctx context.Context) error { return cfg.Verify(ctx, nil) }, Check(doc))(ctx)
	}
}

func open(ctx context.Context, file string) (*lambdaapp.Function, error) {
	cfg, err := googleapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := lambdaapp.Logger(ctx, "google-roster", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	served := &calls{cfg: cfg, log: log}
	return &lambdaapp.Function{
		Handler: WithCalls(Handler(log, func(ctx context.Context, workspace string) (lambdaapp.RefreshResult, error) {
			// Assembled per invocation and closed at its end: nothing is left
			// running in a frozen process.
			app, err := googleapp.New(ctx, cfg, log)
			if err != nil {
				return lambdaapp.RefreshResult{}, err
			}
			defer func() { _ = app.Close() }()
			return Refresh(ctx, app, workspace)
		}, CheckFile(file)), Server(log, served.service)),
		Flush: func(ctx context.Context) {
			served.flush(ctx)
			flush(ctx)
		},
		Close: served.close,
	}, nil
}

// Puller is the part of the assembled module a refresh drives.
type Puller interface {
	Pass(ctx context.Context, workspace string, unsafeLocal bool) (hub.RefreshOutcome, error)
	RefreshPass(ctx context.Context) (hub.RefreshResult, error)
}

// Refresh runs one refresh over every workspace (workspace is empty) or the one
// named. The leases are in the module's table, shared by every invocation, so
// the pass needs no opt-in for a lease held in the process's own memory.
func Refresh(ctx context.Context, p Puller, workspace string) (lambdaapp.RefreshResult, error) {
	if workspace == "" {
		res, err := p.RefreshPass(ctx)
		return lambdaapp.RefreshResult{Kind: lambdaapp.KindRefresh, Workspaces: res.Workspaces, Ran: res.Ran,
			Contended: res.Contended, Failed: res.Failed, Paused: res.Paused}, err
	}
	out, err := p.Pass(ctx, workspace, false)
	res := lambdaapp.RefreshResult{Kind: lambdaapp.KindRefresh, Workspaces: 1}
	switch {
	case errors.Is(err, googleapp.ErrUnknownWorkspace):
		// Not a failed refresh: nothing was asked of any directory.
		return lambdaapp.RefreshResult{}, err
	case err != nil:
		// The error names the cause; the invocation fails with it.
		res.Failed = 1
		return res, err
	case out == hub.RefreshContended:
		res.Contended = 1
	case out == hub.RefreshPaused:
		res.Paused = 1
	default:
		res.Ran = 1
	}
	return res, nil
}
