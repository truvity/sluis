package issuerapp_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/secretstore/secretrec"
	"github.com/truvity/sluis/internal/store"
)

// v5Stores is the issuer's stores on layout v5 (ADR 0072): the secrets are
// module first behind a recording store, and there is no Secrets port.
func v5Stores(plan port.Table) (*store.Stores, *secretrec.Store, *secretstore.StoresV5) {
	rec := secretrec.New()
	v5 := secretstore.FromStoreV5(rec, "")
	return &store.Stores{
		Plan: plan, V5: v5, Secrets: secretstore.NewSourceV5(v5, nil),
		ClientSecrets: secretstore.NewSecretsV5(v5, 0),
	}, rec, v5
}

// plainPolicy is a policy with no generated client, for tryBoot.
const plainPolicy = `
version: 1
groups:
  platform: { members: [platform@north.example] }
`

func seedStateSecret(t *testing.T, v5 *secretstore.StoresV5) {
	t.Helper()
	body := []byte(base64.StdEncoding.EncodeToString(randomBytes(32)) + "\n")
	if _, err := v5.OIDC().StateSecret().Put(context.Background(), body, ""); err != nil {
		t.Fatal(err)
	}
}

// Whatever name the document gives the state secret, on layout v5 it is
// internal/oidc/state-secret. The wrapped ring boots from it and nothing else
// is read.
func TestKMSWrappedStateSecretOnLayoutV5(t *testing.T) {
	st, rec, v5 := v5Stores(nil)
	seedStateSecret(t, v5)
	rec.Reset()
	fake := newFakeKMS(t)
	app := bootDeps(t, issuerapp.Deps{Directory: nobody{}, Keys: fake, Stores: st}, func(f *config.Serve) {
		wrappedConfig(t, &config.SigningKeyKMSWrapped{Algorithms: []string{"ES384"}})(f)
		f.SigningKey.KMSWrapped.StateSecret = "issuer/state-secret" // a v4 name that is not a file here
	})
	if ks := keysOf(t, app); len(ks) != 1 || ks[0].Alg != "ES384" {
		t.Fatalf("keys = %+v", ks)
	}
	if got := rec.Addresses("get"); !slices.Equal(got, []string{"internal/oidc/state-secret"}) {
		t.Errorf("read %v", got)
	}
	if got := rec.Addresses("put"); len(got) != 0 {
		t.Errorf("wrote %v", got)
	}
}

func TestAMissingStateSecretOnLayoutV5NamesItsAddress(t *testing.T) {
	st, _, _ := v5Stores(nil)
	_, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Keys: newFakeKMS(t), Stores: st},
		replacePolicy(t, plainPolicy), wrappedConfig(t, &config.SigningKeyKMSWrapped{Algorithms: []string{"ES384"}}))
	if err == nil || !strings.Contains(err.Error(), "internal/oidc/state-secret") {
		t.Fatalf("err = %v, want a refusal that names the address", err)
	}
}

// The bare signing adapters of signing_adapter_test.go start on layout v5 too.
func TestBareSigningAdaptersOnLayoutV5(t *testing.T) {
	t.Run("kms", func(t *testing.T) {
		a, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		st, _, v5 := v5Stores(port.Table{port.ConcernSigning: {Adapter: "kms"}})
		seedStateSecret(t, v5)
		app := bootDeps(t, issuerapp.Deps{Directory: nobody{}, KMS: kmsFake{"alias/a": a}, Stores: st}, kmsConfig(t, "alias/a"))
		if keys := keysOf(t, app); len(keys) != 1 || keys[0].Alg != "ES384" {
			t.Fatalf("keys = %+v", keys)
		}
	})
	t.Run("kms-wrapped", func(t *testing.T) {
		st, _, v5 := v5Stores(port.Table{port.ConcernSigning: {Adapter: "kms-wrapped"}})
		seedStateSecret(t, v5)
		app := bootDeps(t, issuerapp.Deps{Directory: nobody{}, Keys: newFakeKMS(t), Stores: st},
			wrappedConfig(t, &config.SigningKeyKMSWrapped{KeyID: "alias/w", Algorithms: []string{"ES384"}}))
		if keys := keysOf(t, app); len(keys) != 1 || keys[0].Alg != "ES384" {
			t.Fatalf("keys = %+v", keys)
		}
	})
}

