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
	"github.com/truvity/sluis/internal/modcall"
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
	return &lambdaapp.Function{
		Handler: handler(a.RPC(), a.TickAll, CheckFile(file), log),
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

// handler is the function's handler: the module's calls, the deploy check, and
// the rotation schedule's {"kind":"cloudflare"} event, which is this function's
// to answer since the issuer no longer hosts the module (docs/decisions/0071).
func handler(rpc *modcall.Server, tick func(context.Context) (failed int, summary string, err error),
	check func(context.Context) (deploycheck.Report, error), log *slog.Logger,
) *lambdaapp.HTTP {
	return lambdaapp.NewHTTP(http.NotFoundHandler(), nil, log).WithRPC(rpc).WithCheck(check).WithCloudflare(tick)
}
