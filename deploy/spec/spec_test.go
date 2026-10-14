package spec_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/deploy/spec"
	"github.com/truvity/sluis/internal/issuer"
)

func publicPEM(t *testing.T, key any) string {
	t.Helper()

	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func TestNames(t *testing.T) {
	const account = "111122223333"

	if got := spec.BucketName("blobs", account, "eu-west-1"); got != "blobs-111122223333-eu-west-1" {
		t.Errorf("BucketName = %q", got)
	}

	if got := spec.RoleARN(account, "/audit/", "main-query"); got != "arn:aws:iam::111122223333:role/audit/main-query" {
		t.Errorf("RoleARN = %q", got)
	}

	if got := spec.RoleARN(account, "", "x"); got != "arn:aws:iam::111122223333:role/x" {
		t.Errorf("RoleARN without a path = %q", got)
	}

	if got := spec.FunctionRoleARN(account, "sluis-main-backup"); got != "arn:aws:iam::111122223333:role/sluis-main-backup" {
		t.Errorf("FunctionRoleARN = %q", got)
	}

	if got := spec.KubernetesRoleName("acme"); got != "acme-sluis" {
		t.Errorf("KubernetesRoleName = %q", got)
	}
}

func TestChecks(t *testing.T) {
	for _, ok := range []string{"main", "a", "eu-1"} {
		if err := spec.CheckInstance(ok); err != nil {
			t.Errorf("CheckInstance(%q): %v", ok, err)
		}
	}

	for _, bad := range []string{"", "Main", "-main", "main-", "private", "export", strings.Repeat("a", 33)} {
		if err := spec.CheckInstance(bad); err == nil {
			t.Errorf("CheckInstance(%q) accepted", bad)
		}
	}

	if err := spec.CheckFunctionName("sluis_main-1"); err != nil {
		t.Errorf("CheckFunctionName: %v", err)
	}

	if err := spec.CheckFunctionName("a.b"); err == nil {
		t.Error("CheckFunctionName accepted a dot")
	}

	if err := spec.CheckKeyAlias("alias/main-sign"); err != nil {
		t.Errorf("CheckKeyAlias: %v", err)
	}

	for _, bad := range []string{"alias/aws/ssm", "arn:aws:kms:eu-west-1:111122223333:alias/x", "main", "alias/"} {
		if err := spec.CheckKeyAlias(bad); err == nil {
			t.Errorf("CheckKeyAlias(%q) accepted", bad)
		}
	}

	if err := spec.CheckBucketPrefix("blobs", "eu-central-1"); err != nil {
		t.Errorf("CheckBucketPrefix: %v", err)
	}

	if err := spec.CheckBucketPrefix(strings.Repeat("b", 40), "ap-southeast-1"); err == nil || !strings.Contains(err.Error(), "63 at most") {
		t.Errorf("err = %v, want a name too long refused", err)
	}

	if err := spec.CheckBucketPrefix("Blobs", "eu-central-1"); err == nil {
		t.Error("CheckBucketPrefix accepted upper case")
	}

	if err := spec.CheckCloudflareRotation("1h", "45m"); err != nil {
		t.Errorf("CheckCloudflareRotation: %v", err)
	}

	for _, pair := range [][2]string{{"1h", "1h"}, {"25h", "1h"}, {"30s", "10s"}, {"1h", "x"}} {
		if err := spec.CheckCloudflareRotation(pair[0], pair[1]); err == nil {
			t.Errorf("CheckCloudflareRotation(%q, %q) accepted", pair[0], pair[1])
		}
	}
}

func TestCheckVerifyKey(t *testing.T) {
	keys := verifyKeys(t)

	for _, c := range []struct{ name, pem, alg string }{
		{"P-384 for ES384", keys.p384, spec.AlgES384},
		{"RSA for RS256", keys.rsa2048, spec.AlgRS256},
		{"no algorithm: the key's own", keys.p384, ""},
	} {
		if err := spec.CheckVerifyKey(c.pem, c.alg); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}

	for want, c := range map[string]struct{ pem, alg string }{
		"this one is on P-256": {keys.p256, "ES256"},
		"at least 2048":        {keys.rsa1024, spec.AlgRS256},
		"this one is on P-521": {keys.p521, "ES512"},
		"alg says ES256":       {keys.p384, "ES256"},
		"alg says RS256":       {keys.p384, spec.AlgRS256},
		"PUBLIC key":           {keys.private, spec.AlgES384},
		"holds several":        {keys.p384 + keys.p384, spec.AlgES384},
		"not a JWK or a PEM":   {"nonsense", ""},
	} {
		err := spec.CheckVerifyKey(c.pem, c.alg)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want one saying %q", err, want)
		}
	}
}

