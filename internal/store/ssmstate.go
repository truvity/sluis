package store

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/truvity/sluis/internal/awsretry"
	"github.com/truvity/sluis/storage/state"
	ssmstate "github.com/truvity/sluis/storage/state/ssm"
)

// openSSMState is storage/state/ssm.Open with the service's retryer
// (internal/awsretry): the v4 stores are read at a cold start too, and a herd
// of cold starts is what SSM throttles.
func openSSMState(ctx context.Context, prefix string, opts ...state.Option) (state.Store, error) {
	o := state.ResolveOptions(state.Options{}, opts...)
	var load []func(*awsconfig.LoadOptions) error
	if o.Region != "" {
		load = append(load, awsconfig.WithRegion(o.Region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, load...)
	if err != nil {
		return nil, fmt.Errorf("ssm: load AWS config: %w", err)
	}
	client := awsssm.NewFromConfig(cfg, func(so *awsssm.Options) {
		so.Retryer = awsretry.New()
		if o.Endpoint != "" {
			so.BaseEndpoint = aws.String(o.Endpoint)
		}
	})
	return ssmstate.New(client, prefix, opts...), nil
}
