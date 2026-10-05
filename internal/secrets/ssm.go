package secrets

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
)

// DefaultRefresh is how old the SSM source's copy may be before it is read
// again: a rotated secret reaches every instance within it.
const DefaultRefresh = 5 * time.Minute

// ConfigPrefix is where, under an installation's root, the secrets a document
// names live: `<root>/private/config/<name>` (layout v3).
const ConfigPrefix = "/private/config/"

// ParametersAPI is the part of the SSM client the source calls.
type ParametersAPI interface {
	GetParametersByPath(ctx context.Context, in *awsssm.GetParametersByPathInput, opts ...func(*awsssm.Options)) (*awsssm.GetParametersByPathOutput, error)
}

// SSM reads every parameter under <Root>/private/config/ at once, decrypted,
// and again when its copy is older than Refresh.
type SSM struct {
	API     ParametersAPI
	Root    string
	Refresh time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu      sync.Mutex
	values  map[string]string
	fetched time.Time
}

// NewSSM connects with the platform's credentials (a Lambda role, Pod
// Identity, the environment).
func NewSSM(ctx context.Context, root, region, endpoint string, refresh time.Duration) (*SSM, error) {
	if err := checkRoot(root); err != nil {
		return nil, err
	}
	var loaders []func(*awsconfig.LoadOptions) error
	if region != "" {
		loaders = append(loaders, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return nil, fmt.Errorf("secrets: ssm: %w", err)
	}
	client := awsssm.NewFromConfig(cfg, func(o *awsssm.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	return &SSM{API: client, Root: root, Refresh: refresh}, nil
}

func checkRoot(root string) error {
	if !strings.HasPrefix(root, "/") || strings.HasSuffix(root, "/") || strings.Count(root, "/") < 2 {
		return fmt.Errorf("secrets: ssm: root %q is not /<app>/<instance> (layout v3: /sluis/<instance>)", root)
	}
	return nil
}

// Get implements [Source].
func (s *SSM) Get(ctx context.Context, name string) (string, error) {
	if err := Check(name); err != nil {
		return "", err
	}
	values, err := s.current(ctx)
	if err != nil {
		return "", err
	}
	v, ok := values[name]
	if !ok || v == "" {
		return "", fmt.Errorf("%w: %s (no parameter %s)", ErrNotFound, name, s.Root+ConfigPrefix+name)
	}
	return v, nil
}

// Describe implements [Source].
func (s *SSM) Describe(name string) string { return "ssm " + s.Root + ConfigPrefix + name }

// current is the copy, read again when it is older than the refresh. A read
// that fails keeps the copy there is, when there is one: an SSM outage does not
// take a secret away from an instance that read it.
func (s *SSM) current(ctx context.Context) (map[string]string, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	refresh := s.Refresh
	if refresh <= 0 {
		refresh = DefaultRefresh
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values != nil && now().Sub(s.fetched) < refresh {
		return s.values, nil
	}
	values, err := s.read(ctx)
	if err != nil {
		if s.values != nil {
			return s.values, nil
		}
		return nil, err
	}
	s.values, s.fetched = values, now()
	return values, nil
}

func (s *SSM) read(ctx context.Context) (map[string]string, error) {
	prefix := s.Root + ConfigPrefix
	values := map[string]string{}
	var token *string
	for {
		out, err := s.API.GetParametersByPath(ctx, &awsssm.GetParametersByPathInput{
			Path: aws.String(strings.TrimSuffix(prefix, "/")), Recursive: aws.Bool(true),
			WithDecryption: aws.Bool(true), NextToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("secrets: ssm: reading %s: %w", prefix, err)
		}
		for _, p := range out.Parameters {
			if name, ok := strings.CutPrefix(aws.ToString(p.Name), prefix); ok {
				values[name] = aws.ToString(p.Value)
			}
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return values, nil
		}
		token = out.NextToken
	}
}
