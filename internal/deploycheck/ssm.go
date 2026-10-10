package deploycheck

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/awsretry"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secrets"
)

// SSM reads parameters with one GetParameter each.
//
// The read decrypts: whether a SecureString's content is empty, or is a state
// secret of the right shape, is not in its ciphertext. The content is judged
// and dropped; nothing here logs, returns or wraps it.
type SSM struct {
	API secrets.ParametersAPI
}

var _ Reader = SSM{}

// Read implements [Reader].
func (s SSM) Read(ctx context.Context, name string) (Entry, bool, error) {
	out, err := s.API.GetParameter(ctx, &awsssm.GetParameterInput{Name: aws.String(name), WithDecryption: aws.Bool(true)})
	if err != nil {
		var nf *types.ParameterNotFound
		if errors.As(err, &nf) {
			return Entry{}, false, nil
		}
		return Entry{}, false, fmt.Errorf("get %s: %w", name, err)
	}
	if out.Parameter == nil {
		return Entry{}, false, nil
	}
	return Entry{Value: []byte(aws.ToString(out.Parameter.Value)), Version: out.Parameter.Version}, true, nil
}

// Check is the whole check for a loaded service document: the layout and root
// its `secrets` section names, the declared set of the document and its policy,
// and one read of each from the SSM the section names. only, when given, keeps
// the kinds a function's own module reads (a function checks its own paths and
// no other module's).
func Check(ctx context.Context, doc *config.Serve, pol *config.PolicyDocument, only ...string) (Report, error) {
	layout, err := Layout(doc.Secrets)
	if err != nil {
		return Report{}, err
	}
	s := doc.Secrets
	if err := secrets.CheckRoot(s.Root); err != nil {
		return Report{}, fmt.Errorf("secrets.root: %w", err)
	}
	var loaders []func(*awsconfig.LoadOptions) error
	if s.Region != "" {
		loaders = append(loaders, awsconfig.WithRegion(s.Region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return Report{}, fmt.Errorf("load the AWS configuration: %w", err)
	}
	client := awsssm.NewFromConfig(cfg, func(o *awsssm.Options) {
		o.Retryer = awsretry.New()
		if s.Endpoint != "" {
			o.BaseEndpoint = aws.String(s.Endpoint)
		}
	})
	declared := config.DeclaredSecrets(doc, pol)
	if len(only) > 0 {
		declared = slices.DeleteFunc(declared, func(d config.DeclaredSecret) bool { return !slices.Contains(only, d.Kind) })
	}
	return Run(ctx, SSM{API: client}, s.Root, layout, declared), nil
}
