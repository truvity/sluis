package rpc

import (
	"context"
	"errors"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/consoleauth"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/lambdacall"
)

// DefaultTokenFile is where the pod's projected token for the backup module is
// mounted when the document does not say.
const DefaultTokenFile = "/var/run/secrets/sluis/backup/token"

// Dial is the [Client] of the modules `console.backup` names, called as the
// caller class `console`: each module's Lambda function (its `live-console`
// alias) or Kubernetes Service. A restore target left out is a module the
// router cannot reach, which the page shows as a restore function it has none of.
func Dial(ctx context.Context, b *config.ConsoleBackup) (*Client, error) {
	if b == nil {
		return nil, errors.New("console.backup is not set")
	}
	cfg := modcall.Config{Modules: map[string]modcall.Target{
		Module: {Function: b.Function, URL: b.URL, Audience: b.Audience},
	}}
	if b.RestoreFunction != "" || b.RestoreURL != "" {
		cfg.Modules[RestoreModule] = modcall.Target{Function: b.RestoreFunction, URL: b.RestoreURL, Audience: b.Audience}
	}
	var lambda, http modcall.Caller
	urls, audiences := map[string]string{}, map[string]string{}
	for name, t := range cfg.Modules {
		if t.Function != "" && lambda == nil {
			awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
			if err != nil {
				return nil, fmt.Errorf("console.backup: %w", err)
			}
			lambda = lambdacall.New(awslambda.NewFromConfig(awsCfg, func(*awslambda.Options) {}), cfg).As(CallerConsole)
		}
		if t.URL != "" {
			urls[name], audiences[name] = t.URL, t.Audience
		}
	}
	if len(urls) > 0 {
		file := b.TokenFile
		if file == "" {
			file = DefaultTokenFile
		}
		src := consoleauth.File(file)
		http = &modcall.HTTPCaller{URLs: urls, Audiences: audiences,
			Token: func(ctx context.Context, _ string) (string, error) { return src.Token(ctx) }}
	}
	router, err := modcall.NewRouter(cfg, nil, lambda, http)
	if err != nil {
		return nil, err
	}
	return NewClient(router), nil
}
