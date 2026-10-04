// Package ssm is the AWS Systems Manager Parameter Store adapter of
// port.Secrets (docs/design/ports.md, "Secrets"): dynamic secrets and the
// exports as SecureString parameters.
//
// Credentials are ambient: the AWS SDK's default chain (EKS Pod Identity,
// IRSA, a Lambda role, the environment). Nothing in the configuration holds a
// secret.
//
// # Layout
//
// A port path `p` is the parameter `<root>/private/<p>`, except a path under
// `export/`, which is `<root>/export/<rest>`. The root defaults to `/sluis`.
// The split lets a consumer's External Secrets Operator be granted
// `<root>/export/*` and nothing else: the service's own secrets are under
// `<root>/private/`, which no consumer reads.
//
// # Values
//
// A parameter is a SecureString under the AWS-managed key (`alias/aws/ssm`), or
// under `KMSKeyID` when one is set. A value that is valid text is stored as it
// is, so a consumer reading an export sees the secret itself. A value that is
// not (a NUL or other control byte, bytes that are not UTF-8) or that begins
// with the marker `sluis-b64:` is stored as the marker and its base64, and is
// decoded on read. The size limit applies to what is stored, so a binary value
// is at most about 6 KiB.
//
// The tier is Intelligent-Tiering: a parameter is standard (4 KiB, free)
// until a value needs more, and advanced (8 KiB, billed) from then on. Advanced
// is never chosen for a value that fits the standard tier, and a parameter that
// became advanced is not downgraded (SSM does not allow it).
//
// # Operations
//
//   - Get is GetParameter with decryption; the version is SSM's parameter
//     version, a counter that grows on every write.
//   - Put is PutParameter with Overwrite; the version is the new one.
//   - PutIfVersion with an empty version is PutParameter with Overwrite=false,
//     which SSM evaluates atomically (ParameterAlreadyExists is ErrConflict).
//     With a version it is NOT atomic: see below.
//   - Delete is DeleteParameter; an absent parameter is not an error.
//   - List is GetParametersByPath, recursive, every page, without decryption.
//
// # Compare-and-swap
//
// SSM has no conditional write on a version. PutIfVersion(v) reads the
// parameter's version, refuses with ErrConflict if it is not v (ErrNotFound if
// the parameter is gone), and then writes with Overwrite. A writer that lands
// between the read and the write is overwritten: last writer wins. The caller
// must therefore be serialised some other way; in the service every writer of a
// secret holds the target's tick lease (State, which is atomic), so two writers
// of one secret do not overlap. Creation (an empty version) IS atomic.
package ssm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/port"
)

// DefaultRoot is the parameter hierarchy the adapter lives under.
const DefaultRoot = "/sluis"

const (
	privateDir = "private"
	exportDir  = "export"

	// binaryMarker begins a stored value that is base64 of the secret.
	binaryMarker = "sluis-b64:"
	// maxStored is the largest value SSM takes, an advanced parameter's.
	maxStored = 8 << 10
	// maxName is SSM's limit on a parameter name.
	maxName = 1011
)

// Config is where the parameters live.
type Config struct {
	// Root is the parameter hierarchy, `/sluis` by default: a leading slash,
	// no trailing one.
	Root string
	// KMSKeyID, when set, is the id, ARN or alias of the customer-managed key
	// the SecureStrings are encrypted with. Empty is the AWS-managed key.
	KMSKeyID string
	// Region defaults to the SDK's own resolution (AWS_REGION, the profile).
	Region string
	// Endpoint overrides the service address, for LocalStack.
	Endpoint string
}

// API is the part of the SSM client the adapter calls. *ssm.Client satisfies
// it; the unit tests use a fake.
type API interface {
	GetParameter(ctx context.Context, in *awsssm.GetParameterInput, opts ...func(*awsssm.Options)) (*awsssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *awsssm.PutParameterInput, opts ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *awsssm.DeleteParameterInput, opts ...func(*awsssm.Options)) (*awsssm.DeleteParameterOutput, error)
	GetParametersByPath(ctx context.Context, in *awsssm.GetParametersByPathInput, opts ...func(*awsssm.Options)) (*awsssm.GetParametersByPathOutput, error)
}

