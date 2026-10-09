package s3blob_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/porttest"
	"github.com/truvity/sluis/internal/port/s3blob"
	"github.com/truvity/sluis/internal/secretstore"
	statememory "github.com/truvity/sluis/storage/state/memory"
)

// EnvURL names a LocalStack (or any S3 and KMS endpoint) the conformance run
// uses. Unset, these tests skip and `go test ./...` stays hermetic; the CI job
// and `just test-s3` set it and refuse a run that skipped.
const EnvURL = "ACCESS_ROSTER_S3_URL"

func localstack(t *testing.T) string {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skipf("%s is not set: no S3 to run the conformance suite against", EnvURL)
	}
	// LocalStack takes any credentials; the SDK needs some.
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	return url
}

func randomName(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func newBucket(t *testing.T, url string) string { return newBucketWith(t, url, "", "") }

func newBucketWith(t *testing.T, url, id, secret string) string {
	t.Helper()
	ctx := context.Background()
	var opts []func(*awsconfig.LoadOptions) error
	if id != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: id, SecretAccessKey: secret}, nil
		})))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(url)
		o.UsePathStyle = true
	})
	bucket := "ar-" + randomName(t)
	if _, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket}); err != nil {
		t.Fatalf("creating the bucket: %v", err)
	}
	return bucket
}

func newKey(t *testing.T, url string) string {
	t.Helper()
	ctx := context.Background()
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := kms.NewFromConfig(cfg, func(o *kms.Options) { o.BaseEndpoint = aws.String(url) }).
		CreateKey(ctx, &kms.CreateKeyInput{})
	if err != nil {
		t.Fatalf("creating the key: %v", err)
	}
	return aws.ToString(out.KeyMetadata.KeyId)
}

func conformance(t *testing.T, kmsKey func(*testing.T, string) string) {
	url := localstack(t)
	bucket := newBucket(t, url)
	key := ""
	if kmsKey != nil {
		key = kmsKey(t, url)
	}
	porttest.RunGroups(t, func(t *testing.T) porttest.Env {
		// A fresh prefix per assertion: they do not see each other's objects.
		b, err := s3blob.New(context.Background(), s3blob.Config{
			Bucket: bucket, Prefix: "run/" + randomName(t), Endpoint: url, PathStyle: true, KMSKey: key,
		})
		if err != nil {
			t.Fatal(err)
		}
		return porttest.Env{Set: port.Set{Blob: b}, BlobPrefixes: []string{"reports/", "google/"}}
	}, "blob/")
}

func TestConformance(t *testing.T) { conformance(t, nil) }

// With SSE-KMS the ETag is no longer an MD5 of the body; the conditional write
// must work all the same.
func TestConformanceWithSSEKMS(t *testing.T) { conformance(t, newKey) }

// A bucket that is not there, or a key the caller cannot use, is the store
// being unavailable, not an empty answer: a reader must not take it for "not
// yet written".
func TestAMissingBucketIsUnavailableNotNotFound(t *testing.T) {
	url := localstack(t)
	b, err := s3blob.New(context.Background(), s3blob.Config{Bucket: "no-such-" + randomName(t), Endpoint: url, PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = b.Read(ctx, "reports/x"); !errors.Is(err, port.ErrUnavailable) {
		t.Fatalf("Read from a missing bucket: %v, want ErrUnavailable", err)
	}
}

// With an endpoint and static credentials read from the installation's secrets
// store (a memory one here) at an internal address, the adapter passes the same
// suite: the SDK's default chain is not what signs.
func TestConformanceWithStaticCredentials(t *testing.T) {
	url := localstack(t)
	// Not the environment's: the credentials must come from the store.
	t.Setenv("AWS_ACCESS_KEY_ID", "wrong")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wrong")
	ctx := context.Background()
	stores := secretstore.FromStore(statememory.New(), "")
	doc, err := stores.Internal.S3Credentials("internal/blobs/example")
	if err != nil {
		t.Fatal(err)
	}
	// LocalStack takes any credentials.
	if _, err = doc.Put(ctx, secretstore.S3Credentialsv1{AccessKeyID: "test", SecretAccessKey: "test"}, ""); err != nil {
		t.Fatal(err)
	}
	bucket := newBucketWith(t, url, "test", "test")
	porttest.RunGroups(t, func(t *testing.T) porttest.Env {
		b, err := s3blob.New(ctx, s3blob.Config{
			Bucket: bucket, Prefix: "run/" + randomName(t), Endpoint: url, PathStyle: true, Region: "auto",
			Credentials: func(ctx context.Context) (s3blob.Credentials, error) {
				got, _, err := doc.Get(ctx)
				return s3blob.Credentials{AccessKeyID: got.AccessKeyID, SecretAccessKey: got.SecretAccessKey}, err
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return porttest.Env{Set: port.Set{Blob: b}, BlobPrefixes: []string{"reports/", "google/"}}
	}, "blob/")
}