// The state secret's fingerprint is kept in the oidc module's shared State, and
// a replica with another secret is refused, as on layout v4.
func TestTheStateSecretFingerprintOnLayoutV5(t *testing.T) {
	ctx := context.Background()
	shared, err := store.Open(ctx, store.Config{
		Adapter: store.AdapterMemory, Module: port.ModuleOIDC, SecretsSSM: true, SecretsLayout: config.SecretsLayoutV5,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	fake := newFakeKMS(t)
	boot := func(v5 *secretstore.StoresV5) error {
		st := *shared
		st.Plan = nil
		st.V5, st.Secrets, st.ClientSecrets = v5, secretstore.NewSourceV5(v5, nil), secretstore.NewSecretsV5(v5, 0)
		_, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Keys: fake, Stores: &st},
			replacePolicy(t, plainPolicy), wrappedConfig(t, &config.SigningKeyKMSWrapped{Algorithms: []string{"ES384"}}))
		return err
	}
	_, _, a := v5Stores(nil)
	seedStateSecret(t, a)
	if err = boot(a); err != nil {
		t.Fatal(err)
	}
	if _, err = shared.Ports.State.Get(ctx, "issuer:kms:state-secret-fingerprint"); err != nil {
		t.Errorf("the fingerprint is not in the oidc table: %v", err)
	}
	if err = boot(a); err != nil {
		t.Errorf("a replica with the same secret: %v", err)
	}
	_, _, other := v5Stores(nil)
	seedStateSecret(t, other)
	if err = boot(other); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Errorf("a replica with another secret = %v", err)
	}
}

// The Google sign-in client is internal/oidc/signin/google/client-id and
// client-secret.
func TestTheSignInClientOnLayoutV5(t *testing.T) {
	ctx := context.Background()
	st, rec, v5 := v5Stores(nil)
	if _, err := v5.OIDC().SignInClientID("google").Put(ctx, []byte("client-id.example"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := v5.OIDC().SignInClientSecret("google").Put(ctx, []byte("client-secret-value"), ""); err != nil {
		t.Fatal(err)
	}
	rec.Reset()
	app := bootDeps(t, issuerapp.Deps{Directory: nobody{}, Stores: st}, func(f *config.Serve) {
		f.OAuthClient = &config.OAuthClient{Provider: "google"}
	})
	if app == nil {
		t.Fatal("no app")
	}
	want := []string{"internal/oidc/signin/google/client-id", "internal/oidc/signin/google/client-secret"}
	if got := rec.Addresses("get"); !slices.Equal(got, want) {
		t.Errorf("read %v, want %v", got, want)
	}

	missing, _, _ := v5Stores(nil)
	_, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: missing}, replacePolicy(t, plainPolicy), func(f *config.Serve) {
		f.OAuthClient = &config.OAuthClient{Provider: "google"}
	})
	if err == nil || !strings.Contains(err.Error(), "internal/oidc/signin/google/client-id") {
		t.Errorf("a missing client = %v, want a refusal that names the address", err)
	}
}

// A generated client's record is external/oidc/<id>, byte for byte the v4
// document, and a delivered input is internal/oidc/clients/<id>.
func TestGeneratedClientSecretsOnLayoutV5(t *testing.T) {
	ctx := context.Background()
	st, rec, v5 := v5Stores(nil)
	st.Ports = port.Set{State: memory.New().Set().State}
	st.Shared = true
	if _, err := v5.OIDC().Client("grafana").Put(ctx, []byte("delivered-secret"), ""); err != nil {
		t.Fatal(err)
	}
	rec.Reset()

	app, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: st}, replacePolicy(t, generatingPolicy))
	if err != nil {
		t.Fatal(err)
	}
	doc, _, err := v5.OIDCExternal().Client("grafana").Get(ctx)
	if err != nil || doc.ClientID != "grafana" || doc.ClientSecret != "delivered-secret" {
		t.Fatalf("external/oidc/grafana = %+v, %v (the delivered input is adopted)", doc, err)
	}
	if res := app.ReconcileClientSecrets(ctx); res.Outcomes["grafana"] != clientcreds.OutcomeExisting {
		t.Errorf("second pass = %+v", res)
	}
	if got := rec.Addresses("put"); !slices.Equal(got, []string{"external/oidc/grafana"}) {
		t.Errorf("wrote %v", got)
	}
	for _, a := range rec.Addresses("get") {
		if a != "external/oidc/grafana" && a != "internal/oidc/clients/grafana" {
			t.Errorf("read %s", a)
		}
	}
	if got := rec.Addresses("get"); !slices.Contains(got, "internal/oidc/clients/grafana") {
		t.Errorf("the input was not read: %v", got)
	}
}

// The issuer of acceptance_test.go, on layout v5: it serves its discovery
// document and its keys, and reads no secret it does not need.
func TestTheIssuerServesOnLayoutV5(t *testing.T) {
	st, rec, _ := v5Stores(nil)
	app := bootDeps(t, issuerapp.Deps{Directory: nobody{}, Stores: st})
	for _, path := range []string{"/.well-known/openid-configuration", "/keys"} {
		if code, _ := get(t, app.Handler(), path); code != 200 {
			t.Errorf("%s = %d", path, code)
		}
	}
	if ops := rec.Ops(); len(ops) != 0 {
		t.Errorf("a file-keyed issuer with no OAuth client touched secrets: %v", ops)
	}
}
