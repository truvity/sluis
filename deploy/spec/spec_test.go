package spec_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/truvity/sluis/deploy/spec"
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
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	rsa2048, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	rsa1024, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // the test needs a key too short
	if err != nil {
		t.Fatal(err)
	}

	if err := spec.CheckVerifyKey(publicPEM(t, &p384.PublicKey), spec.AlgES384); err != nil {
		t.Errorf("P-384 for ES384: %v", err)
	}

	if err := spec.CheckVerifyKey(publicPEM(t, &rsa2048.PublicKey), spec.AlgRS256); err != nil {
		t.Errorf("RSA 2048 for RS256: %v", err)
	}

	private := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("x")}))

	for want, c := range map[string]struct{ pem, alg string }{
		"curve P-384":           {publicPEM(t, &p256.PublicKey), spec.AlgES384},
		"not an ECDSA one":      {publicPEM(t, &p384.PublicKey), spec.AlgRS256},
		"not an RSA one":        {publicPEM(t, &rsa2048.PublicKey), spec.AlgES384},
		"at least 2048":         {publicPEM(t, &rsa1024.PublicKey), spec.AlgRS256},
		"a PUBLIC key":          {private, spec.AlgES384},
		"nothing after it":      {publicPEM(t, &p384.PublicKey) + publicPEM(t, &p384.PublicKey), spec.AlgES384},
		"is not ES384 or RS256": {publicPEM(t, &p384.PublicKey), "ES256"},
	} {
		err := spec.CheckVerifyKey(c.pem, c.alg)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want one saying %q", err, want)
		}
	}
}
