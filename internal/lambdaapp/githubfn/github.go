// Package githubfn is the GitHub module's controller function: the
// organisations' team passes and the links pass, on a schedule (`tick`) or when
// the console asks (`run`). It has no HTTP surface and serves no module call:
// the installation-token mint is another function (internal/lambdaapp/githubmintfn),
// so a controller pass and a mint never share a role or a concurrency limit.
// It registers itself with internal/lambdaapp; cmd/sluis-github and the lambda
// build of cmd/sluis import it.
package githubfn

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	githubcontroller "github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/port/invoke"
)

func init() { lambdaapp.Register(lambdaapp.ModuleGitHub, open) }

// Kinds are the kinds of secret the module's check reads. The module declares
// none: an App's credential is made at run time, and the policy's client
// secrets and the directory's keys are the issuer's and the Google module's.
var Kinds []string

// Handler is the function's entry: tick and run events for the targets
// declared says are the policy's, opening one pass per invocation; check is
// the {"kind":"check"} answer. It answers no module call and no HTTP request.
func Handler(log *slog.Logger, declared func(target string) bool, open func(context.Context) (lambdaapp.Pass, error),
	check func(context.Context) (deploycheck.Report, error)) *lambdaapp.HTTP {
	kindOf := func(target string) string {
		if declared(target) {
			return invoke.KindGitHub
		}
		return ""
	}
	controllers := map[string]*lambdaapp.Controller{
		invoke.KindGitHub: {Name: "github", Log: log, Open: open,
			Unknown: func(err error) bool { return errors.Is(err, githubcontroller.ErrUnknownTarget) }},
	}
	return lambdaapp.NewHTTP(http.NotFoundHandler(), nil, log).WithControllers(kindOf, controllers).WithCheck(check)
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
	log, flush, err := lambdaapp.Logger(ctx, "github-controller", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	return &lambdaapp.Function{
		Handler: Handler(log, cfg.Declares, func(ctx context.Context) (lambdaapp.Pass, error) {
			// Assembled per invocation and closed at its end (lambdaapp.Controller):
			// the audit queue is flushed by closing it.
			app, err := githubapp.New(ctx, cfg, log)
			if err != nil {
				return nil, err
			}
			return app, nil
		}, lambdaapp.CheckOwn(doc.Service.Secrets, doc.Policy, Kinds...)),
		Flush: flush,
		Close: func() {},
	}, nil
}
