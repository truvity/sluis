//go:build !lambda

package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port/memory"
)

func TestMaintenanceOnStatusOffAndTheAuditTrail(t *testing.T) {
	ctx := context.Background()
	tables := []maintenance.Table{{Name: "oidc", State: memory.New()}, {Name: "github", State: memory.New()}}
	rec := audittest.New(t)
	var slept time.Duration
	orig := maintenanceSleep
	maintenanceSleep = func(_ context.Context, d time.Duration) { slept = d }
	t.Cleanup(func() { maintenanceSleep = orig })

	run := func(sub string, force bool) string {
		t.Helper()
		var out bytes.Buffer
		au := &operatorAudit{rec: rec, stderr: io.Discard}
		if err := maintenanceRun(ctx, &out, sub, tables, "example", "oleg", "layout v5", false, force, false, au); err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		return out.String()
	}
	if out := run("on", false); !strings.Contains(out, "maintenance on") || slept <= 0 {
		t.Errorf("on = %q (waited %v)", out, slept)
	}
	if out := run("status", false); !strings.Contains(out, "migration") || !strings.Contains(out, "oleg") {
		t.Errorf("status = %q", out)
	}
	if out := run("off", false); !strings.Contains(out, "2 table(s) lifted") {
		t.Errorf("off = %q", out)
	}
	if out := run("status", false); strings.Contains(out, "migration") {
		t.Errorf("status after off = %q", out)
	}
	got := rec.Records()
	if len(got) != 2 || got[0].GetAction() != "roster.maintenance.changed" || got[1].GetAction() != "roster.maintenance.changed" {
		t.Fatalf("audit records = %v, want one per change", got)
	}
}

func TestMaintenanceCmdTakesItsOwnFlagsOutOfTheCommandLine(t *testing.T) {
	var out bytes.Buffer
	if err := maintenanceCmd(&out, []string{"bogus"}); err == nil {
		t.Error("an unknown command was accepted")
	}
	if err := maintenanceCmd(&out, []string{"on", "--by"}); err == nil {
		t.Error("--by without a value was accepted")
	}
}
