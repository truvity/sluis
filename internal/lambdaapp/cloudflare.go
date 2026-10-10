package lambdaapp

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	cfapp "github.com/truvity/sluis/internal/cloudflare/app"
	"github.com/truvity/sluis/internal/config"
)

// servesCloudflare says whether the document makes this function the Cloudflare
// module (cloudflare.serve).
func servesCloudflare(file string) (bool, error) {
	svc, err := config.Load[config.Sluis](file)
	if err != nil {
		return false, err
	}
	return svc.Cloudflare != nil && svc.Cloudflare.Serve != nil, nil
}

// openCloudflare assembles the function that runs the Cloudflare module: it
// answers other modules' calls (docs/decisions/0071) and has no HTTP surface.
// The issuer is not assembled here, so this function can never answer as it.
func openCloudflare(ctx context.Context, file string) (*Function, error) {
	cfg, err := cfapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := logger(ctx, "cloudflare-minter", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	a, err := cfapp.New(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	notFound := http.NotFoundHandler()
	return &Function{
		Handler: NewHTTP(notFound, nil, log).WithRPC(a.RPC()).WithCheck(checkFunc(file, config.SecretMinter)),
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
