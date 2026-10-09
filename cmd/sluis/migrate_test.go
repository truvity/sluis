//go:build !lambda

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestMigrateNeedsItsTwoFiles(t *testing.T) {
	for name, args := range map[string][]string{
		"none":      {"migrate"},
		"no to":     {"migrate", "--from", "a.yaml"},
		"the same":  {"migrate", "--from", "a.yaml", "--to", "a.yaml"},
		"blob mode": {"migrate", "--from", "a.yaml", "--to", "b.yaml", "--blobs", "some"},
	} {
		var out bytes.Buffer
		if err := run(args, &out); !errors.Is(err, errUsage) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var out bytes.Buffer
	if err := run([]string{"migrate", "--help"}, &out); err != nil || !strings.Contains(out.String(), "--i-have-stopped-writers") {
		t.Errorf("migrate --help = %v, %q", err, out.String())
	}
}
