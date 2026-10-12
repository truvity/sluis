// Package cloudflarefn is the Cloudflare module's Lambda function: it answers
// other modules' calls and has no HTTP surface. It registers itself with
// internal/lambdaapp; cmd/sluis-cloudflare and the lambda build of cmd/sluis import it.
package cloudflarefn

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	cfapp "github.com/truvity/sluis/internal/cloudflare/app"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/lambdaapp"
)

func init() { lambdaapp.Register(lambdaapp.ModuleCloudflare, openCloudflare) }

// CheckFile is the {"kind":"check"} answer of the function whose document is in
// file: the adapters the module builds (State, the secrets store the minter
// credential lives in, the blobs, the audit sink), built from the document and
// refused with the setting that is missing; then the minter secret. Nothing it
// reaches mutates and nothing is minted.
func CheckFile(file string) func(context.Context) (deploycheck.Report, error) {
	return func(ctx context.Context) (deploycheck.Report, error) {
		cfg, err := cfapp.Load(file)
		if err != nil {
			return deploycheck.Report{}, err
		}
		return lambdaapp.CheckAdapters(func(ctx context.Context) error { return cfg.Verify(ctx, nil) },
			lambdaapp.CheckFunc(file, config.SecretMinter))(ctx)
	}
}

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
		Handler: lambdaapp.NewHTTP(notFound, nil, log).WithRPC(a.RPC()).WithCheck(CheckFile(file)),
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
