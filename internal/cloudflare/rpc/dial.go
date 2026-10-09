package rpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/consoleauth"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/lambdacall"
)

// DefaultTokenFile is where the pod's projected token for the Cloudflare module
// is mounted when the document does not say.
const DefaultTokenFile = "/var/run/secrets/sluis/cloudflare/token"

// Dial is the [Minting] in the module the document's `cloudflare.remote` names:
// its Lambda function (the `live` alias) or its Kubernetes Service.
func Dial(ctx context.Context, r *config.CloudflareRemote, log *slog.Logger) (*Client, error) {
	if r == nil {
		return nil, errors.New("cloudflare.remote is not set")
	}
	cfg := modcall.Config{Modules: map[string]modcall.Target{Module: {Function: r.Function, URL: r.URL, Audience: r.Audience}}}
	var lambda, http modcall.Caller
	switch {
	case r.Function != "":
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloudflare.remote: %w", err)
		}
		lambda = lambdacall.New(awslambda.NewFromConfig(awsCfg, func(*awslambda.Options) {}), cfg)
	case r.URL != "":
		file := r.TokenFile
		if file == "" {
			file = DefaultTokenFile
		}
		src := consoleauth.File(file)
		http = &modcall.HTTPCaller{
			URLs:      map[string]string{Module: r.URL},
			Audiences: map[string]string{Module: r.Audience},
			Token:     func(ctx context.Context, _ string) (string, error) { return src.Token(ctx) },
		}
	}
	router, err := modcall.NewRouter(cfg, nil, lambda, http)
	if err != nil {
		return nil, err
	}
	return NewClient(router, log), nil
}
