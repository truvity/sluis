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

const storedEndpoint = "https://acct.r2.example.test"

func TestACredentialsRefMayBeTheStoredCredentialOfAPreset(t *testing.T) {
	ok := func(ref string) error {
		return blobWith(config.PortsBlobS3{Bucket: "b", Endpoint: storedEndpoint, CredentialsRef: ref}).validatePorts()
	}
	if err := ok("external/cloudflare/hive-r2-sluis-blobs"); err != nil {
		t.Errorf("the stored credential of a preset: %v", err)
	}
	// It is a plain address: no cloudflare section, and no preset to consult.
	c := blobWith(config.PortsBlobS3{Bucket: "b", Endpoint: storedEndpoint, CredentialsRef: "external/cloudflare/blobs"})
	if c.Cloudflare != nil {
		t.Fatal("the fixture has presets")
	}
	if err := c.validatePorts(); err != nil {
		t.Errorf("a stored ref without presets: %v", err)
	}
	for name, ref := range map[string]string{
		"another external kind": "external/oidc/blobs",
		"a nested name":         "external/cloudflare/a/b",
		"upper case":            "external/cloudflare/Blobs",
		"climbing":              "external/cloudflare/../oidc/x",
		"no preset":             "external/cloudflare/",
	} {
		if err := ok(ref); err == nil {
			t.Errorf("%s: %q accepted", name, ref)
		}
	}
	c = blobWith(config.PortsBlobS3{Bucket: "b", CredentialsRef: "external/cloudflare/blobs"})
	if err := c.validatePorts(); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("a stored ref without an endpoint: %v", err)
	}
}

func r2Doc(key, secret string, expires time.Time) secretstore.Cloudflarev1 {
	return secretstore.Cloudflarev1{
		AccessKeyID: key, SecretAccessKey: secret, Endpoint: storedEndpoint, ExpiresOn: expires.UTC().Format(time.RFC3339),
	}
}

func TestStoredPresetCredentialsAreReadAgainWithAThirdOfTheirTimeLeft(t *testing.T) {
	ctx := context.Background()
	stores := secretstore.FromStore(statememory.New(), "")
	doc := stores.External.Cloudflare("blobs")
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	read := storedPresetCredentials(doc, "external/cloudflare/blobs", storedEndpoint, clock)

	if _, err := read(ctx); err == nil || !strings.Contains(err.Error(), "external/cloudflare/blobs") {
		t.Fatalf("a missing document: %v", err)
	}
	rev, err := doc.Put(ctx, r2Doc("token-id-1", "secret-1", now.Add(30*time.Minute)), "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessKeyID != "token-id-1" || got.SecretAccessKey != "secret-1" {
		t.Errorf("credentials = %+v", got)
	}
	// Two thirds of the 30 minutes are used; the pair is read again with a third left.
	if want := now.Add(20 * time.Minute); !got.Expires.Equal(want) {
		t.Errorf("read again at %s, want %s", got.Expires, want)
	}

	// The module rotated: the next read finds the new pair, with no copy kept.
	if _, err = doc.Put(ctx, r2Doc("token-id-2", "secret-2", now.Add(60*time.Minute)), rev); err != nil {
		t.Fatal(err)
	}
	now = now.Add(21 * time.Minute)
	if got, err = read(ctx); err != nil || got.AccessKeyID != "token-id-2" {
		t.Fatalf("after the rotation: %+v, %v", got, err)
	}
	if want := now.Add(26 * time.Minute); !got.Expires.Equal(want) {
		t.Errorf("read again at %s, want %s", got.Expires, want)
	}

	// An expired pair is refused with the address and without a value.
	now = now.Add(2 * time.Hour)
	_, err = read(ctx)
	if err == nil || !strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "secret-2") {
		t.Errorf("an expired pair: %v", err)
	}
}

func TestStoredPresetCredentialsRefuseWhatIsNotTheStoresR2Pair(t *testing.T) {
	ctx := context.Background()
	stores := secretstore.FromStore(statememory.New(), "")
	doc := stores.External.Cloudflare("blobs")
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	read := storedPresetCredentials(doc, "external/cloudflare/blobs", storedEndpoint, func() time.Time { return now })

	other := r2Doc("id", "secret", now.Add(time.Hour))
	other.Endpoint = "https://another.r2.example.test"
	rev, err := doc.Put(ctx, other, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := read(ctx); err == nil || !strings.Contains(err.Error(), "endpoint") || strings.Contains(err.Error(), "secret") {
		t.Errorf("another account's endpoint: %v", err)
	}
	token := secretstore.Cloudflarev1{Token: "a-token-value", ExpiresOn: now.Add(time.Hour).Format(time.RFC3339)}
	if _, err = doc.Put(ctx, token, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := read(ctx); err == nil || !strings.Contains(err.Error(), "no R2 credentials") || strings.Contains(err.Error(), "a-token-value") {
		t.Errorf("a token preset: %v", err)
	}
}

// The adapter is built from the stored credential on layout v4 and answers from
// the endpoint (unreachable here, so the store is unavailable); a missing
// document stops the build, naming the address.
func TestTheBlobReadsTheStoredCredentialOfAPreset(t *testing.T) {
	ctx := context.Background()
	stores := secretstore.FromStore(statememory.New(), "")
	c := blobWith(config.PortsBlobS3{Bucket: "b", Endpoint: "http://127.0.0.1:1", PathStyle: true, CredentialsRef: "external/cloudflare/blobs"})
	c.v4 = &v4Holder{stores: stores}

	if _, err := c.s3Blob(ctx); err == nil || !strings.Contains(err.Error(), "external/cloudflare/blobs") {
		t.Fatalf("a missing document: %v", err)
	}
	// The document's endpoint is the store's own, whatever scheme the test uses.
	d := r2Doc("token-id", "example-secret", time.Now().Add(time.Hour))
	d.Endpoint = "http://127.0.0.1:1"
	if _, err := stores.External.Cloudflare("blobs").Put(ctx, d, ""); err != nil {
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

	// Without the ssm secrets source there is nowhere to read it from.
	c.v4 = &v4Holder{}
	if _, err = c.s3Blob(ctx); err == nil || !strings.Contains(err.Error(), "ssm secrets source") {
		t.Fatalf("no stores: %v", err)
	}
}
