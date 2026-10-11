package telemetry

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/metric"
)

// The two prefixes the product's metrics were published under before the
// rename, and the prefix they have now. The old names are no longer recorded
// (v1.76 ended the dual-name window); the constructors below still take the
// old-prefixed name a call site was written with and create the instrument
// under the `sluis.` name.
const (
	legacyIssuerPrefix = "access_issuer."
	legacyRosterPrefix = "access_roster."
	currentPrefix      = "sluis."
)

// CurrentName is the `sluis.` name of an instrument written with its old
// prefix: `access_issuer.http.requests` and `access_roster.ticks` become
// `sluis.http.requests` and `sluis.ticks`. A name with neither prefix is
// returned as it is. Only the prefix changes, so the attributes, the unit and
// the type of an instrument are those of the old one.
func CurrentName(legacy string) string {
	for _, p := range []string{legacyIssuerPrefix, legacyRosterPrefix} {
		if rest, ok := strings.CutPrefix(legacy, p); ok {
			return currentPrefix + rest
		}
	}
	return legacy
}

// Int64Counter records into the `sluis.` counter.
type Int64Counter struct{ current metric.Int64Counter }

// NewInt64Counter creates the counter under the `sluis.` form of the name.
func NewInt64Counter(meter metric.Meter, legacyName string, opts ...metric.Int64CounterOption) Int64Counter {
	// Instrument creation fails only on an invalid name, which these are not; a
	// failed one is a no-op instrument, never a stopped service.
	current, _ := meter.Int64Counter(CurrentName(legacyName), opts...)
	return Int64Counter{current}
}

// Add records n.
func (c Int64Counter) Add(ctx context.Context, n int64, opts ...metric.AddOption) {
	c.current.Add(ctx, n, opts...)
}

// Int64UpDownCounter is [Int64Counter] for a counter that goes down.
type Int64UpDownCounter struct{ current metric.Int64UpDownCounter }

// NewInt64UpDownCounter creates the counter under the `sluis.` form of the name.
func NewInt64UpDownCounter(meter metric.Meter, legacyName string, opts ...metric.Int64UpDownCounterOption) Int64UpDownCounter {
	current, _ := meter.Int64UpDownCounter(CurrentName(legacyName), opts...)
	return Int64UpDownCounter{current}
}

// Add records n.
func (c Int64UpDownCounter) Add(ctx context.Context, n int64, opts ...metric.AddOption) {
	c.current.Add(ctx, n, opts...)
}

// Int64Gauge records into the `sluis.` gauge.
type Int64Gauge struct{ current metric.Int64Gauge }

// NewInt64Gauge creates the gauge under the `sluis.` form of the name.
func NewInt64Gauge(meter metric.Meter, legacyName string, opts ...metric.Int64GaugeOption) Int64Gauge {
	current, _ := meter.Int64Gauge(CurrentName(legacyName), opts...)
	return Int64Gauge{current}
}

// Record records v.
func (g Int64Gauge) Record(ctx context.Context, v int64, opts ...metric.RecordOption) {
	g.current.Record(ctx, v, opts...)
}

// Float64Histogram records into the `sluis.` histogram.
type Float64Histogram struct{ current metric.Float64Histogram }

// NewFloat64Histogram creates the histogram under the `sluis.` form of the name.
func NewFloat64Histogram(meter metric.Meter, legacyName string, opts ...metric.Float64HistogramOption) Float64Histogram {
	current, _ := meter.Float64Histogram(CurrentName(legacyName), opts...)
	return Float64Histogram{current}
}

// Record records v.
func (h Float64Histogram) Record(ctx context.Context, v float64, opts ...metric.RecordOption) {
	h.current.Record(ctx, v, opts...)
}
