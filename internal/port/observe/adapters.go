package observe

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/truvity/sluis/internal/port"
)

// Announce logs the resolved adapter table once and sets the gauge
// `sluis_adapter_info{concern,adapter} 1` for each concern, so a dashboard can
// say which adapter an installation runs and an alert can see one change.
//
// The labels are the concern and the adapter's name, both from fixed sets;
// never a setting, which may be an address or a bucket.
func Announce(ctx context.Context, log *slog.Logger, t port.Table) {
	info, _ := otel.Meter(meterName).Int64Gauge("sluis.adapter.info",
		metric.WithDescription("The adapter in use for each concern; the value is always 1."))
	attrs := make([]slog.Attr, 0, len(port.Concerns))
	for _, c := range port.Concerns {
		ch, ok := t[c]
		if !ok {
			continue
		}
		info.Record(ctx, 1, metric.WithAttributes(
			attribute.String("concern", string(c)), attribute.String("adapter", ch.Adapter)))
		attrs = append(attrs, slog.String(string(c), ch.Adapter))
	}
	log.LogAttrs(ctx, slog.LevelInfo, "adapters resolved", attrs...)
}
