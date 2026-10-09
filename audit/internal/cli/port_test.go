//nolint:lll // fixtures are one-line documents
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/truvity/sluis/storage/state/memory"
)

// The store's credentials are a JSON object at an address of the installation's
// state store. A pair that is incomplete, or carries anything else, is refused
// by field name and never echoed.
func TestTheArchiveCredentialsAreReadFromTheStateStore(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		value string
		id    string
		fail  string
	}{
		"a pair":          {value: `{"accessKeyID":"id-1","secretAccessKey":"s3cret-1"}`, id: "id-1"},
		"no secret":       {value: `{"accessKeyID":"id-1"}`, fail: "must hold both"},
		"an extra field":  {value: `{"accessKeyID":"a","secretAccessKey":"s3cret-2","token":"t"}`, fail: "token"},
		"an empty object": {value: `{}`, fail: "must hold both"},
	} {
		t.Run(name, func(t *testing.T) {
			st := memory.New()
			if _, err := st.Put(ctx, "internal/archive", []byte(tc.value), ""); err != nil {
				t.Fatal(err)
			}
			id, secret, err := readCredentials(ctx, st, "internal/archive")
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.fail) {
					t.Fatalf("err = %v, want it to say %q", err, tc.fail)
				}
				if strings.Contains(err.Error(), "s3cret") {
					t.Fatalf("the error echoes a secret: %v", err)
				}
				return
			}
			if err != nil || id != tc.id || secret == "" {
				t.Fatalf("got %q, %q, %v", id, secret, err)
			}
		})
	}
	if _, _, err := readCredentials(ctx, memory.New(), "internal/archive"); err == nil || !strings.Contains(err.Error(), "internal/archive") {
		t.Fatalf("a missing credential: %v", err)
	}
}

func TestTheMinterTokenIsReadFromItsDocumentAndNeverEchoed(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	for addr, value := range map[string]string{
		"good": `{"schema":"cloudflare-minter/v1","token":"m1nter-secret"}`,
		"old":  `{"schema":"other/v1","token":"m1nter-secret"}`,
		"none": `{"schema":"cloudflare-minter/v1"}`,
	} {
		if _, err := st.Put(ctx, "internal/"+addr, []byte(value), ""); err != nil {
			t.Fatal(err)
		}
	}
	if tok, err := readMinter(ctx, st, "internal/good"); err != nil || tok != "m1nter-secret" {
		t.Fatalf("good: %q %v", tok, err)
	}
	for _, addr := range []string{"internal/old", "internal/none", "internal/absent"} {
		if _, err := readMinter(ctx, st, addr); err == nil || strings.Contains(err.Error(), "m1nter-secret") {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

// The rotated document is read at its address of the sluis installation's
// secret store, again after a 403 once it was rotated, and its secret is never
// in an error.
func TestTheRotatedCredentialsAreReadFromTheSluisStore(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	doc := func(id string) []byte {
		return []byte(`{"schema":"cloudflare/v1","access_key_id":"` + id + `","secret_access_key":"s3cret-` + id + `","endpoint":"https://acct.r2.example.test","expires_on":"` +
			time.Now().Add(15*time.Minute).UTC().Format(time.RFC3339) + `"}`)
	}
	rev, err := st.Put(ctx, "external/cloudflare/r2", doc("k1"), "")
	if err != nil {
		t.Fatal(err)
	}
	prov, err := storedFromState(st, "/sluis/main", "external/cloudflare/r2", "HTTPS://acct.r2.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	cache := aws.NewCredentialsCache(prov)
	if c, err := cache.Retrieve(ctx); err != nil || c.AccessKeyID != "k1" {
		t.Fatalf("first read: %+v %v", c, err)
	}
	if _, err = st.Put(ctx, "external/cloudflare/r2", doc("k2"), rev); err != nil {
		t.Fatal(err)
	}
	if replaced, err := reauthenticate(prov, cache).Reauthenticate(ctx); err != nil || !replaced {
		t.Fatalf("after a 403: %v %v", replaced, err)
	}
	if c, _ := cache.Retrieve(ctx); c.AccessKeyID != "k2" {
		t.Fatalf("after a rotation: %s", c.AccessKeyID)
	}

	missing, _ := storedFromState(st, "/sluis/main", "external/cloudflare/absent", "")
	if _, err := missing.Retrieve(ctx); err == nil || !strings.Contains(err.Error(), "/sluis/main/external/cloudflare/absent") {
		t.Fatalf("a missing document: %v", err)
	}
	if _, err = st.Put(ctx, "external/cloudflare/token", []byte(`{"schema":"cloudflare/v1","token":"s3cret-t","expires_on":"2099-01-01T00:00:00Z"}`), ""); err != nil {
		t.Fatal(err)
	}
	token, _ := storedFromState(st, "/sluis/main", "external/cloudflare/token", "")
	if _, err := token.Retrieve(ctx); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("a token preset's document: %v", err)
	}
}

// With archive.sluisDir the document is the file a secrets operator projected.
func TestTheRotatedCredentialsAreReadFromAProjectedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "external", "cloudflare"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"schema":"cloudflare/v1","access_key_id":"k1","secret_access_key":"s","expires_on":"` + time.Now().Add(15*time.Minute).UTC().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "external", "cloudflare", "r2"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	prov, err := storedProvider(context.Background(), StoredCredentials{Ref: "external/cloudflare/r2", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := prov.Retrieve(context.Background()); err != nil || c.AccessKeyID != "k1" {
		t.Fatalf("%+v %v", c, err)
	}
}

// A document for another endpoint than the preset's is refused, naming its
// address and both endpoints and never the secret.
func TestARotatedDocumentForAnotherEndpointIsRefused(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	doc := `{"schema":"cloudflare/v1","access_key_id":"k1","secret_access_key":"s3cret-k1","endpoint":"https://other.r2.example.test","expires_on":"` +
		time.Now().Add(15*time.Minute).UTC().Format(time.RFC3339) + `"}`
	if _, err := st.Put(ctx, "external/cloudflare/r2", []byte(doc), ""); err != nil {
		t.Fatal(err)
	}
	prov, err := storedFromState(st, "/sluis/main", "external/cloudflare/r2", "https://acct.r2.example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = prov.Retrieve(ctx)
	if err == nil {
		t.Fatal("a document for another endpoint was accepted")
	}
	for _, want := range []string{"/sluis/main/external/cloudflare/r2", "https://other.r2.example.test", "https://acct.r2.example.test"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("the refusal carries the secret: %v", err)
	}
}
