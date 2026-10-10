package telemetry

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/otel/metric"
)

// LegacyMetricsEnv is the environment variable that turns the old metric names
// off. The chart sets it from `telemetry.legacyMetrics`.
const LegacyMetricsEnv = "SLUIS_LEGACY_METRICS"

// The two prefixes the product's metrics were published under before the
// rename, and the prefix they have now. During the dual-name window every
// instrument that had an old name is recorded under both.
const (
	legacyIssuerPrefix = "access_issuer."
	legacyRosterPrefix = "access_roster."
	currentPrefix      = "sluis."
)

var legacyMetrics atomic.Bool

func init() {
	// On by default in v1.75: a dashboard or an alert that still selects the old
	// names keeps working. An unreadable value leaves it on.
	legacyMetrics.Store(true)
	if v, ok := os.LookupEnv(LegacyMetricsEnv); ok {
		if on, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			legacyMetrics.Store(on)
		}
	}
}

// LegacyMetrics reports whether the old metric names are still recorded.
func LegacyMetrics() bool { return legacyMetrics.Load() }

// SetLegacyMetrics turns the old names on or off and returns the previous
// setting. It is read at every record, so it may be set after the instruments
// exist; a process sets it once, at start.
func SetLegacyMetrics(on bool) (previous bool) { return legacyMetrics.Swap(on) }

// CurrentName is the `sluis.` name of an instrument that is still created under
// its old name: `access_issuer.http.requests` and `access_roster.ticks` become
// `sluis.http.requests` and `sluis.ticks`. A name with neither prefix is
// returned as it is. Only the prefix changes, so the attributes, the unit and
// the type of an instrument, and with them its cardinality, are the same under
// both names.
func CurrentName(legacy string) string {
	for _, p := range []string{legacyIssuerPrefix, legacyRosterPrefix} {
		if rest, ok := strings.CutPrefix(legacy, p); ok {
			return currentPrefix + rest
		}
	}
	return legacy
}

// Int64Counter records into the `sluis.` counter and, while [LegacyMetrics] is
// on, into the counter of the old name.
type Int64Counter struct{ current, legacy metric.Int64Counter }

// NewInt64Counter creates both counters from the old name.
func NewInt64Counter(meter metric.Meter, legacyName string, opts ...metric.Int64CounterOption) Int64Counter {
	// Instrument creation fails only on an invalid name, which these are not; a
	// failed one is a no-op instrument, never a stopped service.
	current, _ := meter.Int64Counter(CurrentName(legacyName), opts...)
	legacy, _ := meter.Int64Counter(legacyName, opts...)
	return Int64Counter{current, legacy}
}

// Add records n under both names.
func (c Int64Counter) Add(ctx context.Context, n int64, opts ...metric.AddOption) {
	c.current.Add(ctx, n, opts...)
	if LegacyMetrics() {
		c.legacy.Add(ctx, n, opts...)
	}
}

// Int64UpDownCounter is [Int64Counter] for a counter that goes down.
type Int64UpDownCounter struct{ current, legacy metric.Int64UpDownCounter }

// NewInt64UpDownCounter creates both counters from the old name.
func NewInt64UpDownCounter(meter metric.Meter, legacyName string, opts ...metric.Int64UpDownCounterOption) Int64UpDownCounter {
	current, _ := meter.Int64UpDownCounter(CurrentName(legacyName), opts...)
	legacy, _ := meter.Int64UpDownCounter(legacyName, opts...)
	return Int64UpDownCounter{current, legacy}
}

// Add records n under both names.
func (c Int64UpDownCounter) Add(ctx context.Context, n int64, opts ...metric.AddOption) {
	c.current.Add(ctx, n, opts...)
	if LegacyMetrics() {
		c.legacy.Add(ctx, n, opts...)
	}
}

// Int64Gauge records into the `sluis.` gauge and, while [LegacyMetrics] is on,
// into the gauge of the old name.
type Int64Gauge struct{ current, legacy metric.Int64Gauge }

// NewInt64Gauge creates both gauges from the old name.
func NewInt64Gauge(meter metric.Meter, legacyName string, opts ...metric.Int64GaugeOption) Int64Gauge {
	current, _ := meter.Int64Gauge(CurrentName(legacyName), opts...)
	legacy, _ := meter.Int64Gauge(legacyName, opts...)
	return Int64Gauge{current, legacy}
}

// Record records v under both names.
func (g Int64Gauge) Record(ctx context.Context, v int64, opts ...metric.RecordOption) {
	g.current.Record(ctx, v, opts...)
	if LegacyMetrics() {
		g.legacy.Record(ctx, v, opts...)
	}
}

// Float64Histogram records into the `sluis.` histogram and, while
// [LegacyMetrics] is on, into the histogram of the old name.
type Float64Histogram struct{ current, legacy metric.Float64Histogram }

// NewFloat64Histogram creates both histograms from the old name.
func NewFloat64Histogram(meter metric.Meter, legacyName string, opts ...metric.Float64HistogramOption) Float64Histogram {
	current, _ := meter.Float64Histogram(CurrentName(legacyName), opts...)
	legacy, _ := meter.Float64Histogram(legacyName, opts...)
	return Float64Histogram{current, legacy}
}

// Record records v under both names.
func (h Float64Histogram) Record(ctx context.Context, v float64, opts ...metric.RecordOption) {
	h.current.Record(ctx, v, opts...)
	if LegacyMetrics() {
		h.legacy.Record(ctx, v, opts...)
	}
}
