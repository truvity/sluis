//nolint:lll // metric descriptions are prose
package minter

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// What the minter reports. The label is the preset's name and the variant
// (stored or on_demand); never a caller, a token or an account's id.
//
//	sluis.cloudflare.rotation.last_timestamp   when the stored credential was minted (Unix seconds)
//	sluis.cloudflare.rotation.interval         the preset's `rotation`, in seconds
//	sluis.cloudflare.tokens.minted             mints by preset, variant and outcome (ok, refused, failed)
//	sluis.cloudflare.tokens.swept              expired tokens deleted, by preset
//
// The alert fires when the last rotation is older than twice the interval.
const meterName = "github.com/truvity/sluis/cloudflare"

type instruments struct {
	lastRotation metric.Int64Gauge
	rotation     metric.Int64Gauge
	minted       metric.Int64Counter
	sweptTokens  metric.Int64Counter
	refused      metric.Int64Counter
}

var meters = newInstruments()

func newInstruments() instruments {
	meter := otel.Meter(meterName)
	last, _ := meter.Int64Gauge("sluis.cloudflare.rotation.last_timestamp",
		metric.WithUnit("s"),
		metric.WithDescription("When a Cloudflare preset's stored credential was minted, as seconds since the Unix epoch."))
	interval, _ := meter.Int64Gauge("sluis.cloudflare.rotation.interval",
		metric.WithUnit("s"),
		metric.WithDescription("A Cloudflare preset's configured rotation, in seconds."))
	minted, _ := meter.Int64Counter("sluis.cloudflare.tokens.minted",
		metric.WithDescription("Cloudflare credentials minted, by preset, variant (stored or on_demand) and outcome (ok, refused or failed)."))
	swept, _ := meter.Int64Counter("sluis.cloudflare.tokens.swept",
		metric.WithDescription("Expired Cloudflare tokens sluis deleted, by preset."))
	refused, _ := meter.Int64Counter("sluis.cloudflare.prototype.refused",
		metric.WithDescription("Prototypes refused at a mint or a check, by preset and reason (prototype_active, prototype_forbidden, prototype_missing). Any is an error to look at."))
	return instruments{lastRotation: last, rotation: interval, minted: minted, sweptTokens: swept, refused: refused}
}

func (i instruments) lastRotationAt(ctx context.Context, preset string, at time.Time) {
	i.lastRotation.Record(ctx, at.Unix(), metric.WithAttributes(attribute.String("preset", preset)))
}

func (i instruments) mint(ctx context.Context, preset, variant, outcome string) {
	i.minted.Add(ctx, 1, metric.WithAttributes(
		attribute.String("preset", preset), attribute.String("variant", variant), attribute.String("outcome", outcome)))
}

func (i instruments) swept(ctx context.Context, preset string, n int) {
	i.sweptTokens.Add(ctx, int64(n), metric.WithAttributes(attribute.String("preset", preset)))
}

func (i instruments) interval(ctx context.Context, preset string, d time.Duration) {
	i.rotation.Record(ctx, int64(d.Seconds()), metric.WithAttributes(attribute.String("preset", preset)))
}

func (i instruments) prototypeRefused(ctx context.Context, preset, reason string) {
	i.refused.Add(ctx, 1, metric.WithAttributes(attribute.String("preset", preset), attribute.String("reason", reason)))
}
