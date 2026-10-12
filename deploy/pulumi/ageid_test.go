package sluispulumi_test

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"

	"filippo.io/age"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

func TestTheDerivedAgeIdentityRoundTripsThroughAge(t *testing.T) {
	for range 20 {
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			t.Fatal(err)
		}
		id, rcpt, err := arp.AgeKeypairFromSeed(seed)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := age.ParseX25519Identity(id)
		if err != nil {
			t.Fatalf("age does not parse %q: %v", id, err)
		}
		if parsed.Recipient().String() != rcpt {
			t.Fatalf("the recipient is %s, age derives %s", rcpt, parsed.Recipient())
		}
		if _, err := age.ParseX25519Recipient(rcpt); err != nil {
			t.Fatalf("age does not parse the recipient %q: %v", rcpt, err)
		}

		// Encrypt to the recipient, decrypt with the identity.
		r, err := age.ParseX25519Recipient(rcpt)
		if err != nil {
			t.Fatal(err)
		}
		var ct bytes.Buffer
		w, err := age.Encrypt(&ct, r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("the archive")); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		pr, err := age.Decrypt(&ct, parsed)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(pr)
		if err != nil || string(got) != "the archive" {
			t.Fatalf("decrypted %q, %v", got, err)
		}
	}
}

func TestTheAgeSeedIsTheSameKeypairEveryTime(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, 32)
	a, ar, _ := arp.AgeKeypairFromSeed(seed)
	b, br, _ := arp.AgeKeypairFromSeed(seed)
	if a != b || ar != br {
		t.Fatal("the same seed gave two keypairs")
	}
	if _, _, err := arp.AgeKeypairFromSeed(seed[:31]); err == nil {
		t.Fatal("a short seed was accepted")
	}
}
