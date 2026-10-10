// Package s3 is a [state.Store] over an S3-compatible bucket: AWS S3,
// Cloudflare R2, or an emulator.
//
// A key is the object prefix+key. The bucket must have versioning enabled:
// the Rev is the object's VersionId, [state.Store.GetRev] reads that version,
// and Item.Previous is the next older version. Put on an unversioned bucket
// fails, since it has no Rev to return. Delete removes every version and
// delete marker of the key, so a deleted key has no history.
//
// # Conditional Put
//
// The caller's ifRev is a VersionId; S3's preconditions speak ETag. Put
// therefore reads the current object's version and ETag, compares the version
// to ifRev, and writes. With [Config.Conditional] the write carries
// If-None-Match: * (create) or If-Match: <etag> (update), so a concurrent
// writer makes S3 refuse it (412 or 409) and Put returns [state.ErrConflict]:
// the compare-and-set is atomic. AWS S3 and Cloudflare R2 support this. A
// service that ignores the headers still works, but then the check is
// read-then-write and a race between two writers can overwrite one write
// with the other; leave Conditional false for such a service to say so.
//
// # Compression
//
// With [Config.Compress] a value is stored gzipped (Content-Encoding: gzip)
// and uncompressed on read. The SHA-256 of the bytes sent is computed up front
// and sent as ChecksumSHA256, rather than asking the SDK for a
// ChecksumAlgorithm: the SDK would add a trailing checksum with
// aws-chunked encoding, which R2 and several emulators reject on an object
// with a Content-Encoding.
package s3

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/truvity/sluis/storage/state"
)

// ErrUnversioned reports that the bucket returned no VersionId; enable versioning.
var ErrUnversioned = errors.New("s3: the bucket is not versioned (no VersionId returned)")

