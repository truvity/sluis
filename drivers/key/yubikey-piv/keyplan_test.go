//go:build sluis_test

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/atoom/key/yubikeypiv"

	"github.com/truvity/sluis/v2/kits/go/signedconfig"
)

// The card driver is atoom's: it asks its host, through yubikeypiv.PeerKeys, for the pin-release key that a verified KeyPlan lists for a
// node. The check of the signed KeyPlan is sluis's own contract, so it is made here, and tested here: the publisher's signature, the kind,
// the seq (also across a restart, from the mark in the state directory), the schema of the payload and the slot map. The tests of the
// driver's side of the PIN release are in atoom.

const publisherKID = "test-publisher"

func p384(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// node is a node of the slot map: its pin-release key, slot 87.
type node struct {
	name string
	key  *ecdsa.PrivateKey
}

func (n node) spki(t *testing.T) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&n.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// signPlanWith signs a KeyPlan of a seq whose slot map lists the nodes.
func signPlanWith(t *testing.T, key *ecdsa.PrivateKey, seq uint64, kind string, nodes ...node) []byte {
	t.Helper()
	var p strings.Builder
	p.WriteString("default:\n  cache_ttl: 15m\n  slot_map:")
	if len(nodes) == 0 {
		p.WriteString(" []")
	}
	p.WriteString("\n")
	for _, n := range nodes {
		fmt.Fprintf(&p, "    - node: %s\n      slots:\n        - { slot: \"87\", purpose: pin-release, public_key: %s }\n",
			n.name, base64.RawURLEncoding.EncodeToString(n.spki(t)))
	}
	doc, err := signedconfig.Sign(key, publisherKID, seq, kind, []byte(p.String()))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func stateDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func checker(t *testing.T, publisher *ecdsa.PrivateKey, dir string) *planKeys {
	t.Helper()
	k, err := newPlanKeys([]signedconfig.Publisher{{KID: publisherKID, Key: &publisher.PublicKey}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func wantKey(t *testing.T, k *planKeys, doc []byte, n node) {
	t.Helper()
	got, err := k.PeerKey(context.Background(), doc, n.name)
	if err != nil {
		t.Fatalf("PeerKey(%s): %v", n.name, err)
	}
	if !got.Equal(&n.key.PublicKey) {
		t.Fatalf("PeerKey(%s) returned another key", n.name)
	}
}

func wantRefusedPlan(t *testing.T, what string, k *planKeys, doc []byte, n node) {
	t.Helper()
	_, err := k.PeerKey(context.Background(), doc, n.name)
	if err == nil || errors.Is(err, yubikeypiv.ErrUnlisted) {
		t.Fatalf("%s: err = %v, want a refused KeyPlan", what, err)
	}
}

// The check verifies the KeyPlan itself: a KeyPlan signed by another key, for another kind, a lower seq than one it verified (also after a
// restart, from its mark), or another payload under that seq, is refused; a node that the verified plan does not list is ErrUnlisted.
//
// verifies: REQ-KEYS-115
func TestThePlanKeysVerifyTheKeyPlanAndItsSeq(t *testing.T) {
	publisher := p384(t)
	a, b := node{"node-a", p384(t)}, node{"node-b", p384(t)}
	dir := stateDir(t)
	k := checker(t, publisher, dir)

	stranger := p384(t)
	for name, doc := range map[string][]byte{
		"another key":  signPlanWith(t, stranger, 5, "KeyPlan", a, b),
		"another kind": signPlanWith(t, publisher, 5, "Boundary", a, b),
		"no document":  nil,
		"not a JWS":    []byte("{}"),
	} {
		wantRefusedPlan(t, name, k, doc, a)
	}

	wantKey(t, k, signPlanWith(t, publisher, 2, "KeyPlan", a, b), a)
	wantRefusedPlan(t, "seq 1 after seq 2", k, signPlanWith(t, publisher, 1, "KeyPlan", a, b), a)
	wantRefusedPlan(t, "seq 2, another payload", k, signPlanWith(t, publisher, 2, "KeyPlan", b), a)

	// A node the verified plan does not list is unlisted, not a refused plan.
	if _, err := k.PeerKey(context.Background(), signPlanWith(t, publisher, 3, "KeyPlan", b), a.name); !errors.Is(err, yubikeypiv.ErrUnlisted) {
		t.Fatalf("a node that the plan does not list: %v, want ErrUnlisted", err)
	}
	if _, err := k.PeerKey(context.Background(), signPlanWith(t, publisher, 4, "KeyPlan"), a.name); !errors.Is(err, yubikeypiv.ErrUnlisted) {
		t.Fatalf("an empty slot map: %v, want ErrUnlisted", err)
	}

	fi, err := os.Stat(filepath.Join(dir, "keyplan.mark"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the mark: %v %v", fi, err)
	}
	// A check started on the same state takes the mark: seq 1 is refused from its first call.
	again := checker(t, publisher, dir)
	wantRefusedPlan(t, "seq 1 after a restart", again, signPlanWith(t, publisher, 1, "KeyPlan", a, b), a)
	wantKey(t, again, signPlanWith(t, publisher, 5, "KeyPlan", a, b), b)

	// A check without publisher keys verifies no KeyPlan and releases nothing.
	none, err := newPlanKeys(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	wantRefusedPlan(t, "no publisher keys", none, signPlanWith(t, publisher, 9, "KeyPlan", a, b), a)
}

// A signed KeyPlan whose payload the schema refuses leaves no trace: the mark is not saved and its seq is not admitted, so a valid KeyPlan
// of a lower seq, signed later, is still taken.
//
// verifies: REQ-KEYS-115, REQ-DATA-038
func TestAKeyPlanWithAPayloadTheSchemaRefusesLeavesNoMark(t *testing.T) {
	publisher := p384(t)
	a := node{"node-a", p384(t)}
	dir := stateDir(t)
	k := checker(t, publisher, dir)
	wantKey(t, k, signPlanWith(t, publisher, 2, "KeyPlan", a), a)
	markPath := filepath.Join(dir, "keyplan.mark")
	before, err := os.ReadFile(markPath) //nolint:gosec // the test's own state directory
	if err != nil {
		t.Fatal(err)
	}
	bad, err := signedconfig.Sign(publisher, publisherKID, 9, "KeyPlan", []byte("default:\n  cache_ttl: PT15M\n"))
	if err != nil {
		t.Fatal(err)
	}
	wantRefusedPlan(t, "a payload the schema refuses", k, bad, a)
	after, err := os.ReadFile(markPath) //nolint:gosec // the test's own state directory
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("the mark changed for a payload that was refused: %v", err)
	}
	wantKey(t, k, signPlanWith(t, publisher, 3, "KeyPlan", a), a)
}

// The slot map lists only P-384 keys: a node with another kind of key is not a listed peer.
func TestAKeyThatIsNotP384IsNotAListedPeer(t *testing.T) {
	publisher := p384(t)
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	n := node{"node-a", p256}
	k := checker(t, publisher, "")
	if _, err := k.PeerKey(context.Background(), signPlanWith(t, publisher, 1, "KeyPlan", n), n.name); !errors.Is(err, yubikeypiv.ErrUnlisted) {
		t.Fatalf("a P-256 key: %v, want ErrUnlisted", err)
	}
}

// A mark that is not the driver's own regular file, or that does not parse, stops the start.
func TestADamagedMarkStopsTheStart(t *testing.T) {
	publisher := p384(t)
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, "keyplan.mark"), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newPlanKeys([]signedconfig.Publisher{{KID: publisherKID, Key: &publisher.PublicKey}}, dir); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("a damaged mark: %v", err)
	}
	if _, err := newPlanKeys(nil, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a state directory that is missing was taken")
	}
}
