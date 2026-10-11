// Package githubmintfn is the GitHub module's mint-only function
// (docs/decisions/0071, D-v176-7 b): the installation-token mint the issuer
// asks for over modcall, in a function of its own. It serves the `github`
// module's `mint_installation` method to the issuer's class and nothing else:
// it links no controller, so it never runs a pass, and a tick or a run-now
// event is refused. The controller function (internal/lambdaapp/githubfn) in
// turn does not serve the mint, so a token request never queues behind a
// controller pass and the two have separate roles and concurrency limits.
//
// Both read the GitHub module's document (controller-github); which one a
// function is is the zip's (cmd/sluis-github-mint), not the document's.
package githubmintfn

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/modcall"
)

func init() { lambdaapp.Register(lambdaapp.ModuleGitHubMint, open) }

// Kinds are the kinds of secret the function's check reads: none. The App keys
// are made by the console at run time and no document names them.
var Kinds []string

// Handler is the function's entry: module calls through rpc, and the check. No
// controllers are given, so a tick or a run event has nothing to run.
func Handler(log *slog.Logger, rpc *modcall.Server, check func(context.Context) (deploycheck.Report, error)) *lambdaapp.HTTP {
	return lambdaapp.NewHTTP(http.NotFoundHandler(), nil, log).WithRPC(rpc).WithCheck(check)
}

func open(ctx context.Context, file string) (*lambdaapp.Function, error) {
	cfg, err := githubapp.Load(file)
	if err != nil {
		return nil, err
	}
	doc, err := config.LoadConfig[config.ControllerGitHub](file, nil)
	if err != nil {
		return nil, err
	}
	log, flush, err := lambdaapp.Logger(ctx, "github-mint", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	m, err := githubapp.NewMinter(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	return &lambdaapp.Function{
		Handler: Handler(log, m.RPC(), lambdaapp.CheckOwn(doc.Service.Secrets, doc.Policy, Kinds...)),
		Flush:   flush,
		Close:   m.Close,
	}, nil
}
