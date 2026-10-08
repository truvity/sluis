package secrets

import (
	"context"
	"errors"
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

// DefaultMaxStale is how old a copy may grow while every read again fails
// before the source fails closed: an instance whose read access was revoked
// stops serving the secrets it read, a day later at the latest.
const DefaultMaxStale = 24 * time.Hour

// retryAfter is how long after a failed read the next is tried: a throttled or
// denied SSM is not asked again on every request.
const retryAfter = 30 * time.Second

// readTimeout bounds one read of the prefix, every page of it.
const readTimeout = 20 * time.Second

// ConfigPrefix is where, under an installation's root, the secrets a document
// names live: `<root>/private/config/<name>` (layout v3).
const ConfigPrefix = "/private/config/"

// ConfigPrefixV4 is the same under layout v4 (ADR 0041): `<root>/internal/config/<name>`.
const ConfigPrefixV4 = "/internal/config/"

// ParametersAPI is the part of the SSM client the source calls.
type ParametersAPI interface {
	GetParametersByPath(ctx context.Context, in *awsssm.GetParametersByPathInput, opts ...func(*awsssm.Options)) (*awsssm.GetParametersByPathOutput, error)
}

// SSM reads every parameter under <Root>/private/config/ at once, decrypted,
// and again when its copy is older than Refresh.
//
// A read again happens outside the lock, by one caller at a time; everybody
// else is answered from the copy meanwhile. A read that fails keeps the copy,
// and the next is not tried before retryAfter. A copy older than MaxStale with
// every read again failing is not served: the source fails closed.
type SSM struct {
	API     ParametersAPI
	Root    string
	Refresh time.Duration
	// Layout is the installation's secrets layout: "" or "v3" reads
	// <root>/private/config/, "v4" reads <root>/internal/config/ and
	// "transition" reads both, v4 first.
	Layout string
	// MaxStale is how old a copy may grow while reads fail. Zero is
	// DefaultMaxStale.
	MaxStale time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu         sync.Mutex
	values     map[string]string
	fetched    time.Time
	nextTry    time.Time
	lastErr    error
	refreshing bool
	// readMu serialises the reads, so a cold start reads once however many
	// callers arrive together.
	readMu sync.Mutex
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
		return errors.New("secrets: ssm: the root is not /<app>/<instance> (layout v3: /sluis/<instance>)")
	}
	if err := CheckRoot(root); err != nil {
		return fmt.Errorf("secrets: ssm: %w", err)
	}
	return nil
}

// CheckRoot refuses an SSM root that would nest under another installation's
// private or export tree, or under layout v2's (/sluis/private, /sluis/export):
// no segment of it may be `private` or `export`.
func CheckRoot(root string) error {
	for _, seg := range strings.Split(strings.Trim(root, "/"), "/") {
		if seg == "private" || seg == "export" || seg == "internal" || seg == "external" {
			return fmt.Errorf("the root has a segment %q: an instance may not be named private, export, internal or external, "+
				"which would put its parameters under another tree", seg)
		}
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
		return "", fmt.Errorf("%w: %s (no parameter %s)", ErrNotFound, name, s.Root+s.prefixes()[0]+name)
	}
	return v, nil
}

// Describe implements [Source].
func (s *SSM) Describe(name string) string { return "ssm " + s.Root + s.prefixes()[0] + name }

// prefixes are the config prefixes the layout reads, the one that wins first.
func (s *SSM) prefixes() []string {
	switch s.Layout {
	case "v4":
		return []string{ConfigPrefixV4}
	case "transition":
		return []string{ConfigPrefixV4, ConfigPrefix}
	default:
		return []string{ConfigPrefix}
	}
}

// current is the copy, read again when it is older than the refresh.
func (s *SSM) current(ctx context.Context) (map[string]string, error) {
	now := s.now()
	refresh, maxStale := s.Refresh, s.MaxStale
	if refresh <= 0 {
		refresh = DefaultRefresh
	}
	if maxStale <= 0 {
		maxStale = DefaultMaxStale
	}
	s.mu.Lock()
	values, age := s.values, now.Sub(s.fetched)
	switch {
	case values != nil && age < refresh:
		s.mu.Unlock()
		return values, nil
	case now.Before(s.nextTry) || (values != nil && s.refreshing):
		// A read failed a moment ago, or another caller is reading: answer
		// from the copy while it is young enough to.
		err := s.lastErr
		s.mu.Unlock()
		if values != nil && age < maxStale {
			return values, nil
		}
		if err == nil {
			err = errors.New("secrets: ssm: the parameters are being read")
		}
		return nil, s.staleErr(err, values != nil)
	}
	s.refreshing = true
	s.mu.Unlock()

	s.readMu.Lock()
	defer s.readMu.Unlock()
	s.mu.Lock()
	if s.values != nil && s.now().Sub(s.fetched) < refresh {
		// Another caller read while this one waited.
		s.refreshing = false
		values := s.values
		s.mu.Unlock()
		return values, nil
	}
	s.mu.Unlock()
	read, err := s.read(context.WithoutCancel(ctx))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshing = false
	if err != nil {
		s.lastErr, s.nextTry = err, s.now().Add(retryAfter)
		if s.values != nil && s.now().Sub(s.fetched) < maxStale {
			return s.values, nil
		}
		return nil, s.staleErr(err, s.values != nil)
	}
	s.values, s.fetched, s.lastErr, s.nextTry = read, s.now(), nil, time.Time{}
	return read, nil
}

func (s *SSM) staleErr(err error, hadCopy bool) error {
	if hadCopy {
		return fmt.Errorf("secrets: ssm: the copy is older than allowed and it cannot be read again: %w", err)
	}
	return err
}

func (s *SSM) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *SSM) read(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	values := map[string]string{}
	// The first prefix wins: read the others first and let it overwrite.
	prefixes := s.prefixes()
	for i := len(prefixes) - 1; i >= 0; i-- {
		if err := s.readPrefix(ctx, s.Root+prefixes[i], values); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (s *SSM) readPrefix(ctx context.Context, prefix string, values map[string]string) error {
	var token *string
	for {
		out, err := s.API.GetParametersByPath(ctx, &awsssm.GetParametersByPathInput{
			Path: aws.String(strings.TrimSuffix(prefix, "/")), Recursive: aws.Bool(true),
			WithDecryption: aws.Bool(true), NextToken: token,
		})
		if err != nil {
			return fmt.Errorf("secrets: ssm: reading %s: %w", prefix, err)
		}
		for _, p := range out.Parameters {
			if name, ok := strings.CutPrefix(aws.ToString(p.Name), prefix); ok {
				values[name] = aws.ToString(p.Value)
			}
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return nil
		}
		token = out.NextToken
	}
}