// API is the part of the S3 client the store uses; *s3.Client satisfies it.
type API interface {
	GetObject(ctx context.Context, in *awss3.GetObjectInput, opts ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	HeadObject(ctx context.Context, in *awss3.HeadObjectInput, opts ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	PutObject(ctx context.Context, in *awss3.PutObjectInput, opts ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	DeleteObjects(ctx context.Context, in *awss3.DeleteObjectsInput, opts ...func(*awss3.Options)) (*awss3.DeleteObjectsOutput, error)
	ListObjectsV2(ctx context.Context, in *awss3.ListObjectsV2Input, opts ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
	ListObjectVersions(ctx context.Context, in *awss3.ListObjectVersionsInput, opts ...func(*awss3.Options)) (*awss3.ListObjectVersionsOutput, error)
}

// Config selects the bucket and how it is written.
type Config struct {
	Bucket string
	// Prefix is the object-key prefix of the root store, for example "state".
	Prefix string
	// Conditional sends If-Match and If-None-Match on Put; see the package doc.
	Conditional bool
	// Compress gzips stored values; see the package doc.
	Compress bool
	// KMSKeyID, if set, asks for SSE-KMS with that key (state.WithKeyAlias
	// on a Child overrides it).
	KMSKeyID string
	// Credentials overrides the default AWS credential chain in [Open]; for
	// R2 and other S3-compatible services.
	Credentials aws.CredentialsProvider
}

type store struct {
	api    API
	cfg    Config
	prefix string // "" or ends with "/"
}

// New returns the root store of cfg over api.
func New(api API, cfg Config) state.Store {
	return &store{api: api, cfg: cfg, prefix: dir(cfg.Prefix)}
}

// Open builds an S3 client from the default AWS configuration (or
// cfg.Credentials), honouring state.WithRegion and state.WithEndpoint. With an
// endpoint it uses path-style addressing and sends checksums only where the
// API requires them, which S3-compatible services accept.
func Open(ctx context.Context, cfg Config, opts ...state.Option) (state.Store, error) {
	o := state.ResolveOptions(state.Options{KeyAlias: cfg.KMSKeyID}, opts...)
	cfg.KMSKeyID = o.KeyAlias
	var load []func(*awsconfig.LoadOptions) error
	if o.Region != "" {
		load = append(load, awsconfig.WithRegion(o.Region))
	}
	if cfg.Credentials != nil {
		load = append(load, awsconfig.WithCredentialsProvider(cfg.Credentials))
	}
	acfg, err := awsconfig.LoadDefaultConfig(ctx, load...)
	if err != nil {
		return nil, fmt.Errorf("s3: load AWS config: %w", err)
	}
	client := awss3.NewFromConfig(acfg, func(so *awss3.Options) {
		if o.Endpoint != "" {
			so.BaseEndpoint = aws.String(o.Endpoint)
			so.UsePathStyle = true
			so.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			so.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		}
	})
	return New(client, cfg), nil
}

func dir(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

func (s *store) Child(prefix string, opts ...state.Option) state.Store {
	c := *s
	c.prefix = s.prefix + dir(prefix)
	c.cfg.KMSKeyID = state.ResolveOptions(state.Options{KeyAlias: s.cfg.KMSKeyID}, opts...).KeyAlias
	return &c
}

func (s *store) objectKey(key string) (string, error) {
	if err := state.ValidateKey(key); err != nil {
		return "", err
	}
	return s.prefix + key, nil
}

func code(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

func httpStatus(err error) int {
	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

func isNotFound(err error) bool {
	switch code(err) {
	case "NoSuchKey", "NoSuchVersion", "NotFound":
		return true
	}
	return httpStatus(err) == http.StatusNotFound
}

func isPreconditionFailed(err error) bool {
	switch code(err) {
	case "PreconditionFailed", "ConditionalRequestConflict":
		return true
	}
	st := httpStatus(err)
	return st == http.StatusPreconditionFailed || st == http.StatusConflict
}

func (s *store) Get(ctx context.Context, key string) (state.Item, error) {
	return s.read(ctx, key, "")
}

func (s *store) GetRev(ctx context.Context, key string, rev state.Rev) (state.Item, error) {
	if rev == "" {
		return state.Item{}, state.ErrNotFound
	}
	return s.read(ctx, key, rev)
}

func (s *store) read(ctx context.Context, key string, rev state.Rev) (state.Item, error) {
	ok, err := s.objectKey(key)
	if err != nil {
		return state.Item{}, err
	}
	in := &awss3.GetObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(ok)}
	if rev != "" {
		in.VersionId = aws.String(string(rev))
	}
	out, err := s.api.GetObject(ctx, in)
	if err != nil {
		// A malformed version id is "InvalidArgument": no such version.
		if isNotFound(err) || (rev != "" && code(err) == "InvalidArgument") {
			return state.Item{}, state.ErrNotFound
		}
		return state.Item{}, fmt.Errorf("s3: get %s: %w", ok, err)
	}
	defer func() { _ = out.Body.Close() }()
	var body io.Reader = out.Body
	if aws.ToString(out.ContentEncoding) == "gzip" {
		zr, err := gzip.NewReader(out.Body)
		if err != nil {
			return state.Item{}, fmt.Errorf("s3: get %s: %w", ok, err)
		}
		defer func() { _ = zr.Close() }()
		body = zr
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return state.Item{}, fmt.Errorf("s3: read %s: %w", ok, err)
	}
	it := state.Item{Value: b, Rev: state.Rev(aws.ToString(out.VersionId))}
	if out.LastModified != nil {
		it.Modified = *out.LastModified
	}
	if it.Rev == "" {
		return state.Item{}, ErrUnversioned
	}
	prev, err := s.previous(ctx, ok, it.Rev)
	if err != nil {
		return state.Item{}, err
	}
	it.Previous = prev
	return it, nil
}

// previous returns the version of the object key listed right after rev
// (versions are listed newest first), or "".
func (s *store) previous(ctx context.Context, ok string, rev state.Rev) (state.Rev, error) {
	in := &awss3.ListObjectVersionsInput{Bucket: aws.String(s.cfg.Bucket), Prefix: aws.String(ok)}
	seen := false
	for {
		out, err := s.api.ListObjectVersions(ctx, in)
		if err != nil {
			return "", fmt.Errorf("s3: list versions of %s: %w", ok, err)
		}
		for i := range out.Versions {
			v := &out.Versions[i]
			if aws.ToString(v.Key) != ok {
				if seen {
					return "", nil
				}
				continue
			}
			if seen {
				return state.Rev(aws.ToString(v.VersionId)), nil
			}
			if aws.ToString(v.VersionId) == string(rev) {
				seen = true
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return "", nil
		}
		in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}
}

func (s *store) Put(ctx context.Context, key string, value []byte, ifRev state.Rev) (state.Rev, error) {
	ok, err := s.objectKey(key)
	if err != nil {
		return "", err
	}
	if err := state.ValidateObject(value); err != nil {
		return "", err
	}
	head, err := s.api.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(ok)})
	var curRev state.Rev
	var etag *string
	switch {
	case err == nil:
		curRev, etag = state.Rev(aws.ToString(head.VersionId)), head.ETag
	case isNotFound(err):
	default:
		return "", fmt.Errorf("s3: head %s: %w", ok, err)
	}
	if curRev != ifRev {
		return "", state.ErrConflict
	}

	body, enc := value, (*string)(nil)
	if s.cfg.Compress {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(value); err != nil {
			return "", err
		}
		if err := zw.Close(); err != nil {
			return "", err
		}
		body, enc = buf.Bytes(), aws.String("gzip")
	}
	in := &awss3.PutObjectInput{
		Bucket:          aws.String(s.cfg.Bucket),
		Key:             aws.String(ok),
		Body:            bytes.NewReader(body),
		ContentType:     aws.String("application/json"),
		ContentEncoding: enc,
	}
	if enc != nil {
		sum := sha256.Sum256(body)
		in.ChecksumSHA256 = aws.String(base64.StdEncoding.EncodeToString(sum[:]))
	}
	if s.cfg.KMSKeyID != "" {
		in.ServerSideEncryption = types.ServerSideEncryptionAwsKms
		in.SSEKMSKeyId = aws.String(s.cfg.KMSKeyID)
	}
	if s.cfg.Conditional {
		if ifRev == "" {
			in.IfNoneMatch = aws.String("*")
		} else {
			in.IfMatch = etag
		}
	}
	out, err := s.api.PutObject(ctx, in)
	if err != nil {
		if s.cfg.Conditional && isPreconditionFailed(err) {
			return "", state.ErrConflict
		}
		return "", fmt.Errorf("s3: put %s: %w", ok, err)
	}
	if aws.ToString(out.VersionId) == "" {
		return "", ErrUnversioned
	}
	return state.Rev(aws.ToString(out.VersionId)), nil
}

func (s *store) Delete(ctx context.Context, key string) error {
	ok, err := s.objectKey(key)
	if err != nil {
		return err
	}
	var ids []types.ObjectIdentifier
	in := &awss3.ListObjectVersionsInput{Bucket: aws.String(s.cfg.Bucket), Prefix: aws.String(ok)}
	live := false
	for {
		out, err := s.api.ListObjectVersions(ctx, in)
		if err != nil {
			return fmt.Errorf("s3: list versions of %s: %w", ok, err)
		}
		for i := range out.Versions {
			v := &out.Versions[i]
			if aws.ToString(v.Key) == ok {
				live = true
				ids = append(ids, types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
			}
		}
		for _, m := range out.DeleteMarkers {
			if aws.ToString(m.Key) == ok {
				ids = append(ids, types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}
	if !live {
		return state.ErrNotFound
	}
	for len(ids) > 0 {
		n := min(len(ids), 1000)
		res, err := s.api.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
			Bucket: aws.String(s.cfg.Bucket),
			Delete: &types.Delete{Objects: ids[:n], Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("s3: delete %s: %w", ok, err)
		}
		if len(res.Errors) > 0 {
			e := res.Errors[0]
			return fmt.Errorf("s3: delete %s: %s: %s", ok, aws.ToString(e.Code), aws.ToString(e.Message))
		}
		ids = ids[n:]
	}
	return nil
}

func (s *store) List(ctx context.Context) ([]string, error) { return s.list(ctx, true) }

// Walk implements [state.Walker].
func (s *store) Walk(ctx context.Context) ([]string, error) { return s.list(ctx, false) }

func (s *store) list(ctx context.Context, oneLevel bool) ([]string, error) {
	in := &awss3.ListObjectsV2Input{Bucket: aws.String(s.cfg.Bucket), Prefix: aws.String(s.prefix)}
	if oneLevel {
		in.Delimiter = aws.String("/")
	}
	out := []string{}
	for {
		page, err := s.api.ListObjectsV2(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("s3: list %s: %w", s.prefix, err)
		}
		for _, o := range page.Contents {
			if name := strings.TrimPrefix(aws.ToString(o.Key), s.prefix); name != "" {
				out = append(out, name)
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			break
		}
		in.ContinuationToken = page.NextContinuationToken
	}
	sort.Strings(out)
	return out, nil
}
