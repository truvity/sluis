package app_test

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/truvity/sluis/internal/app"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/portstore/portstoretest"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/secretstore/secretrec"
	"github.com/truvity/sluis/internal/store"
)

// On layout v5 there is no Secrets port, and the console still starts: its
// session key and the Google sign-in client are read from the oidc module's
// addresses, each by the path the ADR names (0072).
func TestTheConsoleStartsOnLayoutV5ByTheOIDCModulesPaths(t *testing.T) {
	ctx := context.Background()
	rec := secretrec.New()
	v5 := secretstore.FromStoreV5(rec, "")
	if _, err := v5.OIDC().SignInClientID("google").Put(ctx, []byte("client-id.example"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := v5.OIDC().SignInClientSecret("google").Put(ctx, []byte("client-secret-value"), ""); err != nil {
		t.Fatal(err)
	}
	rec.Reset()

	set := portstoretest.Envs(t)[0].Open(t)
	set.Secrets = nil
	open := func() app.KeptForTest {
		st := &store.Stores{
			Ports: set, Adapter: store.AdapterDynamoDB, Shared: true, Usable: true,
			V5: v5, Secrets: secretstore.NewSourceV5(v5, nil),
		}
		cfg := serveConfig(t, func(f *config.Serve) { f.OAuthClient = &config.OAuthClient{Provider: "google"} })
		kept, err := app.OpenStoresForTest(ctx, cfg, st, quiet)
		if err != nil {
			t.Fatal(err)
		}
		return kept
	}
	a, b := open(), open()
	if len(a.SessionKey) == 0 || !bytes.Equal(a.SessionKey, b.SessionKey) {
		t.Error("two replicas signed sessions with different keys")
	}
	if !a.OAuthClient.Configured() || a.OAuthClient.ID != "client-id.example" {
		t.Errorf("the Google client = %+v", a.OAuthClient)
	}
	want := []string{
		"internal/oidc/console-session-key",
		"internal/oidc/signin/google/client-id",
		"internal/oidc/signin/google/client-secret",
	}
	if got := rec.Addresses("get"); !slices.Equal(got, want) {
		t.Errorf("read %v, want %v", got, want)
	}
	if got := rec.Addresses("put"); !slices.Equal(got, want[:1]) {
		t.Errorf("wrote %v, want only the session key", got)
	}
}
