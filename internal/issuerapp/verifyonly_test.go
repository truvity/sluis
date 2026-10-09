package issuerapp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
)

func TestVerifyOnlyKeysLoadAtStartAndAPrivateKeyStopsIt(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	privDER, _ := x509.MarshalPKCS8PrivateKey(key)
	dir := t.TempDir()
	write := func(name string, typ string, der []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pub, priv := write("pub.pem", "PUBLIC KEY", pubDER), write("priv.pem", "PRIVATE KEY", privDER)

	got, err := loadVerifyOnly([]config.SigningKeyVerifyOnly{{File: pub, Until: "2026-12-01T00:00:00Z"}}, log)
	if err != nil || len(got) != 1 || got[0].Alg != "ES384" || got[0].ID == "" {
		t.Fatalf("a public key: %+v %v", got, err)
	}
	for name, e := range map[string]config.SigningKeyVerifyOnly{
		"private":   {File: priv, Until: "2026-12-01T00:00:00Z"},
		"missing":   {File: filepath.Join(dir, "none.pem"), Until: "2026-12-01T00:00:00Z"},
		"no until":  {File: pub},
		"bad until": {File: pub, Until: "tomorrow"},
		"wrong alg": {File: pub, Alg: "RS256", Until: "2026-12-01T00:00:00Z"},
	} {
		if _, err := loadVerifyOnly([]config.SigningKeyVerifyOnly{e}, log); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, err = loadVerifyOnly([]config.SigningKeyVerifyOnly{{File: priv, Until: "2026-12-01T00:00:00Z"}}, log)
	if err == nil || !strings.Contains(err.Error(), "signingKey.verifyOnly[0]") || strings.Contains(err.Error(), "BEGIN") {
		t.Errorf("the refusal: %v", err)
	}
	twice := config.SigningKeyVerifyOnly{File: pub, Until: "2026-12-01T00:00:00Z"}
	if _, err = loadVerifyOnly([]config.SigningKeyVerifyOnly{twice, twice}, log); err == nil {
		t.Error("the same kid twice was accepted")
	}
}
