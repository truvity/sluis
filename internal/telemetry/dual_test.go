package telemetry_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/truvity/sluis/internal/telemetry"
)

func TestCurrentNameChangesOnlyThePrefix(t *testing.T) {
	for legacy, want := range map[string]string{
		"access_issuer.http.requests":                      "sluis.http.requests",
		"access_issuer.signing_key.active_since_timestamp": "sluis.signing_key.active_since_timestamp",
		"access_roster.ticks":                              "sluis.ticks",
		"access_roster.port.operation.duration":            "sluis.port.operation.duration",
		"github_roster.passes":                             "github_roster.passes",
	} {
		if got := telemetry.CurrentName(legacy); got != want {
			t.Errorf("CurrentName(%q) = %q, want %q", legacy, got, want)
		}
	}
}

// collect returns, per metric name, the attribute sets recorded.
func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string][]attribute.Set {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string][]attribute.Set{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					out[m.Name] = append(out[m.Name], p.Attributes)
				}
			case metricdata.Gauge[int64]:
				for _, p := range d.DataPoints {
					out[m.Name] = append(out[m.Name], p.Attributes)
				}
			case metricdata.Histogram[float64]:
				for _, p := range d.DataPoints {
					out[m.Name] = append(out[m.Name], p.Attributes)
				}
			}
		}
	}
	return out
}

func TestEveryInstrumentRecordsUnderBothNamesWhileLegacyIsOn(t *testing.T) {
	prev := telemetry.SetLegacyMetrics(true)
	t.Cleanup(func() { telemetry.SetLegacyMetrics(prev) })
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	ctx := context.Background()
	at := metric.WithAttributes(attribute.String("kind", "k"))

	telemetry.NewInt64Counter(meter, "access_issuer.c").Add(ctx, 1, at)
	telemetry.NewInt64UpDownCounter(meter, "access_roster.u").Add(ctx, 1, at)
	telemetry.NewInt64Gauge(meter, "access_roster.g").Record(ctx, 7, at)
	telemetry.NewFloat64Histogram(meter, "access_roster.h", metric.WithUnit("s")).Record(ctx, 0.5, at)

	got := collect(t, reader)
	for _, name := range []string{"access_issuer.c", "sluis.c", "access_roster.u", "sluis.u", "access_roster.g", "sluis.g", "access_roster.h", "sluis.h"} {
		sets := got[name]
		if len(sets) != 1 {
			t.Fatalf("%s: %d series, want 1 (all: %v)", name, len(sets), got)
		}
		if v, _ := sets[0].Value("kind"); v.AsString() != "k" || sets[0].Len() != 1 {
			t.Errorf("%s: attributes %v, want kind=k only", name, sets[0])
		}
	}
}

func TestLegacyOffRecordsOnlyTheSluisNames(t *testing.T) {
	prev := telemetry.SetLegacyMetrics(false)
	t.Cleanup(func() { telemetry.SetLegacyMetrics(prev) })
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	telemetry.NewInt64Counter(meter, "access_issuer.c").Add(context.Background(), 1)

	got := collect(t, reader)
	if len(got["sluis.c"]) != 1 {
		t.Errorf("sluis.c: %v", got)
	}
	if _, old := got["access_issuer.c"]; old {
		t.Errorf("the old name is recorded with legacy off: %v", got)
	}
}
