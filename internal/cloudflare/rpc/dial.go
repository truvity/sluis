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
	if r == nil {
		return nil, errors.New("cloudflare.remote is not set")
	}
	router, err := dial.Dial(ctx, dial.FromTargets(map[string]config.ModuleRemote{Module: r.Target()}), CallerIssuer, nil)
	if err != nil {
		return nil, err
	}
	return NewClient(router, log), nil
}
