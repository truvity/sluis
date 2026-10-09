package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secretstore"
	statememory "github.com/truvity/sluis/storage/state/memory"
)

func blobWith(s3 config.PortsBlobS3) Config {
	return Config{Adapter: AdapterMemory, Blob: &config.PortsBlob{Adapter: BlobS3, S3: &s3}}
}

func TestACredentialsRefMustBeAnInternalAddress(t *testing.T) {
	for name, ref := range map[string]string{
		"an external address": "external/oidc/example",
		"a climbing path":     "internal/../external/oidc/x",
		"one segment":         "internal/example",
		"three segments":      "internal/a/b/c",
		"upper case":          "internal/Blobs/R2",
		"a bare name":         "blobs/r2",
	} {
		c := blobWith(config.PortsBlobS3{Bucket: "b", Endpoint: "https://s3.example.test", CredentialsRef: ref})
		err := c.validatePorts()
		if !errors.Is(err, secretstore.ErrRef) {
			t.Errorf("%s: %q: %v, want ErrRef", name, ref, err)
		}
	}
	c := blobWith(config.PortsBlobS3{Bucket: "b", CredentialsRef: "internal/blobs/r2"})
	if err := c.validatePorts(); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("credentialsRef without endpoint: %v", err)
	}
	c = blobWith(config.PortsBlobS3{Bucket: "b", Endpoint: "https://s3.example.test", CredentialsRef: "internal/blobs/r2"})
	if err := c.validatePorts(); err != nil {
		t.Errorf("a valid reference: %v", err)
	}
}

func TestACredentialsRefNeedsV4Secrets(t *testing.T) {
	c := blobWith(config.PortsBlobS3{Bucket: "b", Endpoint: "http://127.0.0.1:1", CredentialsRef: "internal/blobs/r2"})
	c.v4 = &v4Holder{}
	if _, err := c.s3Blob(context.Background()); err == nil || !strings.Contains(err.Error(), "ssm secrets source") {
		t.Fatalf("no v4 stores: %v", err)
	}
}

// The credentials are read from the internal address at build time: a missing
// document stops the build, and a present one builds an adapter that answers
// from the endpoint (unreachable here, so the store is unavailable).
func TestTheBlobReadsItsCredentialsFromTheInternalAddress(t *testing.T) {
	ctx := context.Background()
	stores := secretstore.FromStore(statememory.New(), "")
	c := blobWith(config.PortsBlobS3{Bucket: "b", Endpoint: "http://127.0.0.1:1", PathStyle: true, CredentialsRef: "internal/blobs/r2"})
	c.v4 = &v4Holder{stores: stores}

	if _, err := c.s3Blob(ctx); err == nil || !strings.Contains(err.Error(), "internal/blobs/r2") {
		t.Fatalf("a missing document: %v", err)
	}

	doc, err := stores.Internal.S3Credentials("internal/blobs/r2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = doc.Put(ctx, secretstore.S3Credentialsv1{AccessKeyID: "example-id", SecretAccessKey: "example-secret"}, ""); err != nil {
		t.Fatal(err)
	}
	blob, err := c.s3Blob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err = blob.Read(rctx, "reports/x"); !errors.Is(err, port.ErrUnavailable) {
		t.Fatalf("Read = %v, want ErrUnavailable", err)
	}
	if err != nil && strings.Contains(err.Error(), "example-secret") {
		t.Fatalf("the error carries the secret: %v", err)
	}
}
