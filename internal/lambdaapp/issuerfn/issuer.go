// Package issuerfn is the issuer module's Lambda function: the issuer, the
// signer and the console in one process (docs/decisions/0072), assembled from
// the same pieces the Kubernetes process is. It registers itself with
// internal/lambdaapp; cmd/sluis-issuer and the lambda build of cmd/sluis
// import it.
package issuerfn

import (
	"context"
	"errors"
	"log/slog"
	"time"

	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	githubcontroller "github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/port/invoke"
	"github.com/truvity/sluis/internal/rosterapp"
	slackapp "github.com/truvity/sluis/internal/slackroster/app"
	slackcontroller "github.com/truvity/sluis/internal/slackroster/controller"
)

func init() { lambdaapp.Register(lambdaapp.ModuleIssuer, open) }

func open(ctx context.Context, file string) (*lambdaapp.Function, error) {
	// The KMS signer's state secret (signingKey.kms.stateSecret) is a secret
	// like any other: the document names it, and its `secrets` source (ssm)
	// reads /sluis/<instance>/internal/config/issuer/state-secret.
	cfg, err := rosterapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := lambdaapp.Logger(ctx, "access-issuer", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	// The controllers do not loop on Lambda: each pass is an invocation, and
	// the controller is assembled for it. Taken out of the settings before the
	// service is assembled, whose New would run their loops.
	github, slack := cfg.GitHub, cfg.Slack
	cfg.GitHub, cfg.Slack = nil, nil
	// The KMS signing modes read the state secret and call KMS to open their
	// keys: the first request that needs a key does it, not the start.
	cfg.Issuer.LazySigningKeys = true
	// A start reads no secret: a herd of cold starts would read each of them in
	// every new environment, and SSM throttles a herd. The secrets are read
	// when the request that needs them arrives, and the schedule's refresh
	// settles the generated clients' (WithRefresh below), as the service is
	// assembled here and never run.
	service, err := rosterapp.New(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	// "Run a pass now" reaches the controllers by invoking this function. The
	// plan (adapters.trigger: invoke) built the trigger; it is told which kind a
	// target is, from the policy that declares them.
	set := service.Policy()
	kindOf := func(target string) string {
		if set.SlackWorkspaceDeclared(target) {
			return invoke.KindSlack
		}
		if _, bound := set.Declared().GitHub[target]; bound || target == githubcontroller.LinksTarget {
			return invoke.KindGitHub
		}
		return ""
	}
	if t, ok := service.Trigger().(*invoke.Trigger); ok {
		t.SetKind(kindOf)
	} else {
		log.WarnContext(ctx, "run-now cannot reach the controllers: adapters.trigger is not invoke, so a console write "+
			"is picked up at the controller's next scheduled tick")
	}
	controllers := map[string]*lambdaapp.Controller{}
	if github != nil {
		controllers[invoke.KindGitHub] = &lambdaapp.Controller{Name: "github", Log: log,
			Unknown: func(err error) bool { return errors.Is(err, githubcontroller.ErrUnknownTarget) },
			Open: func(ctx context.Context) (lambdaapp.Pass, error) {
				app, err := githubapp.New(ctx, *github, log)
				if err != nil {
					return nil, err
				}
				return app, nil
			}}
	}
	if slack != nil {
		controllers[invoke.KindSlack] = &lambdaapp.Controller{Name: "slack", Log: log,
			Unknown: func(err error) bool { return errors.Is(err, slackcontroller.ErrUnknownTarget) },
			Open: func(ctx context.Context) (lambdaapp.Pass, error) {
				app, err := slackapp.New(ctx, *slack, log)
				if err != nil {
					return nil, err
				}
				return app, nil
			}}
	}
	// There is no loop to keep the directory's snapshots fresh on Lambda: a
	// request that finds one due refreshes it, and a schedule does so between
	// requests ({"kind":"refresh"}).
	service.UseRequestRefresh(hub.DefaultRequestRefreshTimeout)
	http := lambdaapp.NewHTTP(service.Handler(), service.Settle, log).WithControllers(kindOf, controllers).WithCheck(lambdaapp.CheckFunc(file))
	if service.Cloudflare() != nil {
		http.WithCloudflare(func(ctx context.Context) (int, string, error) {
			res, err := service.TickCloudflare(ctx)
			return res.Failed(), res.Summary(), err
		})
	}
	return &lambdaapp.Function{
		Handler: http.
			WithRefresh(func(ctx context.Context) (lambdaapp.RefreshResult, error) {
				// The issuer's generated client secrets are looked after on the
				// same schedule: there is no loop on Lambda and no schedule of
				// their own, and a failure is logged and counted, never this
				// refresh's error (the next pass retries).
				service.ReconcileClientSecrets(ctx)
				res, err := service.RefreshDirectory(ctx)
				return lambdaapp.RefreshResult{Kind: lambdaapp.KindRefresh, Workspaces: res.Workspaces, Ran: res.Ran, Contended: res.Contended, Failed: res.Failed}, err
			}),
		Flush: func(ctx context.Context) {
			// Before the invocation returns: the records still queued for the audit
			// sink would otherwise wait for the next one.
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := service.FlushAudit(fctx); err != nil {
				log.WarnContext(ctx, "audit records were not delivered before the response", slog.Any("error", err))
			}
			flush(ctx)
		},
		Close: service.Close,
	}, nil
}
