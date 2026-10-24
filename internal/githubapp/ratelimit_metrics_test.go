package githubapp_test

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/githubfake"
)

var (
	meterOnce   sync.Once
	meterReader *sdkmetric.ManualReader
)

// waitCount is the value of the rate-limit wait counter for one kind, and
// whether the series exists at all: a counter that was never added to is absent
// from the store, which is what the dashboard's panel cannot tell from a quiet
// day.
func waitCount(t *testing.T, kind string) (int64, bool) {
	t.Helper()
	meterOnce.Do(func() {
		meterReader = sdkmetric.NewManualReader()
		otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(meterReader)))
	})
	var rm metricdata.ResourceMetrics
	if err := meterReader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	want := attribute.NewSet(attribute.String("kind", kind))
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if m.Name != "github_roster.rate_limited" || !ok {
				continue
			}
			for _, p := range sum.DataPoints {
				if p.Attributes.Equals(&want) {
					return p.Value, true
				}
			}
		}
	}
	return 0, false
}

// The wait counter has a series at zero for every kind after the first call,
// before GitHub has limited anything, and counts a wait on top of it.
func TestTheWaitCounterExistsAtZeroBeforeTheFirstWait(t *testing.T) {
	waitCount(t, "primary") // installs the reader before the first call
	fake := githubfake.Start(t, "globex")
	slept(t)
	fake.AddMember("ada", false)
	org := githubapp.Org{HTTP: fake.Client(), Login: "globex"}
	if _, err := org.Teams(context.Background(), fake.Token); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"retry_after", "primary", "secondary"} {
		if got, ok := waitCount(t, kind); !ok || got != 0 {
			t.Errorf("waits{kind=%q} = %d, present %v, want a series at 0 after an unlimited call", kind, got, ok)
		}
	}

	fake.Rejections = append(fake.Rejections, reject("/orgs/globex/teams", 403,
		`{"message":"You have exceeded a secondary rate limit."}`, nil))
	if _, err := org.Teams(context.Background(), fake.Token); err != nil {
		t.Fatal(err)
	}
	if got, _ := waitCount(t, "secondary"); got != 1 {
		t.Errorf("waits{kind=secondary} = %d after one secondary limit, want 1", got)
	}
	if got, _ := waitCount(t, "primary"); got != 0 {
		t.Errorf("waits{kind=primary} = %d, want 0", got)
	}
}
