//go:build !lambda

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestMigrateV5TakesTheMigrateFlagsAndAPlanSubcommand(t *testing.T) {
	for name, args := range map[string][]string{
		"no subcommand":  {"migrate", "v5"},
		"unknown":        {"migrate", "v5", "apply"},
		"copy not yet":   {"migrate", "v5", "copy", "--from", "a.yaml", "--to", "b.yaml"},
		"no files":       {"migrate", "v5", "plan"},
		"no to":          {"migrate", "v5", "plan", "--from", "a.yaml"},
		"the same":       {"migrate", "v5", "plan", "--from", "a.yaml", "--to", "a.yaml"},
		"blob mode":      {"migrate", "v5", "plan", "--from", "a.yaml", "--to", "b.yaml", "--blobs", "some"},
		"sessions":       {"migrate", "v5", "plan", "--from", "a.yaml", "--to", "b.yaml", "--sessions", "some"},
		"write flag":     {"migrate", "v5", "plan", "--from", "a.yaml", "--to", "b.yaml", "--overwrite"},
		"stray argument": {"migrate", "v5", "plan", "--from", "a.yaml", "--to", "b.yaml", "extra"},
	} {
		var out bytes.Buffer
		err := run(args, &out)
		if err == nil || (name != "write flag" && name != "stray argument" && !errors.Is(err, errUsage)) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var out bytes.Buffer
	if err := run([]string{"migrate", "v5", "plan", "--help"}, &out); err != nil || !strings.Contains(out.String(), "-sessions string") {
		t.Errorf("migrate v5 plan --help = %v, %q", err, out.String())
	}
	// The old command is unchanged.
	out.Reset()
	if err := run([]string{"migrate", "--help"}, &out); err != nil || !strings.Contains(out.String(), "migrate v5 plan") {
		t.Errorf("migrate --help = %v, %q", err, out.String())
	}
}
