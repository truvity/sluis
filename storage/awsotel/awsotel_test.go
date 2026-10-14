package awsotel_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/truvity/sluis/storage/awsotel"
)

// A call through a client built with Providers is a span under the caller's
// span, and a client metric, on the global providers.
func TestAClientCallIsASpanUnderTheCallersSpanAndAMetric(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prevT, prevM := otel.GetTracerProvider(), otel.GetMeterProvider()
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetTracerProvider(prevT); otel.SetMeterProvider(prevM) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = w.Write([]byte(`{"Parameter":{"Name":"n","Value":"v"}}`))
	}))
	defer srv.Close()

	client := ssm.NewFromConfig(aws.Config{
		Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("k", "s", ""),
	}, func(o *ssm.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.TracerProvider, o.MeterProvider = awsotel.Providers()
	})

	ctx, parent := otel.Tracer("test").Start(context.Background(), "parent")
	if _, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String("n")}); err != nil {
		t.Fatal(err)
	}
	parent.End()

	var op *tracetest.SpanStub
	all := spans.GetSpans()
	for i := range all {
		if all[i].Name == "SSM.GetParameter" {
			op = &all[i]
		}
	}
	if op == nil {
		t.Fatalf("no SSM.GetParameter span among %d spans", len(all))
	}
	if op.Parent.SpanID() != parent.SpanContext().SpanID() || op.SpanContext.TraceID() != parent.SpanContext().TraceID() {
		t.Errorf("the client span is not a child of the caller's span")
	}
	children := 0
	for i := range all {
		if all[i].Parent.SpanID() == op.SpanContext.SpanID() {
			children++
		}
	}
	if children == 0 {
		t.Errorf("the operation span has no child spans (serialize, sign, send, ...)")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, sm := range rm.ScopeMetrics {
		n += len(sm.Metrics)
	}
	if n == 0 {
		t.Errorf("no client metric was recorded")
	}
}
