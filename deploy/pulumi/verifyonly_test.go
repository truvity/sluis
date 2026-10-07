package sluispulumi_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// publicPEM is a public key's PEM (`PUBLIC KEY`).
func publicPEM(t *testing.T, pub any) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// oldKeys are an earlier signer's two keys, ES384 and RS256, and their PEMs.
func oldKeys(t *testing.T) (*ecdsa.PrivateKey, *rsa.PrivateKey, string, string) {
	t.Helper()
	ec, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return ec, rs, publicPEM(t, &ec.PublicKey), publicPEM(t, &rs.PublicKey)
}

// verifyOnlyOf is `signingKey.verifyOnly` of a rendered service document.
func verifyOnlyOf(t *testing.T, doc string) any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	signing, _ := m["signingKey"].(map[string]any)
	return signing["verifyOnly"]
}

// The golden: what the serve document says for the two keys of oldEstate, as
// the Kubernetes shape says it (a file each, the kid and alg when given, the
// until in RFC 3339), with the files where the layer holds them.
const verifyOnlyGolden = `
- alg: ES384
  file: /opt/sluis/verify-keys/0.pem
  kid: old-es384
  until: "2026-11-05T00:00:00Z"
- file: /opt/sluis/verify-keys/1.pem
  until: "2026-11-05T12:00:00Z"
`

func golden(t *testing.T) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal([]byte(verifyOnlyGolden), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func verifyOnlyArgs(ecPEM, rsPEM string) []arp.VerifyOnlyKeyArgs {
	return []arp.VerifyOnlyKeyArgs{
		{PEM: ecPEM, KeyID: "old-es384", Alg: "ES384", Until: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)},
		// A zone other than UTC is written in UTC.
		{PEM: "\n" + rsPEM + "\n\n", Until: time.Date(2026, 11, 5, 14, 0, 0, 0, time.FixedZone("CEST", 2*3600))},
	}
}

// The layer holds each verify-only key, one file each, and the serve document
// names them exactly as the Kubernetes shape does, whether the document comes
// from Config or from an Installation.
func TestVerifyOnlyKeysAreInTheLayerAndTheServeDocumentNamesThem(t *testing.T) {
	_, _, ecPEM, rsPEM := oldKeys(t)
	for name, e := range map[string]estate{
		"config": {mutate: func(a *arp.LambdaArgs) { a.VerifyOnly = verifyOnlyArgs(ecPEM, rsPEM) }},
		"installation": withInstallation(exampleInstallation(t), func(a *arp.LambdaArgs) {
			a.VerifyOnly = verifyOnlyArgs(ecPEM, rsPEM)
		}),
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := mustLambda(t, e)
			files := layerFiles(t, rec)
			if files["sluis/verify-keys/0.pem"] != ecPEM || files["sluis/verify-keys/1.pem"] != rsPEM {
				t.Errorf("the layer's keys are not the PEMs given:\n%s\n%s", files["sluis/verify-keys/0.pem"], files["sluis/verify-keys/1.pem"])
			}
			for path, body := range files {
				if strings.Contains(strings.ToUpper(body), "PRIVATE") {
					t.Errorf("%s holds a private key", path)
				}
			}
			if n := len(files); n != 4 {
				t.Errorf("the layer holds %d files, want the two documents and the two keys: %v", n, keysOf(files))
			}
			if got, want := verifyOnlyOf(t, files["sluis/sluis.yaml"]), golden(t); !reflect.DeepEqual(got, want) {
				t.Errorf("signingKey.verifyOnly:\n%v\n--- want ---\n%v", got, want)
			}
			if arp.VerifyOnlyKeyPath(1) != "/opt/sluis/verify-keys/1.pem" {
				t.Errorf("VerifyOnlyKeyPath(1) is %s", arp.VerifyOnlyKeyPath(1))
			}
		})
	}

	// Without keys the layer is the two documents and the document says nothing.
	rec, _ := mustLambda(t, estate{})
	files := layerFiles(t, rec)
	if len(files) != 2 || verifyOnlyOf(t, files["sluis/sluis.yaml"]) != nil {
		t.Errorf("a stack without verify-only keys changed: %v", keysOf(files))
	}
}

