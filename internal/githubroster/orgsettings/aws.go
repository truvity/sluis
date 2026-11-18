package orgsettings

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/truvity/sluis/internal/awsretry"
	"github.com/truvity/sluis/internal/githubroster/orgconfig"
	"github.com/truvity/sluis/storage/awsotel"
)

// ParametersAPI is the part of the SSM client a read calls.
type ParametersAPI interface {
	GetParameter(ctx context.Context, in *awsssm.GetParameterInput, opts ...func(*awsssm.Options)) (*awsssm.GetParameterOutput, error)
}

// ParametersFor builds the parameter-store client of one organisation's
// account, from the credentials of the role assumed there.
type ParametersFor func(creds aws.CredentialsProvider, region string) ParametersAPI

// AWSReader reads each organisation's document and secrets with the role the
// installation names for it. The controller's own credentials (a Lambda
// role, Pod Identity, the environment) are used only to assume that role.
type AWSReader struct {
	configs map[string]orgconfig.Config
	sts     stscreds.AssumeRoleAPIClient
	client  ParametersFor

	mu      sync.Mutex
	clients map[string]ParametersAPI
}

// NewAWSReader connects with the platform's credentials. It calls nothing:
// a role is assumed when an organisation's document is first read.
func NewAWSReader(ctx context.Context, configs map[string]orgconfig.Config) (*AWSReader, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("orgsettings: aws: %w", err)
	}

	stsClient := sts.NewFromConfig(cfg, func(o *sts.Options) {
		o.TracerProvider, o.MeterProvider = awsotel.Providers()
		o.Retryer = awsretry.New()
	})

	return NewAWSReaderWith(configs, stsClient, func(creds aws.CredentialsProvider, region string) ParametersAPI {
		own := cfg.Copy()
		own.Credentials, own.Region = creds, region

		return awsssm.NewFromConfig(own, func(o *awsssm.Options) {
			o.TracerProvider, o.MeterProvider = awsotel.Providers()
			o.Retryer = awsretry.New()
		})
	}), nil
}

// NewAWSReaderWith is [NewAWSReader] with the clients given, for a test.
func NewAWSReaderWith(configs map[string]orgconfig.Config, assume stscreds.AssumeRoleAPIClient, client ParametersFor) *AWSReader {
	return &AWSReader{configs: configs, sts: assume, client: client, clients: map[string]ParametersAPI{}}
}

// For is the source of one organisation, or false when the installation
// names no reading for it.
func (r *AWSReader) For(org string) (Source, bool) {
	if r == nil {
		return nil, false
	}

	cfg, ok := r.configs[org]
	if !ok {
		return nil, false
	}

	return &awsSource{reader: r, org: org, cfg: cfg}, true
}

// parameters is the organisation's client, built once: the credentials cache
// renews the assumed role before it expires. The role is assumed with the
// organisation's external id, when it has one, and with a session policy that
// limits it to the document and the secrets' parameters.
func (r *AWSReader) parameters(org string, cfg orgconfig.Config) (ParametersAPI, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if api, ok := r.clients[org]; ok {
		return api, nil
	}

	session, err := cfg.SessionPolicy()
	if err != nil {
		return nil, err //nolint:wrapcheck // names the key
	}

	provider := stscreds.NewAssumeRoleProvider(r.sts, cfg.Read.RoleARN, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = "sluis-github-" + org
		o.Policy = aws.String(session)

		if cfg.Read.ExternalID != "" {
			o.ExternalID = aws.String(cfg.Read.ExternalID)
		}
	})
	api := r.client(aws.NewCredentialsCache(provider), cfg.Read.Region)
	r.clients[org] = api

	return api, nil
}

type awsSource struct {
	reader *AWSReader
	org    string
	cfg    orgconfig.Config
}

// get reads one parameter. A secret's parameter must be a SecureString: a
// plain String is readable by far more than the role that reads it here.
func (s *awsSource) get(ctx context.Context, name string, decrypt, secure bool) (value string, version int64, err error) {
	api, err := s.reader.parameters(s.org, s.cfg)
	if err != nil {
		return "", 0, err
	}

	out, err := api.GetParameter(ctx, &awsssm.GetParameterInput{Name: aws.String(name), WithDecryption: aws.Bool(decrypt)})
	if err != nil {
		return "", 0, fmt.Errorf("read the parameter %s as %s: %w", name, s.cfg.Read.RoleARN, err)
	}

	if out.Parameter == nil {
		return "", 0, errors.New("the parameter " + name + " came back empty")
	}

	if secure && out.Parameter.Type != types.ParameterTypeSecureString {
		return "", 0, errors.New("the parameter " + name + " is not a SecureString")
	}

	return aws.ToString(out.Parameter.Value), out.Parameter.Version, nil
}

// Document implements [Source].
func (s *awsSource) Document(ctx context.Context) ([]byte, error) {
	value, _, err := s.get(ctx, s.cfg.Read.Parameter, true, false)
	if err != nil {
		return nil, err
	}

	return []byte(value), nil
}

// Version implements [Secrets]. The value is not decrypted.
func (s *awsSource) Version(ctx context.Context, parameter string) (int64, error) {
	_, version, err := s.get(ctx, parameter, false, true)

	return version, err
}

// Value implements [Secrets].
func (s *awsSource) Value(ctx context.Context, parameter string) (string, int64, error) {
	return s.get(ctx, parameter, true, true)
}
