package controller

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/truvity/sluis/internal/outlineroster/status"
)

// meterName is the instrumentation scope every instrument here is under.
const meterName = "github.com/truvity/sluis/outlineroster"

// instruments are the controller's own metrics. The tick's span, duration and
// outcome and the leases are the rails' (access_roster.*).
type instruments struct {
	passes   metric.Int64Counter
	changes  metric.Int64Counter
	breakers metric.Int64Counter
	rows     metric.Int64Gauge
	reported metric.Int64Gauge
}

func newInstruments() instruments {
	meter := otel.Meter(meterName)
	// Instrument creation fails only on an invalid name, which these are not;
	// a failed one is a no-op instrument, never a stopped controller.
	passes, _ := meter.Int64Counter("outline_roster.passes",
		metric.WithDescription("Passes over an Outline workspace, by outcome."))
	changes, _ := meter.Int64Counter("outline_roster.changes",
		metric.WithDescription("Changes made to Outline, by action and whether Outline accepted them."))
	breakers, _ := meter.Int64Counter("outline_roster.breaker_trips",
		metric.WithDescription("Passes that held a set of changes behind a breaker."))
	rows, _ := meter.Int64Gauge("outline_roster.rows",
		metric.WithDescription("Rows in a workspace's last report (accounts, group members, grants and direct members), by state."))
	reported, _ := meter.Int64Gauge("outline_roster.reported",
		metric.WithDescription("Findings reported and never acted on in a workspace's last report, by kind."))
	return instruments{passes, changes, breakers, rows, reported}
}

// recordPass records one workspace's report.
func (m instruments) recordPass(ctx context.Context, report *status.Workspace) {
	workspace := attribute.String("workspace", report.Workspace)
	m.passes.Add(ctx, 1, metric.WithAttributes(workspace, attribute.String("outcome", string(report.Tick.Outcome))))
	rows := map[status.State]int64{}
	for i := range report.Users {
		u := &report.Users[i]
		rows[u.State]++
	}
	for _, g := range report.Groups {
		for _, member := range g.Members {
			rows[member.State]++
		}
	}
	for i := range report.Collections {
		c := &report.Collections[i]
		for _, g := range c.Grants {
			rows[g.State]++
		}
		for _, member := range c.Members {
			rows[member.State]++
		}
	}
	for _, state := range []status.State{
		status.StateOK, status.StateWillAdd, status.StateWillRemove, status.StateWillChange, status.StateWillSuspend,
		status.StateWillActivate, status.StateHeld, status.StateRetrying, status.StateReported, status.StateIgnored,
	} {
		m.rows.Record(ctx, rows[state], metric.WithAttributes(workspace, attribute.String("state", string(state))))
	}
	reported := map[string]int64{}
	for _, f := range report.Reported {
		reported[f.Kind]++
	}
	for _, kind := range []string{status.FindingSetting, status.FindingShare, status.FindingIntegration} {
		m.reported.Record(ctx, reported[kind], metric.WithAttributes(workspace, attribute.String("kind", kind)))
	}
	for _, b := range report.Breakers {
		if !b.Confirmed {
			m.breakers.Add(ctx, 1, metric.WithAttributes(workspace))
			break
		}
	}
}

// recordChange records one change Outline was asked to make.
func (m instruments) recordChange(ctx context.Context, workspace string, action status.Action, ok bool) {
	outcome := "ok"
	if !ok {
		outcome = "failed"
	}
	m.changes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("workspace", workspace), attribute.String("action", string(action)), attribute.String("outcome", outcome)))
}
