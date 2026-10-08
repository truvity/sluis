package transit_test

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/storage/keys"
	"github.com/truvity/sluis/storage/keys/conformance"
	"github.com/truvity/sluis/storage/keys/transit"
	"github.com/truvity/sluis/storage/openbao"
	"github.com/truvity/sluis/storage/openbao/openbaotest"
)

// policy is the one the package documentation gives an estate for a role
// that uses every operation on the keys named.
func policy(mount string, names ...string) string {
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, `
path "%[1]s/keys/%[2]s" { capabilities = ["read"] }
path "%[1]s/encrypt/%[2]s" { capabilities = ["update"] }
path "%[1]s/decrypt/%[2]s" { capabilities = ["update"] }
path "%[1]s/datakey/plaintext/%[2]s" { capabilities = ["update"] }
path "%[1]s/sign/%[2]s" { capabilities = ["update"] }
path "%[1]s/hmac/%[2]s" { capabilities = ["update"] }
`, mount, n)
	}
	return b.String()
}

type fixture struct {
	dev   *openbaotest.Dev
	mount string
}

func (f fixture) key(t *testing.T, name, typ string, derived bool) {
	t.Helper()
	f.dev.MustRoot(t, "POST", f.mount+"/keys/"+name, map[string]any{"type": typ, "derived": derived})
}

func setup(t *testing.T) fixture {
	dev := openbaotest.Env(t)
	return fixture{dev, dev.MountTransit(t)}
}

func TestConformance(t *testing.T) {
	f := setup(t)
	f.key(t, "sym", "aes256-gcm96", false)
	f.key(t, "sym2", "aes256-gcm96", false)
	f.key(t, "sig", "ecdsa-p384", false)
	c := f.dev.Client(t, policy(f.mount, "sym", "sym2", "sig"))
	conformance.Run(t, conformance.Subject{
		Backend:        transit.New(c, transit.WithMount(f.mount)),
		Symmetric:      "sym",
		OtherSymmetric: "sym2",
		Signing:        "sig",
		NoDestroy:      true, // see the package documentation, "Erasing a tenant"
	})
}

