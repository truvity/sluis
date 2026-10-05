package issuerapp_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/store"
)

// kmsFake holds several keys by id: *ecdsa.PrivateKey (P-384) or *rsa.PrivateKey (3072).
type kmsFake map[string]crypto.Signer

func (f kmsFake) GetPublicKey(_ context.Context, in *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	key := f[aws.ToString(in.KeyId)]
	der, _ := x509.MarshalPKIXPublicKey(key.Public())
	out := &kms.GetPublicKeyOutput{KeyId: in.KeyId, KeyUsage: types.KeyUsageTypeSignVerify, PublicKey: der}
	if _, isRSA := key.(*rsa.PrivateKey); isRSA {
		out.KeySpec = types.KeySpecRsa3072
		out.SigningAlgorithms = []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecRsassaPkcs1V15Sha256}
	} else {
		out.KeySpec = types.KeySpecEccNistP384
		out.SigningAlgorithms = []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecEcdsaSha384}
	}
	return out, nil
}

func (f kmsFake) Sign(_ context.Context, in *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	switch key := f[aws.ToString(in.KeyId)].(type) {
	case *rsa.PrivateKey:
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, in.Message)
		return &kms.SignOutput{Signature: sig}, err
	case *ecdsa.PrivateKey:
		der, err := ecdsa.SignASN1(rand.Reader, key, in.Message)
		return &kms.SignOutput{Signature: der}, err
	}
	return nil, errors.New("unknown key")
}

func kmsConfig(t *testing.T, keys ...string) func(*config.Serve) {
	t.Helper()
	return kmsConfigWith(t, base64.StdEncoding.EncodeToString(randomBytes(32))+"\n", keys...)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func kmsConfigWith(t *testing.T, content string, keys ...string) func(*config.Serve) {
	t.Helper()
	secret := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(secret, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(f *config.Serve) {
		f.SigningKey = &config.SigningKey{KMS: &config.SigningKeyKMS{Keys: keys, StateSecret: asName(secret)}}
	}
}

// On an installation that has seen none of them, only the LAST listed key is
// adopted: an older one is never newly recorded, so it cannot sign.
func TestKMSKeysArePublishedFromTheList(t *testing.T) {
	t.Parallel()
	a, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	b, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	app := bootDeps(t, issuerapp.Deps{Directory: nobody{}, KMS: kmsFake{"alias/a": a, "alias/b": b}},
		kmsConfig(t, "alias/a", "alias/b"))
	code, body := get(t, app.Handler(), "/keys")
	if code != 200 {
		t.Fatalf("/keys = %d", code)
	}
	var jwks struct {
		Keys []struct{ Kid, Alg, Crv string }
	}
	if err := json.Unmarshal([]byte(body), &jwks); err != nil {
		t.Fatal(err)
	}
	if len(jwks.Keys) != 1 || jwks.Keys[0].Alg != "ES384" || jwks.Keys[0].Crv != "P-384" {
		t.Fatalf("keys = %+v", jwks.Keys)
	}
}

func TestKMSAndFileAreExclusive(t *testing.T) {
	t.Parallel()
	f := &config.Serve{IssuerURL: "https://issuer.example",
		SigningKey: &config.SigningKey{File: "/k", KMS: &config.SigningKeyKMS{Keys: []string{"k"}, StateSecret: "s"}}}
	if _, err := issuerapp.FromConfig(withPolicy(t, f)); err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("got %v", err)
	}
}

func TestAPlaceholderStateSecretIsRefused(t *testing.T) {
	t.Parallel()
	a, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	for name, content := range map[string]string{
		"repeated": strings.Repeat("x", 64), "short": "abcd", "few bytes": strings.Repeat("ab", 40),
	} {
		policyDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("version: 1\nlifetimes: { default: 12h }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := issuerapp.FromConfig(withPolicy(t, &config.Serve{IssuerURL: "https://issuer.example",
			Policy: &config.PolicyRef{File: filepath.Join(policyDir, "policy.yaml")},
			Listen: &config.Address{Address: ":0"}, Probes: &config.Address{Address: ":0"},
			SigningKey: &config.SigningKey{KMS: &config.SigningKeyKMS{Keys: []string{"a"},
				StateSecret: asName(writeTemp(t, content))}}}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = issuerapp.New(context.Background(), cfg,
			issuerapp.Deps{Directory: nobody{}, KMS: kmsFake{"a": a}, Stores: &store.Stores{Secrets: testSecrets}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil || !strings.Contains(err.Error(), "stateSecret") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// signingKey.kms.additional adds an RS256 ring beside the ES384 one: both are
// published, each from its own KMS key.
func TestKMSAdditionalRS256IsPublishedBesideES384(t *testing.T) {
	t.Parallel()
	a, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	r, _ := rsa.GenerateKey(rand.Reader, 3072)
	change := kmsConfig(t, "alias/es")
	app := bootDeps(t, issuerapp.Deps{Directory: nobody{}, KMS: kmsFake{"alias/es": a, "alias/rs": r}},
		func(f *config.Serve) {
			change(f)
			f.SigningKey.KMS.Additional = []config.SigningKeyKMSAlg{{Alg: "RS256", Keys: []string{"alias/rs"}}}
		})
	_, body := get(t, app.Handler(), "/keys")
	var jwks struct{ Keys []struct{ Kty, Alg string } }
	if err := json.Unmarshal([]byte(body), &jwks); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, k := range jwks.Keys {
		got[k.Alg] = k.Kty
	}
	if len(jwks.Keys) != 2 || got["ES384"] != "EC" || got["RS256"] != "RSA" {
		t.Fatalf("keys = %+v", jwks.Keys)
	}
}

// An RS256 KMS list beside an RS256 file is the same refusal as two files.
func TestKMSRS256AndAnRS256FileClash(t *testing.T) {
	t.Parallel()
	a, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	r, _ := rsa.GenerateKey(rand.Reader, 3072)
	der, _ := x509.MarshalPKCS8PrivateKey(r)
	file := filepath.Join(t.TempDir(), "rs.pem")
	_ = os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	policyDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("version: 1\nlifetimes: { default: 12h }\n"), 0o600)
	change := kmsConfig(t, "alias/es")
	f := &config.Serve{IssuerURL: "https://issuer.example", Policy: &config.PolicyRef{File: filepath.Join(policyDir, "policy.yaml")},
		Listen: &config.Address{Address: ":0"}, Probes: &config.Address{Address: ":0"}}
	change(f)
	f.SigningKey.AdditionalFiles = []string{file}
	f.SigningKey.KMS.Additional = []config.SigningKeyKMSAlg{{Alg: "RS256", Keys: []string{"alias/rs"}}}
	cfg, err := issuerapp.FromConfig(withPolicy(t, f))
	if err != nil {
		t.Fatal(err)
	}
	_, err = issuerapp.New(context.Background(), cfg, issuerapp.Deps{Directory: nobody{}, Stores: &store.Stores{Secrets: testSecrets},
		KMS: kmsFake{"alias/es": a, "alias/rs": r}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "RS256") {
		t.Fatalf("got %v", err)
	}
}
