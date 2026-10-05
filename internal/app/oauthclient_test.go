package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/app"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/portstore/portstoretest"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/store"
)

func withClient(o config.OAuthClient) func(*config.Serve) {
	return func(f *config.Serve) { f.OAuthClient = &o }
}

// The client's two halves are the secrets its provider names, read through the
// source: here the environment.
func TestDeclaredOAuthClientFromItsProvidersSecrets(t *testing.T) {
	t.Setenv("SLUIS_SECRET_PROVIDERS_GOOGLE_DEFAULT_CLIENT_ID", "the-id")
	t.Setenv("SLUIS_SECRET_PROVIDERS_GOOGLE_DEFAULT_CLIENT_SECRET", "s3cret")
	cfg := serveConfig(t, withClient(config.OAuthClient{Provider: "default"}))
	kept, err := app.OpenStoresForTest(context.Background(), cfg,
		&store.Stores{Adapter: store.AdapterLegacy, Secrets: secrets.Env{}}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if c := kept.OAuthClient; c.ID != "the-id" || c.Secret != "s3cret" || !c.Declared {
		t.Errorf("memory path kept %+v", c)
	}
}

func TestDeclaredOAuthClientReachesThePortStores(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{"client-id": "the-id\n", "client-secret": "s3cret\n"} {
		p := filepath.Join(dir, "providers", "google", "default", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := serveConfig(t, withClient(config.OAuthClient{Provider: "default"}))
	portstoretest.Each(t, func(t *testing.T, e portstoretest.Env) {
		st := &store.Stores{Ports: e.Open(t), Adapter: store.AdapterDynamoDB, Shared: true, Usable: true, Secrets: secrets.File{Root: dir}}
		kept, err := app.OpenStoresForTest(context.Background(), cfg, st, quiet)
		if err != nil {
			t.Fatal(err)
		}
		if c := kept.OAuthClient; c.ID != "the-id" || c.Secret != "s3cret" || !c.Declared || !c.Configured() {
			t.Errorf("port path kept %+v", c)
		}
	})
}

func TestDeclaredOAuthClientIsRefusedWhenWrong(t *testing.T) {
	t.Setenv("SLUIS_SECRET_PROVIDERS_GOOGLE_DEFAULT_CLIENT_SECRET", "s3cret")
	for name, tc := range map[string]struct {
		o    config.OAuthClient
		want string
	}{
		"id only":          {config.OAuthClient{ID: "x"}, "secret"},
		"a secret missing": {config.OAuthClient{ID: "x", Provider: "other"}, "providers/google/other/client-secret"},
		"an id missing":    {config.OAuthClient{Provider: "default"}, "providers/google/default/client-id"},
		"two ways":         {config.OAuthClient{ID: "x", Provider: "default", SecretName: "k"}, "one way"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := serveConfig(t, withClient(tc.o))
			_, err := app.OpenStoresForTest(context.Background(), cfg,
				&store.Stores{Adapter: store.AdapterLegacy, Secrets: secrets.Env{}}, quiet)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
