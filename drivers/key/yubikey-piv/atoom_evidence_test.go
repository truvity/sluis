package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The card driver, the software driver and the two small libraries they need are atoom's (github.com/truvity/atoom, pinned by release in
// go.mod). The tests that prove what these requirements say about the card driver are atoom's tests, tagged there as here. This test runs
// them at the release sluis is pinned to, so that the evidence of these records is a run in this repository's CI and not a claim: the fake
// card of atoom's tests, the kit of its key port, the slot plan and the PIN release format. A change of the pin that breaks one of them
// fails here before it reaches a board.
//
// verifies: REQ-KEYS-002, REQ-KEYS-004, REQ-KEYS-022, REQ-KEYS-058, REQ-KEYS-228, REQ-KEYS-235, REQ-KEYS-274, REQ-KEYS-275
func TestTheTestsOfTheAtoomKeyLibrariesPassAtThePinnedRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("SKIP: -short; the run of atoom's tests takes a few seconds")
	}
	// The libraries and not the thin mains (`cmd/...`): a main is atoom's socket host, which sluis does not run (sluis's hosts are its own).
	pkgs := []string{
		"github.com/truvity/atoom/key/yubikeypiv",
		"github.com/truvity/atoom/key/yubikeypiv/internal/...",
		"github.com/truvity/atoom/key/software",
		"github.com/truvity/atoom/key/software/internal/...",
		"github.com/truvity/atoom/key/slotplan/...",
		"github.com/truvity/atoom/key/pinrelease/...",
	}
	out, err := exec.Command("go", append([]string{"test", "-count=1"}, pkgs...)...).CombinedOutput() //nolint:gosec // fixed arguments
	if err != nil {
		t.Fatalf("atoom's tests fail at the pinned commit: %v\n%s", err, strings.TrimSpace(string(out)))
	}
}
