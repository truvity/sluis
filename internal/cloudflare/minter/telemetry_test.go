package minter_test

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare/minter"
)

// A preset the caller names but the policy does not declare is counted under
// the fixed label `unknown`: the name is the caller's to choose, so it must not
// be able to mint a series.
func TestAnUnknownPresetIsOneMetricLabel(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	e := setup(t)
	person := minter.Caller{Actor: audit.Person("x@example.com"), Groups: []string{"ops"}}
	for _, name := range []string{"caller-chosen-1", "caller-chosen-2"} {
		if _, err := e.m.MintFor(ctx, name, person, time.Hour); err == nil {
			t.Fatalf("%s was minted", name)
		}
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok || m.Name != "sluis.cloudflare.tokens.minted" {
				continue
			}
			for _, dp := range sum.DataPoints {
				v, _ := dp.Attributes.Value("preset")
				if got := v.AsString(); got != "unknown" {
					t.Errorf("preset label %q, want unknown", got)
				}
				if dp.Value != 2 {
					t.Errorf("count %d, want 2", dp.Value)
				}
				seen++
			}
		}
	}
	if seen != 1 {
		t.Errorf("%d series, want 1", seen)
	}
}