// Secrets is the adapter.
type Secrets struct {
	api    API
	root   string
	kmsKey string
}

var _ port.Secrets = (*Secrets)(nil)

// New builds the adapter over the SDK's default credential chain.
func New(ctx context.Context, cfg Config) (*Secrets, error) {
	if _, err := rootOf(cfg.Root); err != nil {
		return nil, err
	}
	var loaders []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		loaders = append(loaders, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return nil, fmt.Errorf("ssm: loading the AWS configuration: %w", err)
	}
	client := awsssm.NewFromConfig(awsCfg, func(o *awsssm.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	return NewWithAPI(client, cfg)
}

// NewWithAPI builds the adapter over a client the caller made.
func NewWithAPI(api API, cfg Config) (*Secrets, error) {
	root, err := rootOf(cfg.Root)
	if err != nil {
		return nil, err
	}
	return &Secrets{api: api, root: root, kmsKey: cfg.KMSKeyID}, nil
}

// rootOf normalises and checks the root.
func rootOf(root string) (string, error) {
	if root == "" {
		return DefaultRoot, nil
	}
	if !strings.HasPrefix(root, "/") || strings.HasSuffix(root, "/") {
		return "", fmt.Errorf("ssm: root %q must begin with a slash and not end with one", root)
	}
	if err := port.CheckSecretPath(strings.TrimPrefix(root, "/")); err != nil {
		return "", fmt.Errorf("ssm: root %q: %w", root, err)
	}
	return root, nil
}

// name maps a port path to the parameter name.
func (s *Secrets) name(path string) (string, error) {
	var n string
	if rest, ok := strings.CutPrefix(path, port.ExportPrefix); ok {
		n = s.root + "/" + exportDir + "/" + rest
	} else {
		n = s.root + "/" + privateDir + "/" + path
	}
	if len(n) > maxName {
		return "", fmt.Errorf("%w: secret path %q is too long for a parameter name", port.ErrUnsupported, path)
	}
	return n, nil
}

// path is the inverse of name for a name this adapter listed.
func (s *Secrets) path(name string) (string, bool) {
	if rest, ok := strings.CutPrefix(name, s.root+"/"+exportDir+"/"); ok {
		return port.ExportPrefix + rest, true
	}
	if rest, ok := strings.CutPrefix(name, s.root+"/"+privateDir+"/"); ok {
		return rest, true
	}
	return "", false
}

// encode is the stored form of a value.
func encode(value []byte) string {
	if utf8.Valid(value) && len(value) > 0 && !strings.HasPrefix(string(value), binaryMarker) && !hasControl(value) {
		return string(value)
	}
	return binaryMarker + base64.StdEncoding.EncodeToString(value)
}

func hasControl(b []byte) bool {
	for _, c := range b {
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' || c == 0x7f {
			return true
		}
	}
	return false
}

func decode(stored string) ([]byte, error) {
	if enc, ok := strings.CutPrefix(stored, binaryMarker); ok {
		b, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("ssm: a stored value carries the binary marker but is not base64: %w", err)
		}
		return b, nil
	}
	return []byte(stored), nil
}

// Get implements port.Secrets.
func (s *Secrets) Get(ctx context.Context, path string) (port.Secret, error) {
	if err := port.CheckSecretPath(path); err != nil {
		return port.Secret{}, err
	}
	name, err := s.name(path)
	if err != nil {
		return port.Secret{}, err
	}
	out, err := s.api.GetParameter(ctx, &awsssm.GetParameterInput{Name: &name, WithDecryption: aws.Bool(true)})
	if err != nil {
		if isNotFound(err) {
			return port.Secret{}, port.ErrNotFound
		}
		return port.Secret{}, unavailable("get", err)
	}
	if out.Parameter == nil {
		return port.Secret{}, unavailable("get", errors.New("no parameter in the answer"))
	}
	value, err := decode(aws.ToString(out.Parameter.Value))
	if err != nil {
		return port.Secret{}, err
	}
	return port.Secret{Value: value, Version: strconv.FormatInt(out.Parameter.Version, 10)}, nil
}

// Put implements port.Secrets.
func (s *Secrets) Put(ctx context.Context, path string, value []byte) (string, error) {
	name, stored, err := s.prepare(path, value)
	if err != nil {
		return "", err
	}
	return s.put(ctx, name, stored, true)
}

