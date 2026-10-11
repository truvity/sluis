package rpc

import (
	"context"
	"errors"
	"log/slog"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/modcall/dial"
)

// DefaultTokenFile is where the pod's projected token for the Cloudflare module
// is mounted when the document does not say.
const DefaultTokenFile = "/var/run/secrets/sluis/cloudflare/token"

// Dial is the [Minting] in the module the document's `cloudflare.remote` names:
// its Lambda function (the `live-issuer` alias) or its Kubernetes Service.
func Dial(ctx context.Context, r *config.CloudflareRemote, log *slog.Logger) (*Client, error) {
	return DialAs(ctx, r, CallerIssuer, log)
}

// DialAs is [Dial] calling as another caller class: the console calls as
// [CallerConsole], and the module refuses its methods to any other. On Lambda
// the class picks the function's alias (`live-<class>`); on Kubernetes it is
// the class of the token's subject, so a process that is both must hold a
// token mapped to each class it calls as (a TokenFile per class).
func DialAs(ctx context.Context, r *config.CloudflareRemote, class string, log *slog.Logger) (*Client, error) {
	if r == nil {
		return nil, errors.New("cloudflare.remote is not set")
	}
	router, err := dial.Dial(ctx, dial.FromTargets(map[string]config.ModuleRemote{Module: r.Target()}), class, nil)
	if err != nil {
		return nil, err
	}
	return NewClient(router, log), nil
}
