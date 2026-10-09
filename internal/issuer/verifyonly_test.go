package issuer_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/signer"
	"github.com/truvity/sluis/policy"
)

func pemBlock(t *testing.T, typ string, der []byte) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

// The kid published for an old key is the one its tokens carry: the key a file
// signer held, derived the same way from the public half.
func TestAVerifyOnlyKeyKeepsTheKidTheFileSignerGave(t *testing.T) {
	t.Parallel()
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	old, err := signer.ParseSigningKey(pemBlock(t, "PRIVATE KEY", der))
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	got, err := issuer.ParseVerifyOnlyKey(pemBlock(t, "PUBLIC KEY", pubDER), "", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != old.ID() || got.Alg != jose.ES384 {
		t.Fatalf("kid %q alg %s, want the signer's %q ES384", got.ID, got.Alg, old.ID())
	}
	// A kid and an algorithm may be stated; the algorithm must suit the key.
	got, err = issuer.ParseVerifyOnlyKey(pemBlock(t, "PUBLIC KEY", pubDER), "legacy-1", "ES384", time.Time{})
	if err != nil || got.ID != "legacy-1" {
		t.Fatalf("explicit kid: %q %v", got.ID, err)
	}
	if _, err = issuer.ParseVerifyOnlyKey(pemBlock(t, "PUBLIC KEY", pubDER), "", "RS256", time.Time{}); err == nil {
		t.Error("an algorithm that does not suit the key was accepted")
	}
	// A JWK is read, with its own kid.
	jwk, _ := json.Marshal(jose.JSONWebKey{Key: &priv.PublicKey, KeyID: "from-jwk"})
	if got, err = issuer.ParseVerifyOnlyKey(jwk, "", "", time.Time{}); err != nil || got.ID != "from-jwk" {
		t.Errorf("jwk: %q %v", got.ID, err)
	}
}

// A private key is refused, in every encoding, and the error carries none of it.
func TestAVerifyOnlyKeyRefusesAPrivateKey(t *testing.T) {
	t.Parallel()
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(ec)
	sec1, _ := x509.MarshalECPrivateKey(ec)
	jwk, _ := json.Marshal(jose.JSONWebKey{Key: ec, KeyID: "k"})
	for name, raw := range map[string][]byte{
		"pkcs8":               pemBlock(t, "PRIVATE KEY", pkcs8),
		"sec1":                pemBlock(t, "EC PRIVATE KEY", sec1),
		"pkcs1":               pemBlock(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rk)),
		"jwk":                 jwk,
		"public then private": append(pemBlock(t, "PUBLIC KEY", mustPKIX(t, &ec.PublicKey)), pemBlock(t, "PRIVATE KEY", pkcs8)...),
	} {
		_, err := issuer.ParseVerifyOnlyKey(raw, "", "", time.Time{})
		if err == nil || !strings.Contains(err.Error(), "PUBLIC key") {
			t.Errorf("%s: %v, want a refusal of the private key", name, err)
		}
	}
	if _, err := issuer.ParseVerifyOnlyKey([]byte("nonsense"), "", "", time.Time{}); err == nil {
		t.Error("nonsense accepted")
	}
}

func mustPKIX(t *testing.T, pub any) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// The JWKS carries the verify-only key until its `until`, beside the ring's own,
// and the discovery algorithms follow; the issuer verifies its own old token.
func TestVerifyOnlyKeysArePublishedUntilTheyExpire(t *testing.T) {
	t.Parallel()
	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	shared := issuer.NewMemoryState()
	iss := issuer.New(issuer.Config{URL: "https://issuer.example"}, set, &fakeDirectory{}, shared)
	ec, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	storage, err := issuer.NewStorage(iss, fakeVerifier{}, nil, nil, nil, shared)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	forever, _ := issuer.ParseVerifyOnlyKey(pemBlock(t, "PUBLIC KEY", mustPKIX(t, &ec.PublicKey)), "old-es", "", time.Time{})
	bounded, _ := issuer.ParseVerifyOnlyKey(pemBlock(t, "PUBLIC KEY", mustPKIX(t, &rk.PublicKey)), "old-rs", "", now.Add(time.Hour))
	storage.UseVerifyOnly([]issuer.VerifyOnlyKey{forever, bounded}, func() time.Time { return now })

	ids := func() map[string]bool {
		keys, err := storage.KeySet(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, k := range keys {
			out[k.ID()] = true
		}
		return out
	}
	if got := ids(); !got["old-es"] || !got["old-rs"] || len(got) != 3 {
		t.Fatalf("published %v, want the ring's key and both old ones", got)
	}
	now = now.Add(time.Hour)
	if got := ids(); !got["old-es"] || got["old-rs"] || len(got) != 2 {
		t.Fatalf("published %v after the bounded key's until", got)
	}
	// What the old key signed verifies with the published set.
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES384, Key: jose.JSONWebKey{Key: ec, KeyID: "old-es"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jws, _ := signer.Sign([]byte("old token"))
	keys, _ := storage.KeySet(context.Background())
	verified := false
	for _, k := range keys {
		if _, err := jws.Verify(&jose.JSONWebKey{Key: k.Key(), KeyID: k.ID(), Algorithm: string(k.Algorithm())}); err == nil {
			verified = true
		}
	}
	if !verified {
		t.Error("a token signed by the old key does not verify against the published set")
	}
}