// What the function would refuse at start is refused before anything is
// published, and the error never carries the key.
func TestAVerifyOnlyKeyThatIsNotOnePublicKeyIsRefused(t *testing.T) {
	ec, rs, ecPEM, rsPEM := oldKeys(t)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	privatePEMs := map[string]string{
		"a PKCS#8 private key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		"an EC private key":    string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER})),
		"an RSA private key": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(rs)})),
		"a public key beside a private one": ecPEM + string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
	}
	until := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		keys []arp.VerifyOnlyKeyArgs
		want string
	}{
		"no until":         {[]arp.VerifyOnlyKeyArgs{{PEM: ecPEM}}, "Until is required"},
		"no PEM":           {[]arp.VerifyOnlyKeyArgs{{Until: until}}, "PEM is required"},
		"not a PEM":        {[]arp.VerifyOnlyKeyArgs{{PEM: "ssh-ed25519 AAAA", Until: until}}, "not a PEM"},
		"two keys in one":  {[]arp.VerifyOnlyKeyArgs{{PEM: ecPEM + rsPEM, Until: until}}, "several keys"},
		"the wrong alg":    {[]arp.VerifyOnlyKeyArgs{{PEM: ecPEM, Alg: "RS256", Until: until}}, "the key is for ES384"},
		"the same key":     {[]arp.VerifyOnlyKeyArgs{{PEM: ecPEM, Until: until}, {PEM: ecPEM, Until: until}}, "VerifyOnly[0]"},
		"the same kid":     {[]arp.VerifyOnlyKeyArgs{{PEM: ecPEM, KeyID: "k", Until: until}, {PEM: rsPEM, KeyID: "k", Until: until}}, "VerifyOnly[0]"},
		"another PEM type": {[]arp.VerifyOnlyKeyArgs{{PEM: "-----BEGIN OPENSSH KEY-----\nAAAA\n-----END OPENSSH KEY-----\n", Until: until}}, "OPENSSH KEY block"},
	}
	for name, pemText := range privatePEMs {
		cases[name] = struct {
			keys []arp.VerifyOnlyKeyArgs
			want string
		}{[]arp.VerifyOnlyKeyArgs{{PEM: pemText, Until: until}}, "private key"}
	}
	for name, c := range cases {
		_, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.VerifyOnly = c.keys }})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), strings.Split(ecPEM, "\n")[1]) {
			t.Errorf("%s: the error carries key material: %v", name, err)
		}
	}
}

// signingKey.verifyOnly is the library's: a document or an installation that
// names files of its own is refused, since those files are not in the layer.
func TestVerifyOnlyIsSaidOnlyThroughTheArguments(t *testing.T) {
	_, _, ecPEM, rsPEM := oldKeys(t)
	named := "issuerURL: https://access.example.test\n" +
		"signingKey: {verifyOnly: [{file: /var/run/access-issuer/verify-keys/0.pem, until: \"2026-11-05T00:00:00Z\"}]}\n"
	if _, _, err := buildLambda(t, estate{config: named}); err == nil || !strings.Contains(err.Error(), "LambdaArgs.VerifyOnly") {
		t.Errorf("a document naming verify-only files: %v", err)
	}
	if _, _, err := buildLambda(t, estate{config: named, mutate: func(a *arp.LambdaArgs) {
		a.VerifyOnly = verifyOnlyArgs(ecPEM, rsPEM)
	}}); err == nil || !strings.Contains(err.Error(), "leave it out") {
		t.Errorf("a document naming other verify-only files beside the arguments: %v", err)
	}
	in := exampleInstallation(t)
	k := *in.SigningKey
	if err := yaml.Unmarshal([]byte(`[{file: /var/run/access-issuer/verify-keys/0.pem, until: "2026-11-05T00:00:00Z"}]`), &k.VerifyOnly); err != nil {
		t.Fatal(err)
	}
	in.SigningKey = &k
	if _, _, err := buildLambda(t, withInstallation(in, nil)); err == nil || !strings.Contains(err.Error(), "LambdaArgs.VerifyOnly") {
		t.Errorf("an installation naming verify-only files: %v", err)
	}
}
