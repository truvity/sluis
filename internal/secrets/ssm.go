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
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/awsretry"
)

// DefaultRefresh is how old the SSM source's copy of one secret may be before
// it is read again: a rotated secret reaches every instance within it.
const DefaultRefresh = time.Minute

// DefaultMaxStale is how old a copy may grow while every read again fails
// before the source fails closed: an instance whose read access was revoked
// stops serving the secrets it read, a day later at the latest.
const DefaultMaxStale = 24 * time.Hour

// retryAfter is how long after a failed read of a name the next is tried, and
// how long an absent name is remembered as absent: a throttled or denied SSM
// is not asked again on every request.
const retryAfter = 30 * time.Second

// readTimeout bounds one read of one parameter and every retry
// (internal/awsretry): a Lambda init has ten seconds in all, and a read that
// takes more than six fails and is tried again rather than being ended by the
// platform.
const readTimeout = 6 * time.Second

// maxEntries bounds the names remembered, including the absent ones: a name a
// caller made up is not kept for ever.
const maxEntries = 1024

// ConfigPrefix is where, under an installation's root, the secrets a document
// names live: `<root>/internal/config/<name>` (ADR 0041).
const ConfigPrefix = "/internal/config/"

// ParametersAPI is the part of the SSM client the source calls.
type ParametersAPI interface {
	GetParameter(ctx context.Context, in *awsssm.GetParameterInput, opts ...func(*awsssm.Options)) (*awsssm.GetParameterOutput, error)
}

// SSM reads one SecureString at a time, <Root>/internal/config/<name>,
// decrypted, when a name is first asked for, and again when its copy is older
// than Refresh. Nothing is read when the source is built, and a name nobody
// asks for is never read.
//
// A read again happens outside any lock, by one caller at a time per name;
// everybody else asking for that name is answered from the copy meanwhile, or
// waits for the read when there is none. A read that fails keeps the copy, and
// the next is not tried before retryAfter. An absent name is remembered as
// absent for retryAfter. A copy older than MaxStale with every read again
// failing is not served: the source fails closed.
type SSM struct {
	API     ParametersAPI
	Root    string
	Refresh time.Duration
	// MaxStale is how old a copy may grow while reads fail. Zero is
	// DefaultMaxStale.
	MaxStale time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

// entry is what is known of one name.
type entry struct {
	value   string
	have    bool // value was read
	absent  bool // the last read found no parameter (or an empty one)
	fetched time.Time
	nextTry time.Time
	lastErr error
	flight  chan struct{} // set while a read is in progress; closed when it ends
}

// NewSSM connects with the platform's credentials (a Lambda role, Pod
// Identity, the environment). It makes no call to SSM.
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
		o.Retryer = awsretry.New()
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	return &SSM{API: client, Root: root, Refresh: refresh}, nil
}

func checkRoot(root string) error {
	if !strings.HasPrefix(root, "/") || strings.HasSuffix(root, "/") || strings.Count(root, "/") < 2 {
		return errors.New("secrets: ssm: the root is not /<app>/<instance> (/sluis/<instance>)")
	}
	if err := CheckRoot(root); err != nil {
		return fmt.Errorf("secrets: ssm: %w", err)
	}
	return nil
}

// CheckRoot refuses an SSM root that would nest under another installation's
// tree, or under the old /sluis/private and /sluis/export: no segment of it may
// be `private`, `export`, `internal` or `external`.
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
	refresh, maxStale := s.Refresh, s.MaxStale
	if refresh <= 0 {
		refresh = DefaultRefresh
	}
	if maxStale <= 0 {
		maxStale = DefaultMaxStale
	}
	for {
		s.mu.Lock()
		e := s.entryLocked(name)
		now := s.now()
		age := now.Sub(e.fetched)
		switch {
		case e.have && age < refresh:
			v := e.value
			s.mu.Unlock()
			return v, nil
		case e.absent && age < retryAfter:
			s.mu.Unlock()
			return "", s.notFound(name)
		case e.flight != nil:
			// Another caller is reading this name: answer from the copy
			// while it is young enough to, else wait for the read.
			if e.have && age < maxStale {
				v := e.value
				s.mu.Unlock()
				return v, nil
			}
			wait := e.flight
			s.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		case now.Before(e.nextTry):
			// A read failed a moment ago.
			v, have, err := e.value, e.have && age < maxStale, e.lastErr
			s.mu.Unlock()
			if have {
				return v, nil
			}
			return "", s.staleErr(err, e.have)
		}
		flight := make(chan struct{})
		e.flight = flight
		s.mu.Unlock()

		value, found, err := s.read(context.WithoutCancel(ctx), name)

		s.mu.Lock()
		e.flight = nil
		now = s.now()
		switch {
		case err != nil:
			e.lastErr, e.nextTry = err, now.Add(retryAfter)
		case !found:
			e.have, e.absent, e.value, e.fetched, e.lastErr, e.nextTry = false, true, "", now, nil, time.Time{}
		default:
			e.have, e.absent, e.value, e.fetched, e.lastErr, e.nextTry = true, false, value, now, nil, time.Time{}
		}
		had, v, lastAge := e.have, e.value, now.Sub(e.fetched)
		s.mu.Unlock()
		close(flight)

		switch {
		case err == nil && !found:
			return "", s.notFound(name)
		case err == nil:
			return value, nil
		case had && lastAge < maxStale:
			return v, nil
		}
		return "", s.staleErr(err, had)
	}
}

// Describe implements [Source].
func (s *SSM) Describe(name string) string { return "ssm " + s.Root + ConfigPrefix + name }

func (s *SSM) notFound(name string) error {
	return fmt.Errorf("%w: %s (no parameter %s)", ErrNotFound, name, s.Root+ConfigPrefix+name)
}

// entryLocked is the entry of a name, made when there is none. At the bound the
// entries that no longer matter are dropped first.
func (s *SSM) entryLocked(name string) *entry {
	if e, ok := s.entries[name]; ok {
		return e
	}
	if s.entries == nil {
		s.entries = map[string]*entry{}
	}
	if len(s.entries) >= maxEntries {
		now := s.now()
		for n, e := range s.entries {
			if e.flight == nil && !e.have && now.Sub(e.fetched) >= retryAfter && !now.Before(e.nextTry) {
				delete(s.entries, n)
			}
		}
	}
	e := &entry{}
	if len(s.entries) < maxEntries {
		s.entries[name] = e
	}
	return e
}

func (s *SSM) staleErr(err error, hadCopy bool) error {
	if err == nil {
		err = errors.New("secrets: ssm: the parameter is being read")
	}
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

// read is one decrypted GetParameter. An absent parameter, or an empty one, is
// not an error: found is false.
func (s *SSM) read(ctx context.Context, name string) (value string, found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	out, err := s.API.GetParameter(ctx, &awsssm.GetParameterInput{
		Name: aws.String(s.Root + ConfigPrefix + name), WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var nf *types.ParameterNotFound
		if errors.As(err, &nf) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("secrets: ssm: reading %s: %w", s.Root+ConfigPrefix+name, err)
	}
	if out.Parameter == nil {
		return "", false, nil
	}
	v := aws.ToString(out.Parameter.Value)
	return v, v != "", nil
}
