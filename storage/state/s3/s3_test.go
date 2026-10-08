package s3_test

import (
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/conformance"
	"github.com/truvity/sluis/storage/state/s3"
)

// The tests run against LocalStack and skip without STORAGE_LOCALSTACK_URL;
// hack/storage-conformance.sh sets it and fails if anything skipped.
func bucket(t *testing.T) (*awss3.Client, string, string) {
	url := os.Getenv("STORAGE_LOCALSTACK_URL")
	if url == "" {
		t.Skip("STORAGE_LOCALSTACK_URL is not set: no S3 to test against")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	cfg, err := awsconfig.LoadDefaultConfig(t.Context(), awsconfig.WithRegion("eu-west-1"))
	if err != nil {
		t.Fatal(err)
	}
	c := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(url)
		o.UsePathStyle = true
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	name := "storage-state-test"
	if _, err := c.CreateBucket(t.Context(), &awss3.CreateBucketInput{
		Bucket:                    aws.String(name),
		CreateBucketConfiguration: &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraintEuWest1},
	}); err != nil && !strings.Contains(err.Error(), "BucketAlready") {
		t.Fatal(err)
	}
	if _, err := c.PutBucketVersioning(t.Context(), &awss3.PutBucketVersioningInput{
		Bucket:                  aws.String(name),
		VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	}); err != nil {
		t.Fatal(err)
	}
	return c, name, url
}

func run(t *testing.T, cfg func(s3.Config) s3.Config) {
	c, name, _ := bucket(t)
	conformance.Run(t, func(t *testing.T) state.Store {
		return s3.New(c, cfg(s3.Config{Bucket: name, Prefix: "t/" + strings.ReplaceAll(t.Name(), "/", "-")}))
	})
}

func TestConformance(t *testing.T) {
	run(t, func(c s3.Config) s3.Config { return c })
}

func TestConformanceConditional(t *testing.T) {
	run(t, func(c s3.Config) s3.Config { c.Conditional = true; return c })
}

func TestConformanceCompressed(t *testing.T) {
	run(t, func(c s3.Config) s3.Config { c.Compress = true; return c })
}

func TestOpenHonoursEndpointAndRegion(t *testing.T) {
	_, name, url := bucket(t)
	s, err := s3.Open(t.Context(), s3.Config{Bucket: name, Prefix: "t/open", Compress: true}, state.WithEndpoint(url), state.WithRegion("eu-west-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), "k", []byte(`{"a":1}`), ""); err != nil {
		t.Fatal(err)
	}
	it, err := s.Get(t.Context(), "k")
	if err != nil || string(it.Value) != `{"a":1}` {
		t.Fatalf("%q, %v", it.Value, err)
	}
	if err := s.Delete(t.Context(), "k"); err != nil {
		t.Fatal(err)
	}
}
