// Package cloudflarefn is the Cloudflare module's Lambda function: it answers
// other modules' calls and has no HTTP surface. It registers itself with
// internal/lambdaapp; cmd/sluis-cloudflare and the deprecated cmd/sluis-lambda import it.
package cloudflarefn

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	cfapp "github.com/truvity/sluis/internal/cloudflare/app"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/lambdaapp"
)

func init() { lambdaapp.Register(lambdaapp.ModuleCloudflare, openCloudflare) }

// openCloudflare assembles the function that runs the Cloudflare module: it
// answers other modules' calls (docs/decisions/0071) and has no HTTP surface.
// The issuer is not assembled here, so this function can never answer as it.
func openCloudflare(ctx context.Context, file string) (*lambdaapp.Function, error) {
	cfg, err := cfapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := lambdaapp.Logger(ctx, "cloudflare-minter", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	a, err := cfapp.New(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	notFound := http.NotFoundHandler()
	return &lambdaapp.Function{
		Handler: lambdaapp.NewHTTP(notFound, nil, log).WithRPC(a.RPC()).WithCheck(lambdaapp.CheckFunc(file, config.SecretMinter)),
		Flush: func(ctx context.Context) {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := a.Flush(fctx); err != nil {
				log.WarnContext(ctx, "audit records were not delivered before the response", slog.Any("error", err))
			}
			flush(ctx)
		},
		Close: func() { _ = a.Close() },
	}, nil
}
