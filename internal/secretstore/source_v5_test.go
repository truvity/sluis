package secretstore_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/secretstore/secretrec"
)

func TestSourceV5ReadsTheMovedNamesAtTheirModuleFirstAddress(t *testing.T) {
	ctx := context.Background()
	rec := secretrec.New()
	v5 := secretstore.FromStoreV5(rec, "")
	put(t, v5.OIDC().Client("rp"))
	put(t, v5.OIDC().SignInClientID("google"))
	put(t, v5.OIDC().SignInClientSecret("google"))
	rec.Reset()

	src := secretstore.NewSourceV5(v5, nil)
	for _, name := range []string{secrets.ClientSecret("rp"), secrets.ProviderClientID("google"), secrets.ProviderClientSecret("google")} {
		if got, err := src.Get(ctx, name); err != nil || got != "x" {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
	want := []string{
		"internal/oidc/clients/rp",
		"internal/oidc/signin/google/client-id",
		"internal/oidc/signin/google/client-secret",
	}
	if got := rec.Addresses("get"); !slices.Equal(got, want) {
		t.Errorf("read %v, want %v", got, want)
	}
	if _, err := src.Get(ctx, secrets.ClientSecret("absent")); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("an absent client secret = %v", err)
	}
	if _, err := src.Get(ctx, "valkey/password"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("a name that did not move, with no next source = %v", err)
	}
	if src.Describe(secrets.ClientSecret("rp")) != "ssm internal/oidc/clients/rp" {
		t.Errorf("Describe = %q", src.Describe(secrets.ClientSecret("rp")))
	}

	// A name that did not move is delivered by the next source.
	next := secretstore.NewSourceV5(v5, fixed{"valkey/password": "pw"})
	if got, err := next.Get(ctx, "valkey/password"); err != nil || got != "pw" {
		t.Errorf("next = %q, %v", got, err)
	}
}

type fixed map[string]string

func (f fixed) Get(_ context.Context, name string) (string, error) {
	if v, ok := f[name]; ok {
		return v, nil
	}
	return "", secrets.ErrNotFound
}
func (f fixed) Describe(name string) string { return "fixed " + name }

// The generated clients' records are external/oidc/<id> on layout v5, the same
// document as layout v4, and nothing else goes through the port.
func TestSecretsV5ClientRecordsAreExternalOIDC(t *testing.T) {
	ctx := context.Background()
	rec := secretrec.New()
	v5 := secretstore.FromStoreV5(rec, "")
	s := secretstore.NewSecretsV5(v5, time.Hour)

	path := clientcreds.Path("rp")
	body, err := (&clientcreds.Record{V: clientcreds.RecordVersion, Current: "first"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	ver, err := s.PutIfVersion(ctx, path, body, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, path)
	if err != nil || got.Version != ver {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	doc, _, err := v5.OIDCExternal().Client("rp").Get(ctx)
	if err != nil || doc.ClientSecret != "first" || doc.ClientID != "rp" {
		t.Fatalf("document = %+v, %v", doc, err)
	}
	names, err := s.List(ctx, "credentials/oidc-client")
	if err != nil || !slices.Equal(names, []string{path}) {
		t.Errorf("List = %v, %v", names, err)
	}
	if err = s.Delete(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, path); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("after Delete = %v", err)
	}
	for _, op := range []string{"get", "put", "delete"} {
		for _, a := range rec.Addresses(op) {
			if a != "external/oidc/rp" {
				t.Errorf("%s touched %s, want only external/oidc/rp", op, a)
			}
		}
	}
	if _, err = s.Get(ctx, "credentials/github-app/a/r"); !errors.Is(err, port.ErrUnsupported) {
		t.Errorf("another module's path = %v, want ErrUnsupported", err)
	}
	if _, err = s.Put(ctx, "credentials/github-app/a/r", []byte("x")); !errors.Is(err, port.ErrUnsupported) {
		t.Errorf("a write to another module's path = %v, want ErrUnsupported", err)
	}
}
