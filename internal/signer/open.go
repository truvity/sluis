package signer

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/truvity/sluis/storage/keys"
	keysbackend "github.com/truvity/sluis/storage/keys/kms"
)

// OpenKMS opens the key service for adapter "kms": storage/keys/kms over the
// AWS default credential chain.
func OpenKMS(ctx context.Context, region string) (keys.Backend, error) {
	var loaders []func(*awsconfig.LoadOptions) error
	if region != "" {
		loaders = append(loaders, awsconfig.WithRegion(region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return nil, fmt.Errorf("load the AWS configuration for keys.adapter kms: %w", err)
	}
	return keysbackend.New(kms.NewFromConfig(awsCfg)), nil
}

// OpenSignKey opens the key behind `keys.sign`. A nil backend is opened from
// the adapter the configuration names; a test supplies its own.
func OpenSignKey(ctx context.Context, kc keys.Config, region, instance string, backend keys.Backend) (*keys.Key, error) {
	if backend == nil {
		var err error
		switch kc.Adapter {
		case "kms":
			backend, err = OpenKMS(ctx, region)
		default:
			err = fmt.Errorf("keys.adapter %q cannot be opened by the issuer here (kms is the adapter of a deployment)", kc.Adapter)
		}
		if err != nil {
			return nil, err
		}
	}
	set, err := keys.Open(kc, keys.Options{Backend: backend, Instance: instance})
	if err != nil {
		return nil, err
	}
	return set.For(keys.Sign)
}
