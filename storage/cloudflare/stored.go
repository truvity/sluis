package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// SchemaStored is the schema of the document sluis keeps a preset's current
// credential in, at external/cloudflare/<preset> of its secret store.
const SchemaStored = "cloudflare/v1"

// StoredR2 is the R2 credential of a `cloudflare/v1` document: what sluis
// rotates for an R2 preset. The access key is the token's id and the secret the
// hex SHA-256 of its value; ExpiresOn is when Cloudflare stops accepting it.
type StoredR2 struct {
	AccessKeyID     string
	SecretAccessKey string
	Endpoint        string
	ExpiresOn       time.Time
}

// ParseStoredR2 reads a `cloudflare/v1` document holding R2 credentials. A
// document of another schema, one holding an API token instead (a preset
// without an endpoint), or one lacking a field is refused; a field this
// version does not know is ignored, because adding one is not breaking. No
// error carries a value of the document.
func ParseStoredR2(raw []byte) (StoredR2, error) {
	var d struct {
		Schema          string `json:"schema"`
		Token           string `json:"token"`
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		Endpoint        string `json:"endpoint"`
		ExpiresOn       string `json:"expires_on"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return StoredR2{}, errors.New("not a JSON object")
	}
	if d.Schema != SchemaStored {
		return StoredR2{}, fmt.Errorf("schema %q, want %q", d.Schema, SchemaStored)
	}
	if d.Token != "" || d.AccessKeyID == "" || d.SecretAccessKey == "" {
		return StoredR2{}, errors.New("not the R2 credentials of an R2 preset (access_key_id and secret_access_key, no token)")
	}
	expires, err := time.Parse(time.RFC3339, d.ExpiresOn)
	if err != nil {
		return StoredR2{}, fmt.Errorf("expires_on %q is not RFC 3339", d.ExpiresOn)
	}
	return StoredR2{AccessKeyID: d.AccessKeyID, SecretAccessKey: d.SecretAccessKey, Endpoint: d.Endpoint, ExpiresOn: expires.UTC()}, nil
}

// SameEndpoint reports whether two endpoint URLs name the same store: the same
// scheme and host, without case, and the same path once a trailing slash is
// dropped. An empty or unparsable endpoint matches nothing.
func SameEndpoint(a, b string) bool {
	norm := func(s string) (string, bool) {
		u, err := url.Parse(strings.TrimSpace(s))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return "", false
		}
		return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimSuffix(u.EscapedPath(), "/"), true
	}
	x, okx := norm(a)
	y, oky := norm(b)
	return okx && oky && x == y
}

// Defaults of a [StoredProvider].
const (
	// DefaultRecheck is the longest a stored credential is used before its
	// document is read again.
	DefaultRecheck = time.Minute
	// DefaultMargin is how long before its expires_on a stored credential stops
	// being handed out without reading the document again.
	DefaultMargin = 30 * time.Second
	// storedRetry is how soon a document that could not be read is read again
	// while the credential held from it is still good.
	storedRetry = 10 * time.Second
)

// StoredConfig is what a [StoredProvider] is made of.
type StoredConfig struct {
	// Read returns the document. It is called at the first Retrieve, whenever
	// the held credential is due, and after a 403.
	Read func(ctx context.Context) ([]byte, error)
	// Source names where the document is read from (a parameter, a file), for
	// errors. It is never a value.
	Source string
	// Endpoint, when set, is the store's endpoint: a document whose `endpoint`
	// is another (scheme and host compared without case, a trailing slash
	// ignored), or none, is refused at every read, so a credential for another
	// account or jurisdiction is never handed out.
	Endpoint string
	// Recheck is the longest a credential is used before the document is read
	// again. Zero is [DefaultRecheck].
	Recheck time.Duration
	// Margin is how long before expires_on the document is read again whatever
	// Recheck says. Zero is [DefaultMargin].
	Margin time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// StoredProvider hands out the R2 credential sluis rotates for a preset
// (ADR 0070, decision 6): it reads the `cloudflare/v1` document sluis keeps at
// external/cloudflare/<preset>, from SSM or from a file a secrets operator
// projected, and reads it again before the credential expires and after a 403.
// It mints nothing and holds no minter credential. It is an
// [aws.CredentialsProvider]: wrap it in aws.NewCredentialsCache for an S3 client.
//
// sluis replaces the document every `rotation` and each credential lives
// `lifetime`, so a reader that reads again within `lifetime - rotation` always
// finds a credential that is still accepted.
type StoredProvider struct {
	cfg StoredConfig
	now func() time.Time

	mu     sync.Mutex
	held   StoredR2
	lastRe time.Time
}

// NewStoredProvider checks the configuration and returns the provider. It reads
// nothing: the document is read at the first [StoredProvider.Retrieve].
func NewStoredProvider(cfg StoredConfig) (*StoredProvider, error) {
	if cfg.Read == nil {
		return nil, errors.New("cloudflare: no way to read the stored credential")
	}
	if cfg.Recheck <= 0 {
		cfg.Recheck = DefaultRecheck
	}
	if cfg.Margin <= 0 {
		cfg.Margin = DefaultMargin
	}
	p := &StoredProvider{cfg: cfg, now: cfg.Now}
	if p.now == nil {
		p.now = time.Now
	}
	return p, nil
}

// Retrieve implements [aws.CredentialsProvider]: it reads the document and
// hands out its credential, expiring when the document is next due to be read.
// A read that fails while the held credential is still good hands that one out
// again and tries once more shortly.
func (p *StoredProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	err := p.readLocked(ctx, now)
	if err == nil {
		return p.credentials(now, p.cfg.Recheck), nil
	}
	if p.held.AccessKeyID != "" && now.Before(p.held.ExpiresOn.Add(-p.cfg.Margin)) {
		return p.credentials(now, storedRetry), nil
	}
	return aws.Credentials{}, err
}

// Reauthenticate reads the document again because the store answered 403,
// unless it did so less than [MinReauth] ago, and reports whether the
// credential changed. A caller holding the credentials in a cache invalidates
// it when this answers true.
func (p *StoredProvider) Reauthenticate(ctx context.Context) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if !p.lastRe.IsZero() && now.Sub(p.lastRe) < MinReauth {
		return false, nil
	}
	p.lastRe = now
	before := p.held.AccessKeyID
	if err := p.readLocked(ctx, now); err != nil {
		return false, err
	}
	return p.held.AccessKeyID != before, nil
}

// readLocked reads and checks the document and holds its credential. An
// expired credential is refused: the store would answer 403 naming neither the
// source nor its age.
func (p *StoredProvider) readLocked(ctx context.Context, now time.Time) error {
	raw, err := p.cfg.Read(ctx)
	if err != nil {
		return fmt.Errorf("cloudflare: reading the stored credential %s: %w", p.cfg.Source, err)
	}
	doc, err := ParseStoredR2(raw)
	if err != nil {
		return fmt.Errorf("cloudflare: the stored credential %s: %w", p.cfg.Source, err)
	}
	if p.cfg.Endpoint != "" && !SameEndpoint(doc.Endpoint, p.cfg.Endpoint) {
		return fmt.Errorf("cloudflare: the stored credential %s is for the endpoint %q and the store is at %q: "+
			"is credentials_ref the sluis preset of this store's account?", p.cfg.Source, doc.Endpoint, p.cfg.Endpoint)
	}
	if !now.Before(doc.ExpiresOn) {
		return fmt.Errorf("cloudflare: the stored credential %s expired at %s: is sluis rotating the preset, and is the copy being synced?",
			p.cfg.Source, doc.ExpiresOn.Format(time.RFC3339))
	}
	p.held = doc
	return nil
}

// credentials is the held credential, expiring at the next read: after
// `within` at most, before the margin ahead of expires_on, and never sooner
// than half the time it has left, so that a document that is not replaced in
// time is read again more often as it runs out, but not in a loop.
func (p *StoredProvider) credentials(now time.Time, within time.Duration) aws.Credentials {
	left := p.held.ExpiresOn.Sub(now)
	next := p.held.ExpiresOn.Add(-p.cfg.Margin)
	if half := now.Add(left / 2); next.Before(half) {
		next = half
	}
	if limit := now.Add(within); limit.Before(next) {
		next = limit
	}
	return aws.Credentials{
		AccessKeyID: p.held.AccessKeyID, SecretAccessKey: p.held.SecretAccessKey,
		Source: "sluis-cloudflare-stored", CanExpire: true, Expires: next,
	}
}

var _ aws.CredentialsProvider = (*StoredProvider)(nil)
