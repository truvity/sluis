//go:build !lambda

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/migrate"
)

func TestMigrateV5TakesTheMigrateFlagsAndAPlanSubcommand(t *testing.T) {
	for name, args := range map[string][]string{
		"no subcommand":  {"migrate", "v5"},
		"unknown":        {"migrate", "v5", "apply"},
		"copy no files":  {"migrate", "v5", "copy"},
		"verify no to":   {"migrate", "v5", "verify", "--from", "a.yaml"},
		"verify write":   {"migrate", "v5", "verify", "--from", "a.yaml", "--to", "b.yaml", "--overwrite"},
		"plan dry run":   {"migrate", "v5", "plan", "--from", "a.yaml", "--to", "b.yaml", "--dry-run"},
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
		if err == nil || (name != "write flag" && name != "stray argument" && name != "verify write" && name != "plan dry run" && !errors.Is(err, errUsage)) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var out bytes.Buffer
	if err := run([]string{"migrate", "v5", "plan", "--help"}, &out); err != nil || !strings.Contains(out.String(), "-sessions string") {
		t.Errorf("migrate v5 plan --help = %v, %q", err, out.String())
	}
	out.Reset()
	if err := run([]string{"migrate", "v5", "copy", "--help"}, &out); err != nil || !strings.Contains(out.String(), "-i-have-stopped-writers") {
		t.Errorf("migrate v5 copy --help = %v, %q", err, out.String())
	}
	// The old command is unchanged.
	out.Reset()
	if err := run([]string{"migrate", "--help"}, &out); err != nil || !strings.Contains(out.String(), "migrate v5 plan") {
		t.Errorf("migrate --help = %v, %q", err, out.String())
	}
}

func TestThePlanReportIsOnStdoutAndTheSummaryOnStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	emitPlanReport(&stdout, &stderr, &migrate.PlanReport{From: "a.yaml", To: "b.yaml", OK: true})
	if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "{") || strings.Contains(stdout.String(), "migrate v5 plan") {
		t.Errorf("stdout = %q, want the JSON report alone", stdout.String())
	}
	if !strings.Contains(stderr.String(), "migrate v5 plan a.yaml -> b.yaml") || strings.Contains(stderr.String(), `"modules"`) {
		t.Errorf("stderr = %q, want the summary alone", stderr.String())
	}
}
