// Package ssm is a [state.Store] over AWS Systems Manager Parameter Store.
//
// A key is a SecureString parameter named prefix+key (for example prefix
// "/svc/state" and key "signing/current" give "/svc/state/signing/current").
// The Rev is the parameter's version number; [state.Store.GetRev] reads
// "name:<version>" and returns that version's LastModifiedDate. The version
// before the current one is Item.Previous (SSM keeps the last 100).
//
// # Conditional Put
//
// Creating (ifRev "") is atomic: PutParameter without Overwrite fails when
// the parameter exists. Updating is read-then-write: the current version is
// read, compared to ifRev, and PutParameter overwrites. Parameter Store has
// no compare-and-set, so two writers that both read version N both succeed
// and one write becomes version N+1 and the other N+2; the first is not lost
// (it stays in the history) but the second did not see it. Callers that need
// a lock use a different store or serialise writers themselves.
//
// # Size
//
// A value of up to 4 KiB is written to the standard tier (the tier of an
// existing parameter is left alone); a larger one, up to 8 KiB, is written to
// the advanced tier, which costs more per parameter and cannot be returned to
// standard. Beyond 8 KiB, Put returns [ErrTooLarge].
package ssm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/storage/state"
)

const (
	standardLimit = 4 * 1024
	advancedLimit = 8 * 1024
)

// ErrTooLarge: the value does not fit an advanced-tier parameter (8 KiB).
var ErrTooLarge = errors.New("ssm: value exceeds the 8 KiB parameter limit")

// API is the part of the SSM client the store uses; *ssm.Client satisfies it.
type API interface {
	GetParameter(ctx context.Context, in *awsssm.GetParameterInput, opts ...func(*awsssm.Options)) (*awsssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *awsssm.PutParameterInput, opts ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *awsssm.DeleteParameterInput, opts ...func(*awsssm.Options)) (*awsssm.DeleteParameterOutput, error)
	GetParametersByPath(ctx context.Context, in *awsssm.GetParametersByPathInput, opts ...func(*awsssm.Options)) (*awsssm.GetParametersByPathOutput, error)
}

type store struct {
	api      API
	prefix   string // "" or "/a/b" (no trailing slash)
	keyAlias string
}

// New returns the store under prefix (for example "/svc/state"; a leading
// slash is added if missing). state.WithKeyAlias selects the KMS key
// ("alias/..." or a key id) that encrypts the parameters; the default is
// the account's aws/ssm key. Region and endpoint belong to api; see [Open].
func New(api API, prefix string, opts ...state.Option) state.Store {
	o := state.ResolveOptions(state.Options{}, opts...)
	return &store{api: api, prefix: normalise(prefix), keyAlias: o.KeyAlias}
}

// Open builds an SSM client from the default AWS configuration, honouring
// state.WithRegion and state.WithEndpoint, and returns [New] over it.
func Open(ctx context.Context, prefix string, opts ...state.Option) (state.Store, error) {
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
		if o.Endpoint != "" {
			so.BaseEndpoint = aws.String(o.Endpoint)
		}
	})
	return New(client, prefix, opts...), nil
}

