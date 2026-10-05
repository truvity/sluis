package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
)

// `sluisctl policy render` writes the one document a process loads, from a
// directory of layers, and the binary's loader reads it back.
func TestPolicyRenderWritesADocumentTheServiceLoads(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"policy.yaml":   "version: 1\ngroups:\n  all:access-roster:operator: {}\n",
		"clusters.yaml": "apiVersion: sluis.truvity.github.io/policy/v2\nexchange: {clusters: [{name: devel, issuer: 'https://k.example'}]}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "policy.yaml")
	if err := run([]string{"policy", "render", "-o", out, dir}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil || !strings.HasPrefix(string(raw), "apiVersion: sluis.truvity.github.io/policy/v2\n") {
		t.Fatalf("rendered %q, %v", raw, err)
	}
	d, err := config.Load[config.PolicyDocument](out)
	if err != nil || len(d.Clusters()) != 1 || len(d.Policy.Groups) != 1 {
		t.Fatalf("the rendered document does not load as rendered: %+v %v", d, err)
	}
	if err := run([]string{"policy", "render"}); err == nil {
		t.Error("render with nothing to render was accepted")
	}
	if err := run([]string{"policy", "nonsense"}); err == nil {
		t.Error("an unknown subcommand was accepted")
	}
}
