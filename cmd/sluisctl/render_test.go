package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const renderFixture = "../../config/testdata/truvity.installation.yaml"

// `sluisctl render` writes the two documents the golden files hold, and
// `--check` agrees with them until one is touched.
func TestRenderWritesAndChecksTheDocuments(t *testing.T) {
	out := filepath.Join(t.TempDir(), "rendered")
	if err := run([]string{"render", "--installation", renderFixture, "--out", out}); err != nil {
		t.Fatal(err)
	}
	for name, golden := range map[string]string{
		"sluis.yaml":  "../../config/testdata/truvity.sluis.yaml",
		"policy.yaml": "../../config/testdata/truvity.policy.yaml",
	} {
		got, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is not the golden document", name)
		}
	}
	if err := run([]string{"render", "--installation", renderFixture, "--out", out, "--check"}); err != nil {
		t.Errorf("--check on what was just written: %v", err)
	}

	// A document edited by hand is a difference, and the diff names the line.
	var printed bytes.Buffer
	saved := stdout
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	stdout = f
	t.Cleanup(func() { stdout = saved })
	service := filepath.Join(out, "sluis.yaml")
	raw, _ := os.ReadFile(service)
	if err := os.WriteFile(service, bytes.Replace(raw, []byte("level: info"), []byte("level: debug"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	err = run([]string{"render", "--installation", renderFixture, "--out", out, "--check"})
	if !errors.Is(err, errRenderedDiffers) {
		t.Fatalf("--check on an edited document: %v", err)
	}
	_, _ = f.Seek(0, 0)
	_, _ = printed.ReadFrom(f)
	for _, want := range []string{"-  level: debug", "+  level: info"} {
		if !strings.Contains(printed.String(), want) {
			t.Errorf("the diff has no %q:\n%s", want, printed.String())
		}
	}
	// And --check writes nothing: the edit is still there.
	if after, _ := os.ReadFile(service); !bytes.Contains(after, []byte("level: debug")) {
		t.Error("--check changed a file")
	}
	// A file that is not there is a difference too.
	if err := os.Remove(filepath.Join(out, "policy.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"render", "--installation", renderFixture, "--out", out, "--check"}); !errors.Is(err, errRenderedDiffers) {
		t.Errorf("--check with a missing file: %v", err)
	}
}

func TestRenderUsageAndRefusals(t *testing.T) {
	if err := run([]string{"render"}); err == nil {
		t.Error("render with nothing to render was accepted")
	}
	if err := run([]string{"render", "--installation", renderFixture}); err == nil {
		t.Error("render with no --out was accepted")
	}
	bad := filepath.Join(t.TempDir(), "installation.yaml")
	if err := os.WriteFile(bad, []byte("apiVersion: sluis.truvity.github.io/installation/v1\ninstance: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"render", "--installation", bad, "--out", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "shape") {
		t.Errorf("an installation with no shape: %v", err)
	}
}

func TestLineDiff(t *testing.T) {
	if lineDiff("a", "x\ny\n", "b", "x\ny\n") != "" {
		t.Error("equal texts differ")
	}
	d := lineDiff("a", "x\ny\nz\n", "b", "x\nq\nz\n")
	if !strings.Contains(d, "-y\n") || !strings.Contains(d, "+q\n") || strings.Contains(d, "-x") {
		t.Errorf("diff:\n%s", d)
	}
}
