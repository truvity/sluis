package migrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// SSMConfigAPI is the part of the SSM client [SSMConfig] calls.
type SSMConfigAPI interface {
	GetParameter(ctx context.Context, in *awsssm.GetParameterInput, opts ...func(*awsssm.Options)) (*awsssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *awsssm.PutParameterInput, opts ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error)
}

// SSMConfig is the [ConfigStore] over `<root>/internal/config/` in Parameter
// Store: plain-text SecureStrings, which the secrets source reads as they are.
type SSMConfig struct {
	API  SSMConfigAPI
	Root string
	// KMSKeyID encrypts what is written; empty is the AWS-managed key.
	KMSKeyID string
}

func (c SSMConfig) name(n string) string {
	return strings.TrimSuffix(c.Root, "/") + "/internal/config/" + n
}

// Get implements [ConfigStore].
func (c SSMConfig) Get(ctx context.Context, name string) (string, bool, error) {
	out, err := c.API.GetParameter(ctx, &awsssm.GetParameterInput{Name: aws.String(c.name(name)), WithDecryption: aws.Bool(true)})
	var nf *types.ParameterNotFound
	switch {
	case errors.As(err, &nf):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("ssm: reading %s: %w", c.name(name), err)
	case out.Parameter == nil:
		return "", false, nil
	}
	return aws.ToString(out.Parameter.Value), true, nil
}

// Create implements [ConfigStore]: SSM refuses a parameter that exists.
func (c SSMConfig) Create(ctx context.Context, name, value string) error {
	in := &awsssm.PutParameterInput{
		Name: aws.String(c.name(name)), Value: aws.String(value), Type: types.ParameterTypeSecureString,
		Overwrite: aws.Bool(false), Tier: types.ParameterTierIntelligentTiering,
	}
	if c.KMSKeyID != "" {
		in.KeyId = aws.String(c.KMSKeyID)
	}
	if _, err := c.API.PutParameter(ctx, in); err != nil {
		return fmt.Errorf("ssm: writing %s: %w", c.name(name), err)
	}
	return nil
}
