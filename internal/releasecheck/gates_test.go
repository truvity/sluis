package releasecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// run runs a script of hack/ and returns whether it succeeded and its output.
func run(t *testing.T, script string, args ...string) (bool, string) {
	t.Helper()
	out, err := exec.Command("bash", append([]string{filepath.Join(root, "hack", script)}, args...)...).CombinedOutput()
	if _, isExit := err.(*exec.ExitError); err != nil && !isExit {
		t.Fatalf("%s: %v", script, err)
	}
	return err == nil, string(out)
}

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestNoBreakingPatchGate: auto-release only ever cuts patches, and a patch is
// never breaking.
func TestNoBreakingPatchGate(t *testing.T) {
	for name, tc := range map[string]struct {
		changelog string
		pass      bool
	}{
		"clean unreleased":            {"## Unreleased\n\n- a fix\n\n## v1.2.0\n\n- **Breaking: old.**\n", true},
		"breaking unreleased":         {"## Unreleased\n\n- **Breaking: it.** text\n\n## v1.2.0\n", false},
		"breaking, bold colon":        {"## Unreleased\n\n- **Breaking:** it\n\n## v1.2.0\n", false},
		"breaking in a newer heading": {"## v1.3.0\n\n- **Breaking: it.**\n\n## v1.2.0\n", false},
		"only released":               {"## v1.2.0\n\n- **Breaking: it.**\n", true},
		"nothing pending":             {"## Unreleased\n\n## v1.2.0\n", true},
	} {
		ok, out := run(t, "check-no-breaking-patch.sh", write(t, tc.changelog), "v1.2.0")
		if ok != tc.pass {
			t.Errorf("%s: passed=%v, want %v: %s", name, ok, tc.pass, out)
		}
	}
}
