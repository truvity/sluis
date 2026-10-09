package telemetry_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/sluis/audit/internal/telemetry"
	sdktelemetry "github.com/truvity/sluis/audit/sdk/telemetry"
)

// The alert a deployment needs is on objects the indexer did not take, labelled
// by profile and by whether a retry will help.
func TestObserveCountsWhatItDidNotIndex(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	o, err := telemetry.NewObserve(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	o.Deferred("security", true)
	o.Deferred("security", false)
	o.Deferred("security", false)
	o.Deferred("history", false)

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	deferred := map[string]int64{}
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "audit.observe.index.deferred" {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				profile, _ := p.Attributes.Value("profile")
				reason, _ := p.Attributes.Value("reason")
				deferred[profile.AsString()+"/"+reason.AsString()] += p.Value
			}
		}
	}
	if deferred["security/unreadable"] != 1 || deferred["security/retry"] != 2 || deferred["history/retry"] != 1 {
		t.Fatalf("deferred objects by profile and reason: %v", deferred)
	}
}

func TestProfileOf(t *testing.T) {
	for key, want := range map[string]string{
		"records/security/acme/2026/10/03/12/01ARZ3NDEKTSV4RRFFQ69G5FAV":   "security",
		"records/billing-nl/acme/2026/10/03/12/01ARZ3NDEKTSV4RRFFQ69G5FAV": "billing-nl",
		"holds/h-1/placed.json": "",
		"records/":              "",
	} {
		if got := telemetry.ProfileOf(key); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
}

func TestTracesAreOffUnlessACollectorIsNamed(t *testing.T) {
	for _, name := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"} {
		t.Setenv(name, "")
	}
	if telemetry.TracesEnabled() || telemetry.Enabled() {
		t.Fatal("telemetry is on with no collector named")
	}
	stop, err := telemetry.Start(context.Background(), "audit-test", "v0", slog.Default())
	if err != nil || stop(context.Background()) != nil {
		t.Fatalf("a no-op start failed: %v", err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318")
	if !telemetry.TracesEnabled() || telemetry.Enabled() {
		t.Fatal("the traces endpoint must enable traces and not metrics")
	}
}

// Whatever a span is given, only the allowlist leaves the process: a client
// address, an error's message in an event, a status text, a link's attributes.
func TestTheExporterDropsWhatIsNotAllowlisted(t *testing.T) {
	memory := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.FilterExporter(memory)))
	_, span := tp.Tracer("t").Start(context.Background(), "write", trace.WithAttributes(
		attribute.String("audit.action", "shop.order.placed"),
		attribute.String("audit.tenant.id", "acme"),
		attribute.String("client.address", "203.0.113.9"),
		attribute.String("audit.actor", "alice"),
		attribute.String("http.request.method", "POST"),
		attribute.String("net.peer.ip", "203.0.113.9"),
	))
	span.RecordError(errors.New("could not write the record of alice"))
	span.SetStatus(codes.Error, "alice")
	span.End()

	out := memory.GetSpans()
	if len(out) != 1 {
		t.Fatalf("%d spans exported", len(out))
	}
	for _, kv := range out[0].Attributes {
		if !telemetry.SpanAttributeAllowlist[kv.Key] {
			t.Errorf("%s left the process", kv.Key)
		}
	}
	if len(out[0].Attributes) != 3 {
		t.Errorf("attributes %v, want the three allowed", out[0].Attributes)
	}
	if len(out[0].Events) != 0 || out[0].Status.Description != "" || out[0].Status.Code != codes.Error {
		t.Errorf("events %v, status %+v", out[0].Events, out[0].Status)
	}
}

func TestTheIndexLagIsRecordedPerProfile(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	o, err := telemetry.NewObserve(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	o.Indexed("security", 3, 150*time.Second)
	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	for _, sc := range got.ScopeMetrics {
		for _, m := range sc.Metrics {
			if m.Name != "audit.observe.index.lag" {
				continue
			}
			p := m.Data.(metricdata.Histogram[float64]).DataPoints[0]
			if profile, _ := p.Attributes.Value("profile"); profile.AsString() != "security" || p.Count != 1 || p.Sum != 150 {
				t.Fatalf("%+v", p)
			}
			return
		}
	}
	t.Fatal("no audit.observe.index.lag")
}

func TestTheDefaultSamplerKeepsEveryTrace(t *testing.T) {
	sampler := telemetry.DefaultSampler()
	want := "ParentBased{root:AlwaysOnSampler," +
		"remoteParentSampled:AlwaysOnSampler,remoteParentNotSampled:AlwaysOffSampler," +
		"localParentSampled:AlwaysOnSampler,localParentNotSampled:AlwaysOffSampler}"
	if got := sampler.Description(); got != want {
		t.Fatalf("default sampler = %s, want %s", got, want)
	}
}

// The queue's age is a histogram per transport, in seconds, and a message
// whose sender's clock ran ahead is counted at zero and not dropped.
func TestQueueRecordsTheAgeOfAMessageAtReceive(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	q, err := telemetry.NewQueue(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	q.Received("sqs", 12*time.Second)
	q.Received("sqs", -3*time.Second)

	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	var count uint64
	var sum float64
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "audit.queue.message.age" {
				continue
			}
			for _, p := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				if v, _ := p.Attributes.Value(telemetry.AttrTransport); v.AsString() != "sqs" {
					t.Fatalf("transport = %q", v.AsString())
				}
				count += p.Count
				sum += p.Sum
			}
		}
	}
	if count != 2 || sum != 12 {
		t.Fatalf("count %d, sum %v; want 2 messages totalling 12s", count, sum)
	}
}

