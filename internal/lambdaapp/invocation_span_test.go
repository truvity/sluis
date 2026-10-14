package lambdaapp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/lambdacontext"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/telemetry"
)

// The server span of a module call on Lambda continues the caller's
// traceparent and carries the Lambda request id as faas.invocation_id, through
// the exporter's allowlist.
func TestTheInvocationSpanCarriesTheRequestIDAndContinuesTheTrace(t *testing.T) {
	memory := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.FilterExporter(memory)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	payload, err := json.Marshal(modcall.Request{
		V: 2, Kind: modcall.Kind, Module: "cloudflare", Method: "echo", Payload: json.RawMessage(`{"say":"hi"}`),
		Traceparent: "00-" + traceID + "-00f067aa0ba902b7-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{AwsRequestID: "req-1234"})
	h := lambdaapp.NewHTTP(http.NotFoundHandler(), nil, nil).WithRPC(echoServer())
	if _, err = h.Handle(ctx, payload); err != nil {
		t.Fatal(err)
	}

	spans := memory.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("want one server span, got %d", len(spans))
	}
	s := spans[0]
	if s.SpanKind != trace.SpanKindServer {
		t.Errorf("span kind %v", s.SpanKind)
	}
	if got := s.SpanContext.TraceID().String(); got != traceID {
		t.Errorf("trace id %s: the caller's traceparent was not continued", got)
	}
	var id string
	for _, kv := range s.Attributes {
		if string(kv.Key) == telemetry.AttrFaaSInvocationID {
			id = kv.Value.AsString()
		}
	}
	if id != "req-1234" {
		t.Errorf("faas.invocation_id = %q", id)
	}
}
