package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestMigrateSecretsLayoutTakesOneStepAtATime(t *testing.T) {
	for name, args := range map[string][]string{
		"no config":      {"--to", "v4"},
		"no step":        {"--config", "x.yaml"},
		"another layout": {"--config", "x.yaml", "--to", "v3"},
		"both steps":     {"--config", "x.yaml", "--to", "v4", "--delete-v3"},
		"an argument":    {"--config", "x.yaml", "--to", "v4", "extra"},
	} {
		var out bytes.Buffer
		if err := migrateCmd(&out, append([]string{"secrets-layout"}, args...)); !errors.Is(err, errUsage) {
			t.Errorf("%s: err = %v, want a usage error", name, err)
		}
	}
	var out bytes.Buffer
	if err := migrateCmd(&out, []string{"secrets-layout", "-h"}); err != nil || !bytes.Contains(out.Bytes(), []byte("--delete-v3")) {
		t.Errorf("help = %v %q", err, out.String())
	}
}
