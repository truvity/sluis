package clientcreds

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "github.com/truvity/sluis/clientcreds"

var instruments = newInstruments()

type metrics struct {
	reconciled metric.Int64Counter
	auth       metric.Int64Counter
	rotations  metric.Int64Counter
	purges     metric.Int64Counter
	orphans    metric.Int64Counter
}

func newInstruments() metrics {
	meter := otel.Meter(meterName)
	// Instrument creation fails only on an invalid name, which these are not;
	// a failed one is a no-op instrument.
	reconciled, _ := meter.Int64Counter("sluis.client_secret.reconcile",
		metric.WithDescription("Generated client secrets looked after, by outcome: created, adopted, existing, conflict, unsupported or failed."))
	auth, _ := meter.Int64Counter("sluis.client_secret.auth",
		metric.WithDescription("Confidential client authentications at the token endpoint, by the slot that matched: current, previous or none."))
	rotations, _ := meter.Int64Counter("sluis.client_secret.rotations",
		metric.WithDescription("Generated client secret rotations asked for by a person, by outcome: ok, busy, not_generated, no_record or failed."))
	purges, _ := meter.Int64Counter("sluis.client_secret.purges",
		metric.WithDescription("Orphaned client secret records purged by a person, by outcome: ok, still_declared, no_record, busy or failed."))
	orphans, _ := meter.Int64Counter("sluis.client_secret.orphans",
		metric.WithDescription("Client secret records newly found with no generated client in the policy, each counted once."))
	return metrics{reconciled, auth, rotations, purges, orphans}
}

// CountAuth counts one confidential client authentication by the slot that
// matched ([SlotCurrent], [SlotPrevious] or [SlotNone]). No client id: a
// refused guess must not mint a metric series per id an attacker invents.
func CountAuth(ctx context.Context, slot string) {
	instruments.auth.Add(ctx, 1, metric.WithAttributes(attribute.String("slot", slot)))
}

func countReconcile(ctx context.Context, o Outcome) {
	instruments.reconciled.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}

func countRotation(ctx context.Context, outcome string) {
	instruments.rotations.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

func countPurge(ctx context.Context, outcome string) {
	instruments.purges.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

func countOrphan(ctx context.Context) { instruments.orphans.Add(ctx, 1) }
