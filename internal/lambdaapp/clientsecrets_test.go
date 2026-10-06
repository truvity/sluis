package lambdaapp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/lambdaapp"
)

// reconciled is the count of the reconcile metric for an outcome.
func reconciled(t *testing.T, reader *sdkmetric.ManualReader, outcome string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := attribute.NewSet(attribute.String("outcome", outcome))
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if m.Name != "sluis.client_secret.reconcile" || !ok {
				continue
			}
			for _, p := range sum.DataPoints {
				if p.Attributes.Equals(&want) {
					return p.Value
				}
			}
		}
	}
	return 0
}

// The function has no loop, so the directory refresh the schedule invokes is
// also when a generated client's secret is looked after.
func TestARefreshEventReconcilesTheGeneratedClientSecrets(t *testing.T) {
	// The only test of this binary that installs a meter provider: the
	// reconcile instruments are made at start-up and delegate to the first.
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(`
version: 1
groups:
  all:access-roster:operator: { members: [platform@north.example] }
  all:access-roster:viewer: {}
lifetimes: { default: 12h }
clients:
  console: { kind: public, requires: [all:access-roster:operator], redirects: ["https://access.example/console/callback"] }
  grafana: { kind: confidential, secret: { generate: true }, requires: [all:access-roster:operator] }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "sluis.yaml")
	if err := os.WriteFile(file, []byte(`
issuerURL: https://access.example
publicURL: https://access.example/console
policyDir: `+dir+`
listen: {address: ":0"}
probes: {address: ":0"}
adapters:
  secrets: {adapter: memory}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	fn, err := lambdaapp.Open(context.Background(), func(k string) string {
		if k == config.EnvConfig {
			return file
		}
		return ""
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(fn.Close)

	// Open settled the secret once.
	if got := reconciled(t, reader, "created"); got != 1 {
		t.Fatalf("created after Open = %d, want 1", got)
	}
	before := reconciled(t, reader, "existing")
	for i := 1; i <= 2; i++ {
		if _, err = fn.Handler.Handle(context.Background(), json.RawMessage(`{"kind":"refresh"}`)); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		if got := reconciled(t, reader, "existing"); got != before+int64(i) {
			t.Errorf("after %d refreshes the reconcile ran %d times, want %d", i, got-before, i)
		}
	}
}
