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
	"github.com/truvity/sluis/internal/store"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func withClient(o config.OAuthClient) func(*config.Serve) {
	return func(f *config.Serve) { f.OAuthClient = &o }
}

func TestDeclaredOAuthClientFromIDFileAndSecretEnv(t *testing.T) {
	t.Setenv("SLUIS_TEST_OAUTH_SECRET", "s3cret")
	cfg := serveConfig(t, withClient(config.OAuthClient{
		IDFile: writeFile(t, " the-id\n"), SecretEnv: "SLUIS_TEST_OAUTH_SECRET",
	}))
	kept, err := app.OpenStoresForTest(context.Background(), cfg, &store.Stores{Adapter: store.AdapterLegacy}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if c := kept.OAuthClient; c.ID != "the-id" || c.Secret != "s3cret" || !c.Declared {
		t.Errorf("memory path kept %+v", c)
	}
}

func TestDeclaredOAuthClientFromFilesReachesThePortStores(t *testing.T) {
	cfg := serveConfig(t, withClient(config.OAuthClient{
		IDFile: writeFile(t, "the-id\n"), SecretFile: writeFile(t, "s3cret\n"),
	}))
	portstoretest.Each(t, func(t *testing.T, e portstoretest.Env) {
		st := &store.Stores{Ports: e.Open(t), Adapter: store.AdapterDynamoDB, Shared: true, Usable: true}
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
	t.Setenv("SLUIS_TEST_OAUTH_SECRET", "s3cret")
	for name, tc := range map[string]struct {
		o    config.OAuthClient
		want string
	}{
		"id only":          {config.OAuthClient{ID: "x"}, "both"},
		"secret only":      {config.OAuthClient{SecretEnv: "SLUIS_TEST_OAUTH_SECRET"}, "both"},
		"unreadable file":  {config.OAuthClient{ID: "x", SecretFile: "/nonexistent/secret"}, "secretFile"},
		"unset variable":   {config.OAuthClient{ID: "x", SecretEnv: "SLUIS_TEST_OAUTH_UNSET"}, "secretEnv"},
		"two ways":         {config.OAuthClient{ID: "x", SecretEnv: "SLUIS_TEST_OAUTH_SECRET", SecretName: "k"}, "one way"},
		"id twice":         {config.OAuthClient{ID: "x", IDFile: "/y", SecretEnv: "SLUIS_TEST_OAUTH_SECRET"}, "exclusive"},
		"secret both ways": {config.OAuthClient{ID: "x", SecretFile: "/y", SecretEnv: "SLUIS_TEST_OAUTH_SECRET"}, "exclusive"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := app.FromConfig(issuerFile(t, func(f *config.Serve) { f.Demo = false }, withClient(tc.o)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
