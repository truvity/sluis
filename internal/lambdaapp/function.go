// Package lambdaapp runs sluis as AWS Lambda functions: one binary, one zip,
// three functions, the role chosen by SLUIS_ROLE (docs/integrations/aws-lambda.md).
//
//	http    the issuer and the console behind an API Gateway HTTP API
//	github  the GitHub roster controller: one pass of one organisation per invocation
//	slack   the Slack roster controller: one pass of one workspace per invocation
//
// This package assembles each role from the same pieces the Kubernetes process
// is assembled from (internal/rosterapp and the controllers' apps), and adds the
// two things Lambda needs: translating the platform's events, and reading
// secrets from SSM at cold start. It names no cluster, NATS or Valkey client,
// and cmd/sluis-lambda's import guard keeps it so.
package lambdaapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/truvity/sluis/internal/config"
	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	githubcontroller "github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/port/invoke"
	"github.com/truvity/sluis/internal/rosterapp"
	slackapp "github.com/truvity/sluis/internal/slackroster/app"
	slackcontroller "github.com/truvity/sluis/internal/slackroster/controller"
	"github.com/truvity/sluis/internal/telemetry"
)

// The roles, the values of SLUIS_ROLE.
const (
	RoleHTTP   = "http"
	RoleGitHub = "github"
	RoleSlack  = "slack"
)

// The environment of a function. Everything else is the configuration
// documents', OpenTelemetry's own OTEL_* variables, or the platform's: SLUIS_CONFIG
// (config.EnvConfig) names the service document, in the configuration layer
// mounted at /opt/sluis, and the secrets it names are read from SSM by the
// `secrets` source it declares.
const (
	// EnvRole is the function's role: http, github or slack. Required.
	EnvRole = "SLUIS_ROLE"
)

// Function is a role, assembled at cold start and ready to be invoked.
type Function struct {
	Role    string
	Handler Handler
	// Flush sends what telemetry holds; the platform freezes the process when
	// an invocation returns, and a batch waiting for its timer waits for the
	// next invocation.
	Flush func(context.Context)
	// Close releases what the role holds, at shutdown.
	Close func()
}

// Open assembles the role the environment names. getenv is the process's.
func Open(ctx context.Context, getenv func(string) string) (*Function, error) {
	role := strings.TrimSpace(getenv(EnvRole))
	file := strings.TrimSpace(getenv(config.EnvConfig))
	if role != RoleHTTP && role != RoleGitHub && role != RoleSlack {
		return nil, fmt.Errorf("%s is %q: it is %q, %q or %q", EnvRole, role, RoleHTTP, RoleGitHub, RoleSlack)
	}
	if file == "" {
		return nil, fmt.Errorf("%s is unset: it names the service document, /opt/sluis/<role>.yaml in the configuration layer", config.EnvConfig)
	}
	schema := map[string]string{RoleHTTP: "serve", RoleGitHub: "controller-github", RoleSlack: "controller-slack"}[role]
	// A retired variable that is still set is a deployment that believes it is
	// configuring something.
	if err := config.RefuseRetired(schema, os.Environ()); err != nil {
		return nil, err
	}
	switch role {
	case RoleHTTP:
		return openHTTP(ctx, file)
	case RoleGitHub:
		return openGitHub(ctx, file)
	}
	return openSlack(ctx, file)
}

// SEAM (adapters): the `ssm` secrets, `kms` signing and `sqs` audit adapters
// register themselves in their packages' init; this package imports them (blank,
// beside the imports above) once they land, so that the aws-serverless preset
// resolves in the Lambda binary.

// logger is the JSON logger a function writes to stdout, which CloudWatch Logs
// (and the OTLP layer's log pipeline) collects.
func logger(ctx context.Context, service string, level slog.Level) (*slog.Logger, func(context.Context), error) {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	// The shutdown is not kept: a function is never shut down in order, the
	// platform freezes and ends it, so what matters is [forceFlush].
	if _, err := telemetry.Start(ctx, service, log); err != nil {
		return nil, nil, err
	}
	return log, forceFlush, nil
}

