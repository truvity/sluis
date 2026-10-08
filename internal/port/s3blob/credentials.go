package s3blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Credentials are an access key pair.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// CredentialsFunc reads the current credentials, from wherever the
// installation keeps them. An error must not carry the values.
type CredentialsFunc func(ctx context.Context) (Credentials, error)

// RefreshInterval is the least time between two reads of the credentials: a
// store that answers 403 for a reason a new key does not cure is not hammered.
const RefreshInterval = time.Minute

// credentials holds the key pair the client signs with and re-reads it on
// demand.
type credentials struct {
	read  CredentialsFunc
	now   func() time.Time
	cache *aws.CredentialsCache

	mu   sync.Mutex
	cur  Credentials
	last time.Time
}

func newCredentials(ctx context.Context, read CredentialsFunc, now func() time.Time) (*credentials, error) {
	c := &credentials{read: read, now: now}
	c.cache = aws.NewCredentialsCache(aws.CredentialsProviderFunc(c.retrieve))
	if _, err := c.cache.Retrieve(ctx); err != nil {
		return nil, fmt.Errorf("s3blob: reading the credentials: %w", err)
	}
	return c, nil
}

// retrieve reads the document. The cache calls it on the first use and after
// an Invalidate; the credentials never expire by themselves.
func (c *credentials) retrieve(ctx context.Context) (aws.Credentials, error) {
	got, err := c.read(ctx)
	if err != nil {
		return aws.Credentials{}, err
	}
	if got.AccessKeyID == "" || got.SecretAccessKey == "" {
		return aws.Credentials{}, errors.New("the credentials document has an empty field")
	}
	c.mu.Lock()
	c.cur, c.last = got, c.now()
	c.mu.Unlock()
	return aws.Credentials{AccessKeyID: got.AccessKeyID, SecretAccessKey: got.SecretAccessKey, Source: "sluis"}, nil
}

// refresh drops the cached pair so the next request reads it again, unless it
// was read less than [RefreshInterval] ago. It reports whether it did.
func (c *credentials) refresh() bool {
	c.mu.Lock()
	recent := c.now().Sub(c.last) < RefreshInterval
	c.mu.Unlock()
	if recent {
		return false
	}
	c.cache.Invalidate()
	return true
}

// reauth retries a request once, with credentials read again, when the store
// answered 403 and the last read is old enough.
type reauth struct {
	API
	creds *credentials
}

func isForbidden(err error) bool { return err != nil && status(err) == http.StatusForbidden }

// rewind returns a body reader to its start for the second attempt.
func rewind(body io.Reader) {
	if sk, ok := body.(io.Seeker); ok {
		_, _ = sk.Seek(0, io.SeekStart)
	}
}

func (r *reauth) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	out, err := r.API.GetObject(ctx, in, opts...)
	if isForbidden(err) && r.creds.refresh() {
		return r.API.GetObject(ctx, in, opts...)
	}
	return out, err
}

func (r *reauth) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	out, err := r.API.PutObject(ctx, in, opts...)
	if isForbidden(err) && r.creds.refresh() {
		rewind(in.Body)
		return r.API.PutObject(ctx, in, opts...)
	}
	return out, err
}

func (r *reauth) HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	out, err := r.API.HeadObject(ctx, in, opts...)
	if isForbidden(err) && r.creds.refresh() {
		return r.API.HeadObject(ctx, in, opts...)
	}
	return out, err
}

func (r *reauth) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	out, err := r.API.DeleteObject(ctx, in, opts...)
	if isForbidden(err) && r.creds.refresh() {
		return r.API.DeleteObject(ctx, in, opts...)
	}
	return out, err
}

func (r *reauth) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	out, err := r.API.ListObjectsV2(ctx, in, opts...)
	if isForbidden(err) && r.creds.refresh() {
		return r.API.ListObjectsV2(ctx, in, opts...)
	}
	return out, err
}
