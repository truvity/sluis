package issuerapp_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
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
)

// kmsFake holds several P-384 keys by id.
type kmsFake map[string]*ecdsa.PrivateKey

func (f kmsFake) GetPublicKey(_ context.Context, in *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	key := f[aws.ToString(in.KeyId)]
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	return &kms.GetPublicKeyOutput{
		KeyId: in.KeyId, KeySpec: types.KeySpecEccNistP384, KeyUsage: types.KeyUsageTypeSignVerify, PublicKey: der,
		SigningAlgorithms: []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecEcdsaSha384},
	}, nil
}

func (f kmsFake) Sign(_ context.Context, in *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	der, err := ecdsa.SignASN1(rand.Reader, f[aws.ToString(in.KeyId)], in.Message)
	return &kms.SignOutput{Signature: der}, err
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
		f.SigningKey = &config.SigningKey{KMS: &config.SigningKeyKMS{Keys: keys, StateSecretFile: secret}}
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
		SigningKey: &config.SigningKey{File: "/k", KMS: &config.SigningKeyKMS{Keys: []string{"k"}, StateSecretFile: "/s"}}}
	if _, err := issuerapp.FromConfig(f); err == nil || !strings.Contains(err.Error(), "exclusive") {
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
		cfg, err := issuerapp.FromConfig(&config.Serve{IssuerURL: "https://issuer.example", PolicyDir: policyDir,
			Listen: &config.Address{Address: ":0"}, Probes: &config.Address{Address: ":0"},
			SigningKey: &config.SigningKey{KMS: &config.SigningKeyKMS{Keys: []string{"a"},
				StateSecretFile: writeTemp(t, content)}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = issuerapp.New(context.Background(), cfg,
			issuerapp.Deps{Directory: nobody{}, KMS: kmsFake{"a": a}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil || !strings.Contains(err.Error(), "stateSecretFile") {
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
