// Package signerdial reaches the signer module from a process that does not
// hold the signing key. It sits outside internal/signer so that the signer,
// which imports internal/signer/rpc to serve, does not carry the Lambda client
// or the token sources.
package signerdial

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
	"github.com/truvity/sluis/internal/signer/rpc"
)

// DefaultTokenFile is where the pod's projected token for the signer module is
// mounted when the document does not say.
const DefaultTokenFile = "/var/run/secrets/sluis/signer/token"

// Dial is the signer in the module the document's `signer.remote` names: its
// Lambda function (the `live` alias) or its Kubernetes Service.
func Dial(ctx context.Context, r *config.SignerRemote, log *slog.Logger) (*rpc.Client, error) {
	if r == nil {
		return nil, errors.New("signer.remote is not set")
	}
	cfg := modcall.Config{Modules: map[string]modcall.Target{rpc.Module: {Function: r.Function, URL: r.URL, Audience: r.Audience}}}
	var lambda, http modcall.Caller
	switch {
	case r.Function != "":
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("signer.remote: %w", err)
		}
		lambda = lambdacall.New(awslambda.NewFromConfig(awsCfg, func(*awslambda.Options) {}), cfg)
	case r.URL != "":
		file := r.TokenFile
		if file == "" {
			file = DefaultTokenFile
		}
		src := consoleauth.File(file)
		http = &modcall.HTTPCaller{
			URLs:      map[string]string{rpc.Module: r.URL},
			Audiences: map[string]string{rpc.Module: r.Audience},
			Token:     func(ctx context.Context, _ string) (string, error) { return src.Token(ctx) },
		}
	}
	router, err := modcall.NewRouter(cfg, nil, lambda, http)
	if err != nil {
		return nil, err
	}
	return rpc.NewClient(router, log), nil
}
