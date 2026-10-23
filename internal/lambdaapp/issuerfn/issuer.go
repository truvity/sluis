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

	"github.com/truvity/sluis/internal/deploycheck"
	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	githubcontroller "github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/lambdaapp"
	outlinecontroller "github.com/truvity/sluis/internal/outlineroster/controller"
	"github.com/truvity/sluis/internal/port/invoke"
	"github.com/truvity/sluis/internal/rosterapp"
	slackapp "github.com/truvity/sluis/internal/slackroster/app"
	slackcontroller "github.com/truvity/sluis/internal/slackroster/controller"
)

func init() { lambdaapp.Register(lambdaapp.ModuleIssuer, open) }

// outlineKind is the controller an Outline workspace's tick names:
// {"kind":"tick","controller":"outline","target":"<key>"}.
const outlineKind = "outline"

// CheckFile is the {"kind":"check"} answer of the function whose document is in
// file: the adapters the issuer builds at start (State and the peers' tables, the
// blobs, the secrets, the trigger, the audit sink and the signing adapter's
// settings), built from the document and refused with the setting that is
// missing; then the secrets it declares. Nothing it reaches mutates.
func CheckFile(file string) func(context.Context) (deploycheck.Report, error) {
	return func(ctx context.Context) (deploycheck.Report, error) {
		cfg, err := rosterapp.Load(file)
		if err != nil {
			return deploycheck.Report{}, err
		}
		return lambdaapp.CheckAdapters(func(ctx context.Context) error { return cfg.Verify(ctx, nil) }, lambdaapp.CheckFunc(file))(ctx)
	}
}

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
	// the controller is assembled for it, in the module's own function when
	// `github.remote` or `slack.remote` says so (rosterapp.Load leaves it out of
	// cfg then). Taken out of the settings before the
	// service is assembled, whose New would run their loops.
	github, slack, outline := cfg.GitHub, cfg.Slack, cfg.Outline
	cfg.GitHub, cfg.Slack, cfg.Outline = nil, nil, nil
	// The KMS signing modes read the state secret and call KMS to open their
	// keys: the first request that needs a key does it, not the start.
	cfg.Issuer.LazySigningKeys = true
	cfg.Directory.ReadsNoSecretAtStart = true
	// A start reads no secret: a herd of cold starts would read each of them in
	// every new environment, and SSM throttles a herd. The secrets are read
	// when the request that needs them arrives, and the schedule's refresh
	// settles the generated clients' (WithRefresh below), as the service is
	// assembled here and never run.
	service, err := rosterapp.New(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	// "Run a pass now" reaches a controller by invoking the `live-console` alias
	// of the module's function, not this one. The plan (adapters.trigger:
	// invoke) built the trigger; it is told which kind a target is, from the
	// policy that declares them.
	set := service.Policy()
	kindsOf := func(target string) []string {
		var kinds []string
		if _, bound := set.Declared().GitHub[target]; bound || target == githubcontroller.LinksTarget {
			kinds = append(kinds, invoke.KindGitHub)
		}
		if set.SlackWorkspaceDeclared(target) {
			kinds = append(kinds, invoke.KindSlack)
		}
		return kinds
	}
	if t, ok := service.Trigger().(*invoke.Trigger); ok {
		t.SetKinds(kindsOf)
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
	if outline != nil {
		// The Outline controller is this function's own (docs/decisions/0077): its
		// schedule invokes the issuer, and the pass asks the console in this
		// process.
		controllers[outlineKind] = &lambdaapp.Controller{Name: outlineKind, Log: log,
			Unknown: func(err error) bool { return errors.Is(err, outlinecontroller.ErrUnknownTarget) },
			Open: func(ctx context.Context) (lambdaapp.Pass, error) {
				return service.Outline(ctx, outline)
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
	// A module that is another function's (`<module>.remote`) is not assembled
	// here: rosterapp.Load left its controller out above, and what is left of the
	// directory is the issuer's reading of the snapshot the Google function keeps.
	// Only a module this process hosts (the demonstration, the tests) is
	// refreshed, rotated or run here.
	hostsDirectory := cfg.Hosted("google")
	if hostsDirectory {
		// There is no loop to keep the directory's snapshots fresh on Lambda: a
		// request that finds one due refreshes it, and a schedule does so between
		// requests ({"kind":"refresh"}). With google.remote the hub only reads the
		// snapshots and both are the Google module's.
		service.UseRequestRefresh(hub.DefaultRequestRefreshTimeout)
	}
	// A target's kinds, for the events this function runs: the trigger's, and
	// the Outline controller's, which no trigger invokes.
	eventKinds := func(target string) []string {
		kinds := kindsOf(target)
		if outline != nil && set.OutlineWorkspaceDeclared(target) {
			kinds = append(kinds, outlineKind)
		}
		return kinds
	}
	http := lambdaapp.NewHTTP(service.Handler(), service.Settle, log).WithControllers(eventKinds, controllers).WithCheck(CheckFile(file))
	if service.Cloudflare() != nil && (cfg.Cloudflare == nil || cfg.Cloudflare.Remote == nil) {
		http.WithCloudflare(func(ctx context.Context) (int, string, error) {
			res, err := service.TickCloudflare(ctx)
			return res.Failed(), res.Summary(), err
		})
	}
	// The issuer's generated client secrets are looked after on a schedule of
	// their own: there is no loop on Lambda, and a failure is logged and counted,
	// never this event's error (the next pass retries).
	clients := func(ctx context.Context) (lambdaapp.RefreshResult, error) {
		service.ReconcileClientSecrets(ctx)
		return lambdaapp.RefreshResult{Kind: lambdaapp.KindClients}, nil //nolint:staticcheck // the issuer's own event; the deprecation is of the old refresh alias
	}
	http.WithClients(clients)
	// {"kind":"refresh"} is the name before "clients", accepted for one release.
	// It reconciles the clients' secrets as it always did, and refreshes the
	// directory only where this process hosts it.
	http.WithRefresh(func(ctx context.Context) (lambdaapp.RefreshResult, error) {
		res, _ := clients(ctx)
		res.Kind = lambdaapp.KindRefresh
		if !hostsDirectory {
			return res, nil
		}
		dir, err := service.RefreshDirectory(ctx)
		res.Workspaces, res.Ran, res.Contended, res.Failed = dir.Workspaces, dir.Ran, dir.Contended, dir.Failed
		return res, err
	})
	return &lambdaapp.Function{
		Handler: http,
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
