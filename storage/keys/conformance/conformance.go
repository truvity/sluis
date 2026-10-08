// Package conformance is the suite every keys.Backend passes. A backend's
// test builds a Subject and calls Run:
//
//	func TestConformance(t *testing.T) {
//		conformance.Run(t, conformance.Subject{Backend: b, Symmetric: "k1", OtherSymmetric: "k2", Signing: "s1"})
//	}
package conformance

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha512"
	"errors"
	"testing"

	"github.com/truvity/sluis/storage/keys"
)

// Subject is a backend and the key names to exercise it with, all valid
// for Backend.ValidateName and present in the backend.
type Subject struct {
	Backend        keys.Backend
	Symmetric      string // a symmetric key
	OtherSymmetric string // a second, different symmetric key
	Signing        string // an ECC P-384 signing key
	// NoMAC skips the MAC tests for a backend without keys.MACBackend.
	NoMAC bool
	// NoDestroy is for a backend that cannot erase a tenant: the suite then
	// checks that Destroy says keys.ErrUnsupported and changes nothing,
	// instead of the erasure tests.
	NoDestroy bool
}

// Run runs the suite.
func Run(t *testing.T, s Subject) {
	t.Helper()
	cfg := func() keys.Config {
		return keys.Config{Adapter: s.Backend.Name(), Keys: map[keys.Purpose]keys.Entry{
			keys.Conceal:   {Key: s.Symmetric},
			keys.Archive:   {Key: s.OtherSymmetric},
			keys.Seal:      {Key: s.Symmetric, Context: keys.ContextSpec{Mode: keys.ContextOff}},
			keys.Sign:      {Key: s.Signing},
			keys.Pseudonym: {Key: s.Symmetric},
		}}
	}
	open := func(t *testing.T, instance string) *keys.Keys {
		t.Helper()
		ks, err := keys.Open(cfg(), keys.Options{Backend: s.Backend, Instance: instance})
		if err != nil {
			t.Fatal(err)
		}
		return ks
	}
	ctx := context.Background()
	must := func(t *testing.T, ks *keys.Keys, p keys.Purpose) *keys.Key {
		t.Helper()
		k, err := ks.For(p)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}

	t.Run("round trip", func(t *testing.T) {
		k := must(t, open(t, "inst-a"), keys.Conceal)
		for _, pt := range [][]byte{[]byte("hello"), {}, bytes.Repeat([]byte{7}, 4000)} {
			ct, err := k.Encrypt(ctx, pt)
			if err != nil {
				t.Fatal(err)
			}
			if len(pt) > 0 && bytes.Contains(ct, pt) {
				t.Fatal("ciphertext contains the plaintext")
			}
			got, err := k.Decrypt(ctx, ct)
			if err != nil || !bytes.Equal(got, pt) {
				t.Fatalf("got %q, %v", got, err)
			}
		}
	})

	t.Run("context binds the instance", func(t *testing.T) {
		a := must(t, open(t, "inst-a"), keys.Conceal)
		b := must(t, open(t, "inst-b"), keys.Conceal)
		ct, _ := a.Encrypt(ctx, []byte("x"))
		if _, err := b.Decrypt(ctx, ct); err == nil {
			t.Fatal("another instance decrypted it")
		}
	})

	t.Run("context binds the purpose", func(t *testing.T) {
		ks := open(t, "inst-a")
		// Same key, two purposes: conceal has the default context, and so
		// does pseudonym, but the purpose differs.
		c, p := must(t, ks, keys.Conceal), must(t, ks, keys.Pseudonym)
		ct, _ := c.Encrypt(ctx, []byte("x"))
		if _, err := p.Decrypt(ctx, ct); err == nil {
			t.Fatal("a purpose decrypted another purpose's ciphertext under a shared key")
		}
	})

	t.Run("context off sends none", func(t *testing.T) {
		ks := open(t, "inst-a")
		seal := must(t, ks, keys.Seal)
		if seal.Context() != nil {
			t.Fatalf("context off has context %v", seal.Context())
		}
		ct, _ := seal.Encrypt(ctx, []byte("x"))
		if _, err := s.Backend.Decrypt(ctx, s.Symmetric, ct, nil); err != nil {
			t.Fatalf("an unconditioned decrypt failed: %v", err)
		}
		if _, err := s.Backend.Decrypt(ctx, s.Symmetric, ct, map[string]string{"purpose": "seal"}); err == nil {
			t.Fatal("a context was accepted for a ciphertext made without one")
		}
	})

	t.Run("decrypt context override", func(t *testing.T) {
		k := must(t, open(t, "inst-a"), keys.Conceal)
		legacy := map[string]string{"purpose": "legacy-ring", "alg": "ES384", "kid": "k1"}
		ct, err := s.Backend.Encrypt(ctx, s.Symmetric, []byte("old"), legacy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Decrypt(ctx, ct); err == nil {
			t.Fatal("decrypted a legacy ciphertext without the override")
		}
		got, err := k.Decrypt(ctx, ct, keys.WithContext(legacy))
		if err != nil || string(got) != "old" {
			t.Fatalf("override: %q, %v", got, err)
		}
		bare, _ := s.Backend.Encrypt(ctx, s.Symmetric, []byte("bare"), nil)
		if got, err := k.Decrypt(ctx, bare, keys.WithoutContext()); err != nil || string(got) != "bare" {
			t.Fatalf("without context: %q, %v", got, err)
		}
	})

	t.Run("wrong key and damage fail", func(t *testing.T) {
		ks := open(t, "inst-a")
		c := must(t, ks, keys.Conceal)
		ct, _ := c.Encrypt(ctx, []byte("secret"))
		if _, err := s.Backend.Decrypt(ctx, s.OtherSymmetric, ct, c.Context()); err == nil {
			t.Fatal("another key decrypted it")
		}
		bad := append([]byte(nil), ct...)
		bad[len(bad)-1] ^= 1
		if _, err := c.Decrypt(ctx, bad); err == nil {
			t.Fatal("a damaged ciphertext decrypted")
		}
		if _, err := c.Decrypt(ctx, nil); err == nil {
			t.Fatal("an empty ciphertext decrypted")
		}
	})

	t.Run("data key", func(t *testing.T) {
		k := must(t, open(t, "inst-a"), keys.Archive)
		dk, err := k.GenerateDataKey(ctx)
		if err != nil || len(dk.Plaintext) != 32 || bytes.Equal(dk.Plaintext, dk.Wrapped) {
			t.Fatalf("data key: %v len %d", err, len(dk.Plaintext))
		}
		got, err := k.UnwrapDataKey(ctx, dk.Wrapped)
		if err != nil || !bytes.Equal(got, dk.Plaintext) {
			t.Fatalf("unwrap: %v", err)
		}
		other, _ := k.GenerateDataKey(ctx)
		if bytes.Equal(other.Plaintext, dk.Plaintext) {
			t.Fatal("two data keys are equal")
		}
		wrongCtx := must(t, open(t, "inst-b"), keys.Archive)
		if _, err := wrongCtx.UnwrapDataKey(ctx, dk.Wrapped); err == nil {
			t.Fatal("a data key unwrapped under another instance")
		}
	})

	t.Run("sign", func(t *testing.T) {
		k := must(t, open(t, "inst-a"), keys.Sign)
		pub, err := k.PublicKey(ctx)
		if err != nil || pub.Algorithm != "ES384" {
			t.Fatalf("public key: %+v, %v", pub, err)
		}
		ec, ok := pub.Key.(*ecdsa.PublicKey)
		if !ok {
			t.Fatalf("public key is %T", pub.Key)
		}
		digest := sha512.Sum384([]byte("message"))
		sig, err := k.Sign(ctx, digest[:])
		if err != nil || sig.Algorithm != "ES384" {
			t.Fatalf("sign: %+v, %v", sig, err)
		}
		if !ecdsa.VerifyASN1(ec, digest[:], sig.Value) {
			t.Fatal("signature does not verify")
		}
		other := sha512.Sum384([]byte("other"))
		if ecdsa.VerifyASN1(ec, other[:], sig.Value) {
			t.Fatal("signature verifies another digest")
		}
		raw, err := keys.ToJOSE(sig.Value, 48)
		if err != nil || len(raw) != 96 {
			t.Fatalf("ToJOSE: %d, %v", len(raw), err)
		}
	})

	t.Run("MAC", func(t *testing.T) {
		if s.NoMAC {
			t.Skip("backend has no MAC")
		}
		ks := open(t, "inst-a")
		p := must(t, ks, keys.Pseudonym)
		a1, err := p.MAC(ctx, "tenant-1", []byte("alice"))
		if err != nil || len(a1) != 32 {
			t.Fatalf("mac: %d, %v", len(a1), err)
		}
		a2, _ := p.MAC(ctx, "tenant-1", []byte("alice"))
		if !bytes.Equal(a1, a2) {
			t.Fatal("a pseudonym is not stable")
		}
		if b, _ := p.MAC(ctx, "tenant-2", []byte("alice")); bytes.Equal(a1, b) {
			t.Fatal("two tenants share a pseudonym")
		}
		if b, _ := p.MAC(ctx, "tenant-1", []byte("bob")); bytes.Equal(a1, b) {
			t.Fatal("two values share a pseudonym")
		}
		other := must(t, ks, keys.Archive) // same backend, different purpose and key
		if b, _ := other.MAC(ctx, "tenant-1", []byte("alice")); bytes.Equal(a1, b) {
			t.Fatal("two purposes share a pseudonym")
		}
		if _, err := p.MAC(ctx, "", []byte("x")); err == nil {
			t.Fatal("MAC accepted an empty tenant")
		}
	})

	t.Run("destroy", func(t *testing.T) {
		if s.NoMAC {
			t.Skip("backend has no MAC")
		}
		ks := open(t, "inst-a")
		p := must(t, ks, keys.Pseudonym)
		if s.NoDestroy {
			if _, err := p.MAC(ctx, "tenant-keep", []byte("alice")); err != nil {
				t.Fatal(err)
			}
			if err := p.Destroy(ctx, "tenant-keep"); !errors.Is(err, keys.ErrUnsupported) {
				t.Fatalf("Destroy: got %v, want ErrUnsupported", err)
			}
			if _, err := p.MAC(ctx, "tenant-keep", []byte("alice")); err != nil {
				t.Fatalf("a refused Destroy changed MAC: %v", err)
			}
			return
		}
		if _, err := p.MAC(ctx, "tenant-gone", []byte("alice")); err != nil {
			t.Fatal(err)
		}
		keep, err := p.MAC(ctx, "tenant-stay", []byte("alice"))
		if err != nil {
			t.Fatal(err)
		}
		if gone, err := p.Destroyed(ctx, "tenant-gone"); err != nil || gone {
			t.Fatalf("Destroyed before Destroy: %v, %v", gone, err)
		}
		if err := p.Destroy(ctx, "tenant-gone"); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if gone, err := p.Destroyed(ctx, "tenant-gone"); err != nil || !gone {
			t.Fatalf("Destroyed after Destroy: %v, %v", gone, err)
		}
		// Not the same Key value only: a new Keys over the same backend.
		for _, k := range []*keys.Key{p, must(t, open(t, "inst-a"), keys.Pseudonym)} {
			if _, err := k.MAC(ctx, "tenant-gone", []byte("alice")); !errors.Is(err, keys.ErrDestroyed) {
				t.Fatalf("MAC after Destroy: got %v, want ErrDestroyed", err)
			}
		}
		if after, err := p.MAC(ctx, "tenant-stay", []byte("alice")); err != nil || !bytes.Equal(after, keep) {
			t.Fatalf("another tenant changed: %v", err)
		}
		if err := p.Destroy(ctx, "tenant-gone"); err != nil {
			t.Fatalf("a second Destroy: %v", err)
		}
		// A tenant never used can be destroyed in advance, and stays destroyed.
		if err := p.Destroy(ctx, "tenant-never"); err != nil {
			t.Fatalf("Destroy of an unused tenant: %v", err)
		}
		if _, err := p.MAC(ctx, "tenant-never", []byte("x")); !errors.Is(err, keys.ErrDestroyed) {
			t.Fatalf("MAC of a tenant destroyed before use: got %v", err)
		}
		// Another purpose's material for the same tenant name is its own.
		if _, err := must(t, ks, keys.Archive).MAC(ctx, "tenant-gone", []byte("alice")); err != nil && !errors.Is(err, keys.ErrUnsupported) {
			t.Fatalf("destroying the pseudonym tenant reached another purpose: %v", err)
		}
		if err := p.Destroy(ctx, ""); err == nil {
			t.Fatal("Destroy accepted an empty tenant")
		}
	})

	t.Run("not configured", func(t *testing.T) {
		ks, err := keys.Open(keys.Config{Adapter: s.Backend.Name(), Keys: map[keys.Purpose]keys.Entry{
			keys.Conceal: {Key: s.Symmetric},
		}}, keys.Options{Backend: s.Backend, Instance: "i"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ks.For(keys.Sign); !errors.Is(err, keys.ErrNotConfigured) {
			t.Fatalf("got %v", err)
		}
	})
}