// An unregistered catalogue version is counted by the source and version the
// record names, and what an emitter can make up is bounded: past a few pairs the
// rest is "other", so one that sends anything cannot make a series each.
func TestUnknownCatalogueIsCountedByNameAndBoundedInLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	w, err := telemetry.NewWriter(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	w.UnknownCatalogue("wallet", "2.0.0")
	w.UnknownCatalogue("wallet", "2.0.0")
	for i := 0; i < 100; i++ {
		w.UnknownCatalogue("made-up", fmt.Sprintf("%d.0.0", i))
	}
	var got metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	counts, series := map[string]int64{}, 0
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "audit.writer.catalogue.unknown" {
				continue
			}
			for _, p := range m.Data.(metricdata.Sum[int64]).DataPoints {
				source, _ := p.Attributes.Value("source")
				version, _ := p.Attributes.Value("catalogue_version")
				counts[source.AsString()+"@"+version.AsString()] += p.Value
				series++
			}
		}
	}
	if counts["wallet@2.0.0"] != 2 {
		t.Errorf("counts = %v", counts)
	}
	if series > 21 || counts["other@other"] == 0 {
		t.Errorf("%d series, other = %d: the labels are not bounded", series, counts["other@other"])
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
		out[string(kv.Key)] = kv.Value.Emit()
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

type jsonCodec struct{}

func (jsonCodec) Name() string                    { return "json" }
func (jsonCodec) Marshal(any) ([]byte, error)     { return []byte("{}"), nil }
func (jsonCodec) Unmarshal(_ []byte, _ any) error { return nil }

// A span from an otelconnect call (v0.10 sets rpc.system.name, rpc.method,
// rpc.response.status_code and, on a failure, error.type) keeps them through the
// exporter's allowlist, and loses the error's message.
func TestAConnectSpanKeepsItsRPCAttributes(t *testing.T) {
	memory := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.FilterExporter(memory)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })

	mux := http.NewServeMux()
	mux.Handle("/pkg.Service/Method", connect.NewUnaryHandler("/pkg.Service/Method",
		func(context.Context, *connect.Request[struct{}]) (*connect.Response[struct{}], error) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("alice@example.com is not allowed"))
		}, append([]connect.HandlerOption{connect.WithCodec(jsonCodec{})}, sdktelemetry.ConnectOptions()...)...))
	server := httptest.NewServer(mux)
	defer server.Close()
	client := connect.NewClient[struct{}, struct{}](server.Client(), server.URL+"/pkg.Service/Method",
		append([]connect.ClientOption{connect.WithCodec(jsonCodec{})}, sdktelemetry.ConnectClientOptions()...)...)
	if _, err := client.CallUnary(context.Background(), connect.NewRequest(&struct{}{})); err == nil {
		t.Fatal("the handler's refusal did not arrive")
	}

	spans := memory.GetSpans()
	if len(spans) == 0 {
		t.Fatal("no span exported")
	}
	for i := range spans {
		got := map[string]string{}
		for _, kv := range spans[i].Attributes {
			got[string(kv.Key)] = kv.Value.Emit()
		}
		for k, v := range map[string]string{
			"rpc.system.name": "connectrpc", "rpc.method": "pkg.Service/Method",
			"rpc.response.status_code": "PERMISSION_DENIED", "error.type": "PERMISSION_DENIED",
		} {
			if got[k] != v {
				t.Errorf("span %q: %s = %q, want %q (attributes %v)", spans[i].Name, k, got[k], v, got)
			}
		}
		if strings.Contains(fmt.Sprint(spans[i]), "alice@example.com") {
			t.Errorf("the error's message left the process")
		}
	}
}
