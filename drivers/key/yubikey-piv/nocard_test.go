//go:build !atoom_card

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/v2/kits/go/cli"
)

// A build without the tag atoom_card has no C code: it has the same commands and refuses to open a card.
func TestABuildWithoutCardSupportSaysSoAndMakesNothing(t *testing.T) {
	base := t.TempDir()
	socket, admin := filepath.Join(base, "s"), filepath.Join(base, "a")
	code, _, stderr := run(t, append([]string{"serve", "--environment", "development", "--socket", socket, "--admin-socket", admin, "--node", "n"}, pinFlags(t)...)...)
	if code != cli.ExitFailed || !strings.Contains(stderr, "atoom_card") {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	for _, p := range []string{socket, admin} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s was made", p)
		}
	}
	if code, _, _ := run(t, "check", "--environment", "development"); code != cli.ExitFailed {
		t.Fatalf("check: exit %d, want 1", code)
	}
}
