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

const threePresets = "presets:\n" +
	"  operational: {bucket: audit-operational}\n" +
	"  standard: {bucket: audit-standard}\n" +
	"  attested: {bucket: audit-attested}\n"

func helmTemplate(t *testing.T, values string) (string, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "preset.yaml")
	if err := os.WriteFile(p, []byte(values), 0o600); err != nil {
		t.Fatal(err)
	}
	// The base names a preset of its own; `presets: null` clears it, so that the
	// values under test are the presets the install has.
	cleared := filepath.Join(t.TempDir(), "cleared.yaml")
	if err := os.WriteFile(cleared, []byte("presets: null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helm(t), "template", "audit", ".", "-f", "testdata/values/operational-base.yaml", "-f", cleared, "-f", p)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

// The chart derives a profile's preset from a table generated from the
// framework profile files; it must say what the Go derivation says, for every
// framework profile and every preset a profile may ask for: refused exactly
// when the preset is weaker than the framework profile's minimum, and the
// refusal names the profile and the framework profile. And, with only the
// preset the profile needs configured, it renders; with only a weaker one
// configured, it is refused naming both (profile.Deployment.CheckStorage).
func TestTheChartDerivesAProfilesPresetAsGoDoes(t *testing.T) {
	frameworks, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(frameworks) == 0 {
		t.Fatal("no framework profiles: the sweep would prove nothing")
	}
	for name, fw := range frameworks {
		minimum := fw.MinPreset
		if minimum == "" {
			minimum = profile.Operational
		}
		for _, preset := range profile.Presets {
			values := "profiles:\n  p:\n    frameworks: [" + name + "]\n    preset: " + string(preset) + "\n" + threePresets
			stderr, err := helmTemplate(t, values)
			weaker := preset.Rank() < minimum.Rank()
			switch {
			case weaker && err == nil:
				t.Errorf("%s asking for %s rendered; its minimum is %s", name, preset, minimum)
			case weaker && !strings.Contains(stderr, "profile p asks for the "+string(preset)+" preset and its framework profiles "+name+" need "+string(minimum)):
				t.Errorf("%s asking for %s was refused without naming the profile that needs more: %s", name, preset, stderr)
			case !weaker && err != nil:
				t.Errorf("%s asking for %s was refused: %s", name, preset, stderr)
			}
		}
		// The preset the profile is kept under must be configured, and the
		// refusal names both.
		for _, configured := range profile.Presets {
			values := "profiles:\n  p:\n    frameworks: [" + name + "]\npresets:\n  " + string(configured) + ": {bucket: audit-" + string(configured) + "}\n"
			stderr, err := helmTemplate(t, values)
			missing := configured != minimum
			switch {
			case missing && err == nil:
				t.Errorf("%s needs %s and only %s is configured, and it rendered", name, minimum, configured)
			case missing && (!strings.Contains(stderr, "profile p is kept under the "+string(minimum)+" preset") ||
				!strings.Contains(stderr, "configures only "+string(configured))):
				t.Errorf("%s needs %s and only %s is configured: the refusal does not name both: %s", name, minimum, configured, stderr)
			case !missing && err != nil && strings.Contains(stderr, "is kept under the"):
				t.Errorf("%s under its own preset %s was refused: %s", name, configured, stderr)
			}
		}
	}
}