// forceFlush sends the batches the global providers hold, bounded.
func forceFlush(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	type flusher interface{ ForceFlush(context.Context) error }
	if p, ok := any(otel.GetMeterProvider()).(flusher); ok {
		_ = p.ForceFlush(ctx)
	}
	if p, ok := any(otel.GetTracerProvider()).(flusher); ok {
		_ = p.ForceFlush(ctx)
	}
}

func openHTTP(ctx context.Context, file string) (*Function, error) {
	// The KMS signer's state secret (signingKey.kms.stateSecret) is a secret
	// like any other: the document names it, and its `secrets` source (ssm)
	// reads /sluis/<instance>/private/config/issuer/state-secret.
	cfg, err := rosterapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := logger(ctx, "access-issuer", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	service, err := rosterapp.New(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	// "Run a pass now" reaches the controllers by invoking them. The plan
	// (adapters.trigger: invoke) built the trigger; it is told which kind a
	// target is, from the policy that declares them.
	if t, ok := service.Trigger().(*invoke.Trigger); ok {
		set := service.Policy()
		t.SetKind(func(target string) string {
			if set.SlackWorkspaceDeclared(target) {
				return invoke.KindSlack
			}
			if _, bound := set.Declared().GitHub[target]; bound || target == githubcontroller.LinksTarget {
				return invoke.KindGitHub
			}
			return ""
		})
	} else {
		log.WarnContext(ctx, "run-now cannot reach the controllers: adapters.trigger is not invoke, so a console write "+
			"is picked up at the controller's next scheduled tick")
	}
	// There is no loop to keep the directory's snapshots fresh on Lambda: a
	// request that finds one due refreshes it, and a schedule does so between
	// requests ({"kind":"refresh"}).
	service.UseRequestRefresh(hub.DefaultRequestRefreshTimeout)
	return &Function{
		Role: RoleHTTP,
		Handler: NewHTTP(service.Handler(), service.Settle, log).WithRefresh(func(ctx context.Context) (RefreshResult, error) {
			res, err := service.RefreshDirectory(ctx)
			return RefreshResult{Kind: KindRefresh, Workspaces: res.Workspaces, Ran: res.Ran, Contended: res.Contended, Failed: res.Failed}, err
		}).WithExports(func(ctx context.Context) (ExportsResult, error) {
			res, declared := service.ExportsPass(ctx)
			out := ExportsResult{Kind: KindExports, Outcome: "ran", Exports: res.Exports, Done: res.Done, Contended: res.Contended, Failed: res.Failed}
			switch {
			case !declared:
				out.Outcome = "none"
			case res.Failed > 0:
				out.Outcome = "failed"
			}
			return out, nil
		}),
		Flush: func(ctx context.Context) {
			// Before the invocation returns: the records still queued for the audit
			// sink would otherwise wait for the next one.
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := service.FlushAudit(fctx); err != nil {
				log.WarnContext(ctx, "audit records were not delivered before the response", "error", err)
			}
			flush(ctx)
		},
		Close: service.Close,
	}, nil
}

func openGitHub(ctx context.Context, file string) (*Function, error) {
	cfg, err := githubapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := logger(ctx, "github-roster", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	return &Function{
		Role: RoleGitHub,
		Handler: &Controller{Name: "github", Log: log,
			Unknown: func(err error) bool { return errors.Is(err, githubcontroller.ErrUnknownTarget) }, Open: func(ctx context.Context) (Pass, error) {
				app, err := githubapp.New(ctx, cfg, log)
				if err != nil {
					return nil, err
				}
				return app, nil
			}},
		Flush: flush,
		Close: func() {},
	}, nil
}

func openSlack(ctx context.Context, file string) (*Function, error) {
	cfg, err := slackapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := logger(ctx, "slack-roster", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	return &Function{
		Role: RoleSlack,
		Handler: &Controller{Name: "slack", Log: log,
			Unknown: func(err error) bool { return errors.Is(err, slackcontroller.ErrUnknownTarget) }, Open: func(ctx context.Context) (Pass, error) {
				app, err := slackapp.New(ctx, cfg, log)
				if err != nil {
					return nil, err
				}
				return app, nil
			}},
		Flush: flush,
		Close: func() {},
	}, nil
}