// PutIfVersion implements port.Secrets. With an empty version it is atomic;
// with one it is a read and a write, and a writer in between is overwritten
// (see the package documentation).
func (s *Secrets) PutIfVersion(ctx context.Context, path string, value []byte, version string) (string, error) {
	name, stored, err := s.prepare(path, value)
	if err != nil {
		return "", err
	}
	if version == "" {
		return s.put(ctx, name, stored, false)
	}
	out, err := s.api.GetParameter(ctx, &awsssm.GetParameterInput{Name: &name})
	if err != nil {
		if isNotFound(err) {
			return "", port.ErrNotFound
		}
		return "", unavailable("get", err)
	}
	if out.Parameter == nil || strconv.FormatInt(out.Parameter.Version, 10) != version {
		return "", port.ErrConflict
	}
	return s.put(ctx, name, stored, true)
}

func (s *Secrets) prepare(path string, value []byte) (name, stored string, err error) {
	if err = port.CheckSecretWrite(path, value); err != nil {
		return "", "", err
	}
	if name, err = s.name(path); err != nil {
		return "", "", err
	}
	stored = encode(value)
	if len(stored) > maxStored {
		return "", "", port.ErrTooLarge
	}
	return name, stored, nil
}

func (s *Secrets) put(ctx context.Context, name, stored string, overwrite bool) (string, error) {
	in := &awsssm.PutParameterInput{
		Name: &name, Value: &stored, Type: types.ParameterTypeSecureString,
		Overwrite: aws.Bool(overwrite), Tier: types.ParameterTierIntelligentTiering,
	}
	if s.kmsKey != "" {
		in.KeyId = &s.kmsKey
	}
	out, err := s.api.PutParameter(ctx, in)
	if err != nil {
		var exists *types.ParameterAlreadyExists
		if errors.As(err, &exists) {
			return "", port.ErrConflict
		}
		return "", unavailable("put", err)
	}
	return strconv.FormatInt(out.Version, 10), nil
}

// Delete implements port.Secrets.
func (s *Secrets) Delete(ctx context.Context, path string) error {
	if err := port.CheckSecretPath(path); err != nil {
		return err
	}
	name, err := s.name(path)
	if err != nil {
		return err
	}
	if _, err := s.api.DeleteParameter(ctx, &awsssm.DeleteParameterInput{Name: &name}); err != nil && !isNotFound(err) {
		return unavailable("delete", err)
	}
	return nil
}

// List implements port.Secrets.
func (s *Secrets) List(ctx context.Context, prefix string) ([]string, error) {
	p, err := port.SecretPrefix(prefix)
	if err != nil {
		return nil, err
	}
	var roots []string
	switch {
	case p == "":
		roots = []string{s.root + "/" + privateDir, s.root + "/" + exportDir}
	case strings.HasPrefix(p, port.ExportPrefix):
		roots = []string{s.root + "/" + exportDir + "/" + strings.TrimPrefix(p, port.ExportPrefix)}
	default:
		roots = []string{s.root + "/" + privateDir + "/" + p}
	}
	out := []string{}
	for _, root := range roots {
		names, err := s.byPath(ctx, strings.TrimSuffix(root, "/"))
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if path, ok := s.path(n); ok {
				out = append(out, path)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Secrets) byPath(ctx context.Context, path string) ([]string, error) {
	var names []string
	var token *string
	for {
		out, err := s.api.GetParametersByPath(ctx, &awsssm.GetParametersByPathInput{
			Path: &path, Recursive: aws.Bool(true), WithDecryption: aws.Bool(false),
			MaxResults: aws.Int32(10), NextToken: token,
		})
		if err != nil {
			return nil, unavailable("list", err)
		}
		for _, p := range out.Parameters {
			names = append(names, aws.ToString(p.Name))
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return names, nil
		}
		token = out.NextToken
	}
}

func isNotFound(err error) bool {
	var nf *types.ParameterNotFound
	return errors.As(err, &nf)
}

func unavailable(what string, err error) error {
	return fmt.Errorf("%w: ssm %s: %w", port.ErrUnavailable, what, err)
}
