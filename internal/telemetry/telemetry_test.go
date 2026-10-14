package telemetry_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"

	"github.com/truvity/sluis/internal/telemetry"
)

// With no collector named nothing is exported, and starting and stopping
// costs nothing and fails nothing.
func TestNoCollectorExportsNothing(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	if telemetry.Enabled() {
		t.Fatal("enabled with no collector named")
	}
	shutdown, err := telemetry.Start(context.Background(), "test", slog.New(slog.DiscardHandler))
	if err != nil || shutdown(context.Background()) != nil {
		t.Errorf("Start = %v", err)
	}
}

// A named collector starts the exporter; nothing is sent until the first
// interval, so a collector that is not there yet stops nothing.
func TestANamedCollectorStartsTheExporter(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	if !telemetry.Enabled() {
		t.Fatal("not enabled with a collector named")
	}
	shutdown, err := telemetry.Start(context.Background(), "test", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = shutdown(ctx)
}

// With no sampler named, a root trace is kept (parent based, always_on); naming
// one in the environment replaces that without a release.
func TestTheDefaultSamplerKeepsRootTracesAndTheEnvironmentOverridesIt(t *testing.T) {
	for _, c := range []struct {
		name, sampler, arg string
		sampled            bool
		// childOfSampled: a span whose remote parent is sampled.
		childOfSampled bool
	}{
		{"default", "", "", true, true},
		{"always_on", "always_on", "", true, true},
		{"always_off", "always_off", "", false, false},
		{"parentbased_always_on", "parentbased_always_on", "", true, true},
		{"parentbased_always_off", "parentbased_always_off", "", false, true},
		{"parentbased_traceidratio 0", "parentbased_traceidratio", "0", false, true},
		{"parentbased_traceidratio 1", "parentbased_traceidratio", "1", true, true},
		{"traceidratio 0", "traceidratio", "0", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:1")
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
			t.Setenv("OTEL_TRACES_SAMPLER", c.sampler)
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", c.arg)
			if !telemetry.TracesEnabled() {
				t.Fatal("traces are not enabled with a collector named")
			}
			previous := otel.GetTracerProvider()
			shutdown, err := telemetry.Start(context.Background(), "test", slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer func() {
				cancelled, cancel := context.WithCancel(context.Background())
				cancel()
				_ = shutdown(cancelled)
				otel.SetTracerProvider(previous)
			}()
			_, span := telemetry.Tracer().Start(context.Background(), "root")
			defer span.End()
			if got := span.SpanContext().IsSampled(); got != c.sampled {
				t.Errorf("sampled = %v, want %v", got, c.sampled)
			}
			remote := telemetry.ExtractWire(context.Background(), "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "")
			_, child := telemetry.Tracer().Start(remote, "child")
			defer child.End()
			if got := child.SpanContext().IsSampled(); got != c.childOfSampled {
				t.Errorf("a child of a sampled remote parent: sampled = %v, want %v", got, c.childOfSampled)
			}
		})
	}
}

// No collector named for traces: the tracer is the no-op, and a metrics-only
// collector starts no trace exporter.
func TestTracesNeedTheirOwnCollector(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://127.0.0.1:1")
	if telemetry.TracesEnabled() || !telemetry.Enabled() {
		t.Fatalf("traces %v, metrics %v", telemetry.TracesEnabled(), telemetry.Enabled())
	}
}

func attrs(t *testing.T) map[string]string {
	t.Helper()
	res, err := telemetry.Resource("svc", "v1")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, kv := range res.Attributes() {
		out[string(kv.Key)] = kv.Value.String()
	}
	return out
}

// Two concurrent processes (Lambda execution environments) must not export the
// same series: the resource names this one, and keeps the name for its life.
func TestTheResourceNamesTheInstance(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "")
	a := attrs(t)
	if _, err := uuid.Parse(a["service.instance.id"]); err != nil {
		t.Errorf("service.instance.id %q: %v", a["service.instance.id"], err)
	}
	if a["service.instance.id"] != attrs(t)["service.instance.id"] {
		t.Error("the instance id changed within a process")
	}
	if a["service.name"] != "svc" || a["service.version"] != "v1" {
		t.Errorf("service attributes %v", a)
	}
	for _, k := range []string{"faas.name", "faas.instance", "cloud.provider", "cloud.region"} {
		if _, ok := a[k]; ok {
			t.Errorf("%s set outside Lambda", k)
		}
	}
}

func TestTheResourceNamesTheLambdaEnvironment(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "example-fn")
	t.Setenv("AWS_LAMBDA_LOG_STREAM_NAME", "2026/10/09/[$LATEST]abc123")
	t.Setenv("AWS_REGION", "eu-west-1")
	a := attrs(t)
	want := map[string]string{
		"faas.name": "example-fn", "faas.instance": "2026/10/09/[$LATEST]abc123",
		"cloud.provider": "aws", "cloud.region": "eu-west-1",
	}
	for k, v := range want {
		if a[k] != v {
			t.Errorf("%s = %q, want %q", k, a[k], v)
		}
	}
}
