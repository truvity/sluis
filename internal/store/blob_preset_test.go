//nolint:lll // messages and fixtures are prose and one-line tables
package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secretstore"
	statememory "github.com/truvity/sluis/storage/state/memory"
)

// R2 on a static document is the default path and asks nothing of Cloudflare:
// no `cloudflare` section, no preset, no minter credential. The service
// document that says so loads, and the blob adapter builds from the document at
// the internal address.
func TestBlobsOnR2StartFromAStaticDocumentWithNoCloudflareSection(t *testing.T) {
	ctx := context.Background()
	f, err := config.Load[config.Serve](writeServe(t, `
apiVersion: sluis.truvity.github.io/serve/v2
issuerURL: https://access.example
secrets: {source: ssm, root: /sluis/example, layout: v4}
ports:
  adapter: memory
  blob:
    adapter: s3
    s3:
      bucket: example-bucket
      endpoint: https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com
      pathStyle: true
      credentialsRef: internal/blobs/r2
`))
	if err != nil {
		t.Fatalf("the document does not load: %v", err)
	}
	if f.Cloudflare != nil {
		t.Fatal("the test document has a cloudflare section")
	}
	c, err := FromServe(f)
	if err != nil {
		t.Fatal(err)
	}
	if c.Cloudflare != nil || c.Blob.S3.Credentials != nil {
		t.Fatalf("a static document pulled in Cloudflare: %+v", c.Blob.S3)
	}
	if err = c.validatePorts(); err != nil {
		t.Fatal(err)
	}
	stores := secretstore.FromStore(statememory.New(), secretstore.LayoutV4, "")
	c.v4 = &v4Holder{stores: stores}
	doc, _ := stores.Internal.S3Credentials("internal/blobs/r2")
	if _, err = doc.Put(ctx, secretstore.S3Credentialsv1{AccessKeyID: "example-id", SecretAccessKey: "example-secret"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = c.s3Blob(ctx); err != nil {
		t.Fatalf("the blob does not build from a static document: %v", err)
	}
}

func TestACredentialsPresetIsHeldToTheCloudflareSection(t *testing.T) {
	section := &config.Cloudflare{
		Accounts: map[string]config.CloudflareAccount{"main": {ID: "0123456789abcdef0123456789abcdef", Minter: "internal/cloudflare/main/minter"}},
		Presets: map[string]config.CloudflarePreset{
			"r2-blobs": {Account: "main", Prototype: "proto-r2-00001", Description: "blobs", Lifetime: config.Duration(15 * time.Minute), Rotation: config.Duration(5 * time.Minute), Endpoint: "https://e.example"},
			"dns":      {Account: "main", Prototype: "proto-dns-0001", Description: "dns", Lifetime: config.Duration(15 * time.Minute), Rotation: config.Duration(5 * time.Minute)},
		},
	}
	for name, tc := range map[string]struct {
		s3   config.PortsBlobS3
		want string
	}{
		"both ways":      {config.PortsBlobS3{Bucket: "b", Endpoint: "https://e.example", CredentialsRef: "internal/blobs/r2", Credentials: &config.PortsBlobCredentials{Preset: "r2-blobs"}}, "exclusive"},
		"no endpoint":    {config.PortsBlobS3{Bucket: "b", Credentials: &config.PortsBlobCredentials{Preset: "r2-blobs"}}, "endpoint"},
		"unknown preset": {config.PortsBlobS3{Bucket: "b", Endpoint: "https://e.example", Credentials: &config.PortsBlobCredentials{Preset: "nope"}}, "not in cloudflare.presets"},
		"a token preset": {config.PortsBlobS3{Bucket: "b", Endpoint: "https://e.example", Credentials: &config.PortsBlobCredentials{Preset: "dns"}}, "not an R2 preset"},
	} {
		c := blobWith(tc.s3)
		c.Cloudflare = section
		if err := c.validatePorts(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	c := blobWith(config.PortsBlobS3{Bucket: "b", Endpoint: "https://e.example", Credentials: &config.PortsBlobCredentials{Preset: "r2-blobs"}})
	c.Cloudflare = section
	if err := c.validatePorts(); err != nil {
		t.Errorf("a good preset: %v", err)
	}
	c.Cloudflare = nil
	if err := c.validatePorts(); err == nil {
		t.Error("a preset with no cloudflare section")
	}
}

type presetAPI struct {
	mu      sync.Mutex
	creates int
}

func (a *presetAPI) GetToken(context.Context, string) (cloudflare.Token, error) {
	return cloudflare.Token{ID: "proto-r2-00001", Status: cloudflare.StatusDisabled,
		Policies: json.RawMessage(`[{"effect":"allow","resources":{"r":"*"},"permission_groups":[{"id":"g-r2"}]}]`)}, nil
}
func (a *presetAPI) CreateToken(_ context.Context, in cloudflare.NewToken) (cloudflare.Created, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.creates++
	return cloudflare.Created{ID: "k" + string(rune('0'+a.creates)), Value: "v", ExpiresOn: in.ExpiresOn}, nil
}
func (a *presetAPI) DeleteToken(context.Context, string) error              { return nil }
func (a *presetAPI) ListTokens(context.Context) ([]cloudflare.Token, error) { return nil, nil }
func (a *presetAPI) PermissionGroups(context.Context) (map[string]string, error) {
	return map[string]string{"g-r2": "Workers R2 Storage Write"}, nil
}

// The blob mints its own credentials from the preset: no s3-credentials
// document exists, and the minter credential is what clones the prototype.
func TestTheBlobMintsItsOwnR2CredentialsFromAPreset(t *testing.T) {
	ctx := context.Background()
	stores := secretstore.FromStore(statememory.New(), secretstore.LayoutV4, "")
	mv, _ := stores.Internal.CloudflareMinter("internal/cloudflare/main/minter")
	if _, err := mv.Put(ctx, secretstore.CloudflareMinterv1{Token: "minter-secret"}, ""); err != nil {
		t.Fatal(err)
	}
	api := &presetAPI{}
	c := blobWith(config.PortsBlobS3{Bucket: "b", Endpoint: "http://127.0.0.1:1", PathStyle: true, Credentials: &config.PortsBlobCredentials{Preset: "r2-blobs"}})
	c.Instance = "example"
	c.Cloudflare = &config.Cloudflare{
		Accounts: map[string]config.CloudflareAccount{"main": {ID: "0123456789abcdef0123456789abcdef", Minter: "internal/cloudflare/main/minter"}},
		Presets: map[string]config.CloudflarePreset{"r2-blobs": {Account: "main", Prototype: "proto-r2-00001", Description: "blobs",
			Lifetime: config.Duration(15 * time.Minute), Rotation: config.Duration(5 * time.Minute), Endpoint: "https://127.0.0.1:1"}},
	}
	c.CloudflareDial = func(_ context.Context, _, token string) (minter.API, error) {
		if token != "minter-secret" {
			return nil, errors.New("not the minter credential")
		}
		return api, nil
	}
	c.v4 = &v4Holder{stores: stores}
	blob, err := c.s3Blob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if api.creates != 1 {
		t.Fatalf("creates = %d, want 1 at build", api.creates)
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err = blob.Read(rctx, "reports/x"); !errors.Is(err, port.ErrUnavailable) {
		t.Fatalf("Read = %v, want ErrUnavailable", err)
	}
}

func writeServe(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "serve.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
