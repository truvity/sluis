package ssm_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/conformance"
	"github.com/truvity/sluis/storage/state/ssm"
)

// The tests run against LocalStack and skip without STORAGE_LOCALSTACK_URL;
// hack/storage-conformance.sh sets it and fails if anything skipped.
func client(t *testing.T) (*awsssm.Client, string) {
	url := os.Getenv("STORAGE_LOCALSTACK_URL")
	if url == "" {
		t.Skip("STORAGE_LOCALSTACK_URL is not set: no SSM to test against")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	cfg, err := awsconfig.LoadDefaultConfig(t.Context(), awsconfig.WithRegion("eu-west-1"))
	if err != nil {
		t.Fatal(err)
	}
	return awsssm.NewFromConfig(cfg, func(o *awsssm.Options) { o.BaseEndpoint = aws.String(url) }), url
}

func TestConformance(t *testing.T) {
	c, _ := client(t)
	conformance.Run(t, func(t *testing.T) state.Store {
		return ssm.New(c, "/storage-test/"+strings.ReplaceAll(t.Name(), "/", "-"))
	})
}

func TestOpenHonoursEndpointAndRegion(t *testing.T) {
	_, url := client(t)
	s, err := ssm.Open(t.Context(), "/storage-test/open", state.WithEndpoint(url), state.WithRegion("eu-west-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), "k", []byte(`{}`), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), "k"); err != nil {
		t.Fatal(err)
	}
}

func TestSizeTiers(t *testing.T) {
	c, _ := client(t)
	s := ssm.New(c, "/storage-test/size")
	big := func(n int) []byte { return []byte(`{"v":"` + strings.Repeat("x", n-8) + `"}`) }
	for _, n := range []int{4096, 4097, 8192} {
		rev, err := s.Put(context.Background(), "k", big(n), "")
		if err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		it, err := s.Get(context.Background(), "k")
		if err != nil || len(it.Value) != n || it.Rev != rev {
			t.Fatalf("%d bytes: read back %d, %v", n, len(it.Value), err)
		}
		if err := s.Delete(context.Background(), "k"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(context.Background(), "k", big(8193), ""); !errors.Is(err, ssm.ErrTooLarge) {
		t.Fatalf("8193 bytes: %v, want ErrTooLarge", err)
	}
}
