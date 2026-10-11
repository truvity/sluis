// Package slackfn is the Slack module's controller function: the workspaces'
// user-group passes, on a schedule (`tick`) or when the console asks (`run`).
// It has no HTTP surface and serves no module call. It registers itself with
// internal/lambdaapp; cmd/sluis-slack and the lambda build of cmd/sluis import
// it.
package slackfn

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/port/invoke"
	slackapp "github.com/truvity/sluis/internal/slackroster/app"
	slackcontroller "github.com/truvity/sluis/internal/slackroster/controller"
)

func init() { lambdaapp.Register(lambdaapp.ModuleSlack, open) }

// Kinds are the kinds of secret the module's check reads. The module declares
// none: a workspace's credential is made at run time, and the policy's client
// secrets and the directory's keys are the issuer's and the Google module's.
var Kinds []string

// Handler is the function's entry: tick and run events for the targets
// declared says are the policy's, opening one pass per invocation; check is
// the {"kind":"check"} answer. It answers no module call and no HTTP request.
func Handler(log *slog.Logger, declared func(target string) bool, open func(context.Context) (lambdaapp.Pass, error),
	check func(context.Context) (deploycheck.Report, error)) *lambdaapp.HTTP {
	kindsOf := func(target string) []string {
		if declared(target) {
			return []string{invoke.KindSlack}
		}
		return nil
	}
	controllers := map[string]*lambdaapp.Controller{
		invoke.KindSlack: {Name: "slack", Log: log, Open: open,
			Unknown: func(err error) bool { return errors.Is(err, slackcontroller.ErrUnknownTarget) }},
	}
	return lambdaapp.NewHTTP(http.NotFoundHandler(), nil, log).WithControllers(kindsOf, controllers).WithCheck(check)
}

func open(ctx context.Context, file string) (*lambdaapp.Function, error) {
	cfg, err := slackapp.Load(file)
	if err != nil {
		return nil, err
	}
	doc, err := config.LoadConfig[config.ControllerSlack](file, nil)
	if err != nil {
		return nil, err
	}
	log, flush, err := lambdaapp.Logger(ctx, "slack-controller", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	return &lambdaapp.Function{
		Handler: Handler(log, cfg.Declares, func(ctx context.Context) (lambdaapp.Pass, error) {
			// Assembled per invocation and closed at its end (lambdaapp.Controller):
			// the audit queue is flushed by closing it.
			app, err := slackapp.New(ctx, cfg, log)
			if err != nil {
				return nil, err
			}
			return app, nil
		}, lambdaapp.CheckOwn(doc.Service.Secrets, doc.Policy, Kinds...)),
		Flush: flush,
		Close: func() {},
	}, nil
}
