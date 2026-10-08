package local_test

import (
	"bytes"
	"testing"

	"github.com/truvity/sluis/storage/keys/conformance"
	"github.com/truvity/sluis/storage/keys/local"
)

func TestConformance(t *testing.T) {
	b, err := local.New(local.RandomRoot())
	if err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, conformance.Subject{Backend: b, Symmetric: "sym-one", OtherSymmetric: "sym-two", Signing: "sign-one"})
}

func TestRefusesWeakRoots(t *testing.T) {
	for name, root := range map[string][]byte{
		"empty":    nil,
		"short":    local.RandomRoot()[:31],
		"repeated": bytes.Repeat([]byte{0x41}, 64),
		"word":     bytes.Repeat([]byte("password"), 8),
		"counter":  []byte("0123456789abcdef0123456789abcdef")[:15],
	} {
		if _, err := local.New(root); err == nil {
			t.Errorf("%s: weak root accepted", name)
		}
	}
}

func TestSameRootSameKeys(t *testing.T) {
	root := local.RandomRoot()
	a, _ := local.New(root)
	b, _ := local.New(root)
	ct, _ := a.Encrypt(t.Context(), "k", []byte("x"), nil)
	if _, err := b.Decrypt(t.Context(), "k", ct, nil); err != nil {
		t.Fatal(err)
	}
	other, _ := local.New(local.RandomRoot())
	if _, err := other.Decrypt(t.Context(), "k", ct, nil); err == nil {
		t.Fatal("another root decrypted")
	}
}

func TestValidateName(t *testing.T) {
	b, _ := local.New(local.RandomRoot())
	if b.ValidateName("alias/x") == nil || b.ValidateName("Seal") == nil || b.ValidateName("seal-1") != nil {
		t.Fatal("names")
	}
}
