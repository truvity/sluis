package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/truvity/atoom/key/yubikeypiv"

	"github.com/truvity/sluis/v2/kits/go/rpc"
)

// Only the owner of the card driver, key, may call it, and the socket is for the driver's user alone.
func TestTheKeySocketIsOwnerOnly(t *testing.T) {
	cfg := keyServerConfig("test", "/run/x.sock")
	if cfg.Mode != 0o600 || cfg.Actor != name || cfg.Boundary != "test" {
		t.Fatalf("config = %+v", cfg)
	}
	if owner() != "key" {
		t.Fatalf("the owner of %s is %q, want key", name, owner())
	}
	if !cfg.Allow(rpc.Peer{Name: "key"}, "/atoom.key.v1alpha1.KeyStoreService/Sign") {
		t.Error("the owner was refused")
	}
	if cfg.Allow(rpc.Peer{Name: "session"}, "/atoom.key.v1alpha1.KeyStoreService/Sign") {
		t.Error("another actor was allowed")
	}
}

// The driver admits as many RSA signatures by default as key@'s bucket for the purpose lets through at once (its burst), so a burst that
// key@ allows is signed and not refused as busy. The two trees may not import each other, so the test reads the constant of key@ from
// its source and holds the driver's default to it; a change of one without the other fails here.
//
// verifies: REQ-KEYS-276
func TestTheDriversRSAPlacesAreTheBurstOfKeysBucket(t *testing.T) {
	src, err := os.ReadFile("../../../actors/key/internal/keyd/rsalimit.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*cardRSABurst\s*=\s*([0-9]+(?:\.[0-9]+)?)\s*$`).FindSubmatch(src)
	if m == nil {
		t.Fatal("cardRSABurst is not a constant of rsalimit.go that this test can read")
	}
	burst, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		t.Fatal(err)
	}
	if float64(yubikeypiv.RSAQueueDefault) != burst {
		t.Fatalf("the driver admits %d RSA signatures by default and key@'s bucket has a burst of %v: they are one number", yubikeypiv.RSAQueueDefault, burst)
	}
}