func normalise(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

func (s *store) Child(prefix string, opts ...state.Option) state.Store {
	c := *s
	c.prefix = s.prefix + normalise(prefix)
	c.keyAlias = state.ResolveOptions(state.Options{KeyAlias: s.keyAlias}, opts...).KeyAlias
	return &c
}

func (s *store) name(key string) (string, error) {
	if err := state.ValidateKey(key); err != nil {
		return "", err
	}
	return s.prefix + "/" + key, nil
}

func notFound(err error) bool {
	var a *types.ParameterNotFound
	var b *types.ParameterVersionNotFound
	return errors.As(err, &a) || errors.As(err, &b)
}

func (s *store) Get(ctx context.Context, key string) (state.Item, error) {
	name, err := s.name(key)
	if err != nil {
		return state.Item{}, err
	}
	return s.get(ctx, name)
}

func (s *store) GetRev(ctx context.Context, key string, rev state.Rev) (state.Item, error) {
	name, err := s.name(key)
	if err != nil {
		return state.Item{}, err
	}
	if v, err := strconv.ParseInt(string(rev), 10, 64); err != nil || v < 1 {
		return state.Item{}, state.ErrNotFound
	}
	return s.get(ctx, name+":"+string(rev))
}

func (s *store) get(ctx context.Context, selector string) (state.Item, error) {
	out, err := s.api.GetParameter(ctx, &awsssm.GetParameterInput{Name: aws.String(selector), WithDecryption: aws.Bool(true)})
	if err != nil {
		if notFound(err) {
			return state.Item{}, state.ErrNotFound
		}
		return state.Item{}, fmt.Errorf("ssm: get %s: %w", selector, err)
	}
	p := out.Parameter
	it := state.Item{Value: []byte(aws.ToString(p.Value)), Rev: state.Rev(strconv.FormatInt(p.Version, 10))}
	if p.LastModifiedDate != nil {
		it.Modified = *p.LastModifiedDate
	}
	if p.Version > 1 {
		it.Previous = state.Rev(strconv.FormatInt(p.Version-1, 10))
	}
	return it, nil
}

// currentVersion returns the current version of name, or "" if it does not exist.
func (s *store) currentVersion(ctx context.Context, name string) (state.Rev, error) {
	out, err := s.api.GetParameter(ctx, &awsssm.GetParameterInput{Name: aws.String(name)})
	if err != nil {
		if notFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("ssm: read %s: %w", name, err)
	}
	return state.Rev(strconv.FormatInt(out.Parameter.Version, 10)), nil
}

func (s *store) Put(ctx context.Context, key string, value []byte, ifRev state.Rev) (state.Rev, error) {
	name, err := s.name(key)
	if err != nil {
		return "", err
	}
	if err := state.ValidateObject(value); err != nil {
		return "", err
	}
	if len(value) > advancedLimit {
		return "", ErrTooLarge
	}
	if ifRev != "" {
		cur, err := s.currentVersion(ctx, name)
		if err != nil {
			return "", err
		}
		if cur != ifRev {
			return "", state.ErrConflict
		}
	}
	in := &awsssm.PutParameterInput{
		Name:      aws.String(name),
		Value:     aws.String(string(value)),
		Type:      types.ParameterTypeSecureString,
		Overwrite: aws.Bool(ifRev != ""),
	}
	if s.keyAlias != "" {
		in.KeyId = aws.String(s.keyAlias)
	}
	if len(value) > standardLimit {
		in.Tier = types.ParameterTierAdvanced
	}
	out, err := s.api.PutParameter(ctx, in)
	if err != nil {
		var exists *types.ParameterAlreadyExists
		if errors.As(err, &exists) {
			return "", state.ErrConflict
		}
		return "", fmt.Errorf("ssm: put %s: %w", name, err)
	}
	return state.Rev(strconv.FormatInt(out.Version, 10)), nil
}

func (s *store) Delete(ctx context.Context, key string) error {
	name, err := s.name(key)
	if err != nil {
		return err
	}
	if _, err := s.api.DeleteParameter(ctx, &awsssm.DeleteParameterInput{Name: aws.String(name)}); err != nil {
		if notFound(err) {
			return state.ErrNotFound
		}
		return fmt.Errorf("ssm: delete %s: %w", name, err)
	}
	return nil
}

func (s *store) List(ctx context.Context) ([]string, error) {
	path := s.prefix
	if path == "" {
		path = "/"
	}
	in := &awsssm.GetParametersByPathInput{Path: aws.String(path), Recursive: aws.Bool(false)}
	out := []string{}
	for {
		page, err := s.api.GetParametersByPath(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("ssm: list %s: %w", path, err)
		}
		for _, p := range page.Parameters {
			out = append(out, strings.TrimPrefix(aws.ToString(p.Name), s.prefix+"/"))
		}
		if page.NextToken == nil {
			break
		}
		in.NextToken = page.NextToken
	}
	sort.Strings(out)
	return out, nil
}
