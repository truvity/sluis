package issuerapp_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/kmsfake"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/store"
)

// `adapters.signing: {adapter: kms}` with no settings, beside `signingKey.kms`,
// starts with the keys `signingKey.kms` names: it used to fail with "the kms
// adapter needs keys and stateSecretFile".
func TestABareKMSSigningAdapterUsesSigningKeyKMS(t *testing.T) {
	t.Parallel()
	a, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	plan := port.Table{port.ConcernSigning: {Adapter: "kms"}}
	app := bootDeps(t, issuerapp.Deps{Directory: nobody{}, KMS: kmsFake{"alias/a": a}, Stores: &store.Stores{Plan: plan, Secrets: testSecrets}},
		kmsConfig(t, "alias/a"))
	if keys := keysOf(t, app); len(keys) != 1 || keys[0].Alg != "ES384" {
		t.Fatalf("keys = %+v", keys)
	}
}

func TestABareKMSWrappedSigningAdapterUsesSigningKeyKMSWrapped(t *testing.T) {
	t.Parallel()
	fake := kmsfake.New()
	plan := port.Table{port.ConcernSigning: {Adapter: "kms-wrapped"}}
	app := bootDeps(t, issuerapp.Deps{Directory: nobody{}, KMSWrapped: fake, Stores: &store.Stores{Plan: plan, Secrets: testSecrets}},
		wrappedConfig(t, &config.SigningKeyKMSWrapped{KeyID: "alias/w", Algorithms: []string{"ES384"}}))
	if keys := keysOf(t, app); len(keys) != 1 || keys[0].Alg != "ES384" {
		t.Fatalf("keys = %+v", keys)
	}
}
