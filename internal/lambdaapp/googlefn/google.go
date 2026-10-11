// Package googlefn is the Google module's function: the directory refresh, on
// a schedule ({"kind":"refresh"}, every workspace) or for one workspace
// ({"kind":"refresh","workspace":"<id>"}), and the check of its own secrets. It
// has no HTTP surface and serves no module call: the issuer reads the snapshot
// the refresh writes (D-v176-6 a) and calls nothing here. It registers itself
// with internal/lambdaapp; cmd/sluis-google and the lambda build of cmd/sluis
// import it.
package googlefn

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	googleapp "github.com/truvity/sluis/internal/googleroster/app"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/lambdaapp"
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

func open(ctx context.Context, file string) (*lambdaapp.Function, error) {
	cfg, err := googleapp.Load(file)
	if err != nil {
		return nil, err
	}
	doc, err := config.LoadGoogle(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := lambdaapp.Logger(ctx, "google-roster", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	return &lambdaapp.Function{
		Handler: Handler(log, func(ctx context.Context, workspace string) (lambdaapp.RefreshResult, error) {
			// Assembled per invocation and closed at its end: nothing is left
			// running in a frozen process.
			app, err := googleapp.New(ctx, cfg, log)
			if err != nil {
				return lambdaapp.RefreshResult{}, err
			}
			defer func() { _ = app.Close() }()
			return Refresh(ctx, app, workspace)
		}, Check(doc)),
		Flush: flush,
		Close: func() {},
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