// A derived key takes the context as its derivation; the same suite passes.
func TestConformanceDerived(t *testing.T) {
	f := setup(t)
	f.key(t, "sym", "aes256-gcm96", true)
	f.key(t, "sym2", "chacha20-poly1305", true)
	f.key(t, "sig", "ecdsa-p384", false)
	c := f.dev.Client(t, policy(f.mount, "sym", "sym2", "sig"))
	b := transit.New(c, transit.WithMount(f.mount))
	// "context off" needs a key that takes none: the derived key refuses.
	ctx := t.Context()
	if _, err := b.Encrypt(ctx, "sym", []byte("x"), nil); err == nil {
		t.Fatal("a derived key encrypted without a context")
	}
	ct, err := b.Encrypt(ctx, "sym", []byte("x"), map[string]string{"purpose": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Decrypt(ctx, "sym", ct, map[string]string{"purpose": "b"}); !errors.Is(err, keys.ErrDecrypt) {
		t.Fatalf("another context: %v, want ErrDecrypt", err)
	}
	if got, err := b.Decrypt(ctx, "sym", ct, map[string]string{"purpose": "a"}); err != nil || string(got) != "x" {
		t.Fatalf("%q, %v", got, err)
	}
	pt, wrapped, err := b.GenerateDataKey(ctx, "sym2", map[string]string{"purpose": "a"})
	if err != nil || len(pt) != 32 {
		t.Fatalf("data key: %v", err)
	}
	if got, err := b.Decrypt(ctx, "sym2", wrapped, map[string]string{"purpose": "a"}); err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("unwrap: %v", err)
	}
}

// Transit ignores a context on a key that is not derived: the backend must
// refuse rather than send a binding that binds nothing.
func TestContextThatWouldBeIgnoredIsRefused(t *testing.T) {
	f := setup(t)
	f.key(t, "plain", "aes256-gcm96", false)
	f.key(t, "derived", "aes256-gcm96", true)
	f.key(t, "rsa", "rsa-2048", false)
	c := f.dev.Client(t, policy(f.mount, "plain", "derived", "rsa"))
	ec := map[string]string{"purpose": "a"}
	ctx := t.Context()

	forced := transit.New(c, transit.WithMount(f.mount), transit.WithBinding(transit.BindDerivation))
	if _, err := forced.Encrypt(ctx, "plain", []byte("x"), ec); !errors.Is(err, keys.ErrUnsupported) {
		t.Fatalf("derivation on a key that is not derived: %v, want ErrUnsupported", err)
	}
	aad := transit.New(c, transit.WithMount(f.mount), transit.WithBinding(transit.BindAssociatedData))
	if _, err := aad.Encrypt(ctx, "derived", []byte("x"), ec); !errors.Is(err, keys.ErrUnsupported) {
		t.Fatalf("associated data on a derived key: %v, want ErrUnsupported", err)
	}
	auto := transit.New(c, transit.WithMount(f.mount))
	if _, err := auto.Encrypt(ctx, "rsa", []byte("x"), ec); !errors.Is(err, keys.ErrUnsupported) {
		t.Fatalf("a context on an RSA key: %v, want ErrUnsupported", err)
	}
	// Without a context an RSA key encrypts, since nothing is bound.
	ct, err := auto.Encrypt(ctx, "rsa", []byte("x"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := auto.Decrypt(ctx, "rsa", ct, nil); err != nil || string(got) != "x" {
		t.Fatalf("%q, %v", got, err)
	}
}

func TestDecryptRefusesWhatIsNotTransit(t *testing.T) {
	f := setup(t)
	f.key(t, "sym", "aes256-gcm96", false)
	b := transit.New(f.dev.Client(t, policy(f.mount, "sym")), transit.WithMount(f.mount))
	for _, ct := range []string{"", "AAAA", "vault:v1:AAAA", "vault:v9:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if _, err := b.Decrypt(t.Context(), "sym", []byte(ct), nil); !errors.Is(err, keys.ErrDecrypt) {
			t.Errorf("Decrypt(%q) = %v, want ErrDecrypt", ct, err)
		}
	}
	// A key that does not exist is not "damaged ciphertext".
	if _, err := b.Decrypt(t.Context(), "nope", []byte("vault:v1:AAAA"), nil); err == nil || errors.Is(err, keys.ErrDecrypt) {
		t.Errorf("a missing key: %v, want an error that is not ErrDecrypt", err)
	}
}

func TestRSASignsRS256(t *testing.T) {
	f := setup(t)
	f.key(t, "rsa", "rsa-3072", false)
	f.key(t, "sym", "aes256-gcm96", false)
	b := transit.New(f.dev.Client(t, policy(f.mount, "rsa", "sym")), transit.WithMount(f.mount))
	ctx := t.Context()
	pub, alg, err := b.PublicKey(ctx, "rsa")
	if err != nil || alg != "RS256" {
		t.Fatalf("%s, %v", alg, err)
	}
	if _, err := b.Sign(ctx, "rsa", make([]byte, 48)); err == nil {
		t.Fatal("a SHA-384 digest was accepted for RS256")
	}
	digest := sha256.Sum256([]byte("message"))
	sig, err := b.Sign(ctx, "rsa", digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(pub.(*rsa.PublicKey), crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("the signature is not PKCS#1 v1.5: %v", err)
	}
	if _, _, err := b.PublicKey(ctx, "sym"); !errors.Is(err, keys.ErrUnsupported) {
		t.Fatalf("a symmetric key has a public key: %v", err)
	}
}

// Signatures are pinned to the version read with the public key, and move to
// a rotated key after the information expires.
func TestSignPinsTheVersion(t *testing.T) {
	f := setup(t)
	f.key(t, "sig", "ecdsa-p384", false)
	c := f.dev.Client(t, policy(f.mount, "sig"))
	long := transit.New(c, transit.WithMount(f.mount), transit.WithInfoTTL(time.Hour))
	short := transit.New(c, transit.WithMount(f.mount), transit.WithInfoTTL(time.Nanosecond))
	ctx := t.Context()

	v1, err := long.KeyVersion(ctx, "sig")
	if err != nil || v1 != 1 {
		t.Fatalf("version %d, %v", v1, err)
	}
	f.dev.MustRoot(t, "POST", f.mount+"/keys/sig/rotate", nil)

	digest := sha512.Sum384([]byte("m"))
	sig, err := long.Sign(ctx, "sig", digest[:])
	if err != nil {
		t.Fatal(err)
	}
	pub1, _, _ := long.PublicKey(ctx, "sig")
	if !ecdsa.VerifyASN1(pub1.(*ecdsa.PublicKey), digest[:], sig) {
		t.Fatal("after a rotation the pinned signature no longer matches the pinned public key")
	}
	if v, _ := long.KeyVersion(ctx, "sig"); v != 1 {
		t.Fatalf("the long-lived backend moved to version %d", v)
	}

	if v, err := short.KeyVersion(ctx, "sig"); err != nil || v != 2 {
		t.Fatalf("a fresh read: version %d, %v; want 2", v, err)
	}
	sig2, err := short.Sign(ctx, "sig", digest[:])
	if err != nil {
		t.Fatal(err)
	}
	pub2, _, _ := short.PublicKey(ctx, "sig")
	if !ecdsa.VerifyASN1(pub2.(*ecdsa.PublicKey), digest[:], sig2) || ecdsa.VerifyASN1(pub1.(*ecdsa.PublicKey), digest[:], sig2) {
		t.Fatal("the new version's signature does not match the new public key alone")
	}
}

func TestMACIsStableAndRotationProof(t *testing.T) {
	f := setup(t)
	f.key(t, "mac", "aes256-gcm96", false)
	c := f.dev.Client(t, policy(f.mount, "mac"))
	b := transit.New(c, transit.WithMount(f.mount))
	ctx := t.Context()
	m1, err := b.MAC(ctx, "mac", keys.Pseudonym, "tenant-1", []byte("alice"))
	if err != nil || len(m1) != 32 {
		t.Fatalf("%d bytes, %v", len(m1), err)
	}
	f.dev.MustRoot(t, "POST", f.mount+"/keys/mac/rotate", nil)
	m2, err := b.MAC(ctx, "mac", keys.Pseudonym, "tenant-1", []byte("alice"))
	if err != nil || !bytes.Equal(m1, m2) {
		t.Fatalf("a rotation changed a pseudonym: %x vs %x, %v", m1, m2, err)
	}
	// ("ab", "c") and ("a", "bc") must not collide.
	a, _ := b.MAC(ctx, "mac", keys.Pseudonym, "ab", []byte("c"))
	z, _ := b.MAC(ctx, "mac", keys.Pseudonym, "a", []byte("bc"))
	if bytes.Equal(a, z) {
		t.Fatal("(tenant ab, data c) collides with (tenant a, data bc)")
	}
	// A pinned version that does not exist is an error, not a silent other key.
	if _, err := transit.New(c, transit.WithMount(f.mount), transit.WithMACKeyVersion(7)).MAC(ctx, "mac", keys.Pseudonym, "t", nil); err == nil {
		t.Fatal("MAC under a version that does not exist")
	}
}

// What the package documentation says an ACL can do: pin the context a role
// may send, and require it be sent.
func TestPolicyPinsTheContext(t *testing.T) {
	f := setup(t)
	f.key(t, "sym", "aes256-gcm96", false)
	f.key(t, "der", "aes256-gcm96", true)
	ec := map[string]string{"instance": "prod", "purpose": "conceal"}
	canon := base64.StdEncoding.EncodeToString([]byte(`{"instance":"prod","purpose":"conceal"}`))
	pol := fmt.Sprintf(`
path "%[1]s/keys/sym" { capabilities = ["read"] }
path "%[1]s/keys/der" { capabilities = ["read"] }
path "%[1]s/encrypt/sym" {
  capabilities = ["update"]
  required_parameters = ["associated_data"]
  allowed_parameters = { "plaintext" = [], "associated_data" = ["%[2]s"] }
}
path "%[1]s/decrypt/sym" {
  capabilities = ["update"]
  required_parameters = ["associated_data"]
  allowed_parameters = { "ciphertext" = [], "associated_data" = ["%[2]s"] }
}
path "%[1]s/encrypt/der" {
  capabilities = ["update"]
  required_parameters = ["context"]
  allowed_parameters = { "plaintext" = [], "context" = ["%[2]s"] }
}
`, f.mount, canon)
	b := transit.New(f.dev.Client(t, pol), transit.WithMount(f.mount))
	ctx := t.Context()

	for _, key := range []string{"sym", "der"} {
		if _, err := b.Encrypt(ctx, key, []byte("x"), ec); err != nil {
			t.Fatalf("%s with the pinned context: %v", key, err)
		}
		if _, err := b.Encrypt(ctx, key, []byte("x"), map[string]string{"instance": "stage", "purpose": "conceal"}); openbao.Status(err) != 403 {
			t.Fatalf("%s with another context: %v, want 403", key, err)
		}
	}
	// The parameter left out: required_parameters makes the server refuse
	// what allowed_parameters alone would let through.
	if _, err := b.Encrypt(ctx, "sym", []byte("x"), nil); openbao.Status(err) != 403 {
		t.Fatalf("sym without a context: %v, want 403", err)
	}
	ct, err := b.Encrypt(ctx, "sym", []byte("x"), ec)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b.Decrypt(ctx, "sym", ct, ec); err != nil || string(got) != "x" {
		t.Fatalf("%q, %v", got, err)
	}
}

func TestValidateName(t *testing.T) {
	b := transit.New(nil)
	for name, want := range map[string]string{
		"alias/audit":  "no aliases",
		"team/audit":   "no '/'",
		"has space":    "letters",
		"":             "letters",
		"-lead":        "letters",
		"sluis-seal.1": "",
		"audit_data":   "",
	} {
		err := b.ValidateName(name)
		switch {
		case want == "" && err != nil, want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: %v", name, err)
		}
	}
}
