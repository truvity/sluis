package s3store

import (
	"context"
	"errors"
	"io"
	"net/http"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Reauth replaces the credentials the store signs with, and reports whether it
// did. An interface, so that [Options] stays comparable. A store whose credentials are minted (a Cloudflare preset) sets it in
// [Options]: when the store answers 403 the request is sent once more, after
// Reauth, because the credentials may have been revoked or may not have been
// accepted yet. Reauth bounds how often it replaces them (the minting provider:
// once in 30 seconds), and answering false means "the credentials are as they
// were", so the request is not sent again.
type Reauth interface {
	Reauthenticate(ctx context.Context) (replaced bool, err error)
}

// ReauthFunc is a function as a [Reauth].
type ReauthFunc func(ctx context.Context) (replaced bool, err error)

// Reauthenticate calls f.
func (f ReauthFunc) Reauthenticate(ctx context.Context) (bool, error) { return f(ctx) }

// reauthAPI sends a request again, once, after a 403 that Reauth answered by
// replacing the credentials. It never retries anything else, and never twice.
type reauthAPI struct {
	API
	reauth Reauth
}

func forbidden(err error) bool {
	var response *awshttp.ResponseError
	return errors.As(err, &response) && response.HTTPStatusCode() == http.StatusForbidden
}

// again reports whether to send the request once more.
func (r *reauthAPI) again(ctx context.Context, err error) bool {
	if !forbidden(err) {
		return false
	}
	replaced, rerr := r.reauth.Reauthenticate(ctx)
	return rerr == nil && replaced
}

func rewind(body io.Reader) {
	if sk, ok := body.(io.Seeker); ok {
		_, _ = sk.Seek(0, io.SeekStart)
	}
}

func (r *reauthAPI) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	out, err := r.API.PutObject(ctx, in, opts...)
	if r.again(ctx, err) {
		rewind(in.Body)
		return r.API.PutObject(ctx, in, opts...)
	}
	return out, err
}

func (r *reauthAPI) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	out, err := r.API.GetObject(ctx, in, opts...)
	if r.again(ctx, err) {
		return r.API.GetObject(ctx, in, opts...)
	}
	return out, err
}

func (r *reauthAPI) HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	out, err := r.API.HeadObject(ctx, in, opts...)
	if r.again(ctx, err) {
		return r.API.HeadObject(ctx, in, opts...)
	}
	return out, err
}

func (r *reauthAPI) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	out, err := r.API.ListObjectsV2(ctx, in, opts...)
	if r.again(ctx, err) {
		return r.API.ListObjectsV2(ctx, in, opts...)
	}
	return out, err
}

func (r *reauthAPI) PutObjectLegalHold(ctx context.Context, in *s3.PutObjectLegalHoldInput, opts ...func(*s3.Options)) (*s3.PutObjectLegalHoldOutput, error) {
	out, err := r.API.PutObjectLegalHold(ctx, in, opts...)
	if r.again(ctx, err) {
		return r.API.PutObjectLegalHold(ctx, in, opts...)
	}
	return out, err
}

func (r *reauthAPI) PutObjectRetention(ctx context.Context, in *s3.PutObjectRetentionInput, opts ...func(*s3.Options)) (*s3.PutObjectRetentionOutput, error) {
	out, err := r.API.PutObjectRetention(ctx, in, opts...)
	if r.again(ctx, err) {
		return r.API.PutObjectRetention(ctx, in, opts...)
	}
	return out, err
}