// TestCheckVerifyKeyAgreesWithTheIssuer feeds the same inputs to the deploy
// check and to the issuer's own parser and holds their accept and refuse
// equal: a key the check passes must load in the running issuer, and one the
// issuer refuses must be refused before the apply.
func TestCheckVerifyKeyAgreesWithTheIssuer(t *testing.T) {
	keys := verifyKeys(t)
	for name, c := range map[string]struct{ pem, alg string }{
		"p384":             {keys.p384, ""},
		"p384 es384":       {keys.p384, "ES384"},
		"p384 rs256":       {keys.p384, "RS256"},
		"p256":             {keys.p256, "ES256"},
		"p256 es384":       {keys.p256, "ES384"},
		"p521":             {keys.p521, "ES512"},
		"rsa 2048":         {keys.rsa2048, "RS256"},
		"rsa 2048 es384":   {keys.rsa2048, "ES384"},
		"rsa 1024":         {keys.rsa1024, "RS256"},
		"private":          {keys.private, ""},
		"two blocks":       {keys.p384 + keys.p384, ""},
		"unknown alg":      {keys.p384, "HS256"},
		"empty":            {"", ""},
		"garbage":          {"-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n", ""},
		"a JWK":            {`{"kty":"EC","crv":"P-384","x":"","y":""}`, ""},
		"trailing newline": {keys.p384 + "\n\n", "ES384"},
	} {
		_, issuerErr := issuer.ParseVerifyOnlyKey([]byte(c.pem), "", c.alg, time.Time{})
		if got := spec.CheckVerifyKey(c.pem, c.alg) == nil; got != (issuerErr == nil) {
			t.Errorf("%s: deploy/spec accepts %v, the issuer %v (%v)", name, got, issuerErr == nil, issuerErr)
		}
	}
}

type verifyKeySet struct{ p384, p256, p521, rsa2048, rsa1024, private string }

func verifyKeys(t *testing.T) verifyKeySet {
	t.Helper()

	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	p521, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	rsa2048, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	rsa1024, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // the test needs a short key
	if err != nil {
		t.Fatal(err)
	}

	return verifyKeySet{
		p384: publicPEM(t, &p384.PublicKey), p256: publicPEM(t, &p256.PublicKey), p521: publicPEM(t, &p521.PublicKey),
		rsa2048: publicPEM(t, &rsa2048.PublicKey), rsa1024: publicPEM(t, &rsa1024.PublicKey),
		private: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("x")})),
	}
}

func TestChartSecretIsTheChartsShape(t *testing.T) {
	raw, err := os.ReadFile("../../charts/sluis/values.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// The chart documents the shape in a comment above `secrets: []`.
	for _, key := range []string{"- name: valkey/password", "secretName: valkey", "key: password"} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("the chart's values no longer document %q", key)
		}
	}

	var entries []spec.ChartSecret
	if err := yaml.Unmarshal([]byte("- name: valkey/password\n  secretName: valkey\n  key: password\n"), &entries); err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 || entries[0] != (spec.ChartSecret{Name: "valkey/password", SecretName: "valkey", Key: "password"}) {
		t.Errorf("entries = %+v", entries)
	}
}
