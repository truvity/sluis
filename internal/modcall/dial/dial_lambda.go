//go:build lambda

package dial

import (
	"context"
	"fmt"

	"github.com/truvity/sluis/storage/awsotel"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/lambdacall"
)

// lambdaCaller invokes the functions cfg names through the class's alias, with
// the function role's credentials.
func lambdaCaller(ctx context.Context, cfg modcall.Config, class string) (modcall.Caller, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("modcall: %w", err)
	}
	return lambdacall.New(awslambda.NewFromConfig(awsCfg, func(o *awslambda.Options) { o.TracerProvider, o.MeterProvider = awsotel.Providers() }), cfg).As(class), nil
}
