package chart_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/profile"
)

// The chart derives the install preset from a table generated from the
// framework profile files; it must say what the Go derivation says, for every
// framework profile and every preset: refused exactly when the preset is
// weaker than the framework profile's minimum, and the refusal names the
// profile.
func TestTheChartDerivesThePresetAsGoDoes(t *testing.T) {
	frameworks, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(frameworks) == 0 {
		t.Fatal("no framework profiles: the sweep would prove nothing")
	}
	for name, fw := range frameworks {
		for _, preset := range profile.Presets {
			values := "profiles:\n  security: null\n  p:\n    frameworks: [" + name + "]\npreset: " + string(preset) + "\n"
			p := filepath.Join(t.TempDir(), "preset.yaml")
			if err := os.WriteFile(p, []byte(values), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(helm(t), "template", "audit", ".", "-f", "testdata/values/direct.yaml", "-f", p)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			weaker := preset.Rank() < fw.MinPreset.Rank()
			switch {
			case weaker && err == nil:
				t.Errorf("%s under %s rendered; its minimum is %s", name, preset, fw.MinPreset)
			case weaker && !strings.Contains(stderr.String(), "profile p (framework profile "+name+") needs "+string(fw.MinPreset)):
				t.Errorf("%s under %s was refused without naming the profile that needs more: %s", name, preset, stderr.String())
			case !weaker && err != nil && strings.Contains(stderr.String(), "is weaker than the profiles need"):
				t.Errorf("%s under %s was refused: %s", name, preset, stderr.String())
			}
		}
	}
}
