package telemetry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	directoryv1 "github.com/truvity/sluis/gen/directory/v1"
	"github.com/truvity/sluis/gen/directory/v1/directoryv1connect"
	"github.com/truvity/sluis/internal/telemetry"
)

// markers are what no span may carry out of the process: each is planted below
// somewhere a careless call site, or a library, would put it.
var markers = []string{"alice@example.com", "grp-secret-admins", "tokenhash-9f8e7d", "203.0.113.77", "sub-1234-secret"}

func exported(t *testing.T) (*tracetest.InMemoryExporter, trace.Tracer) {
	t.Helper()
	memory := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.FilterExporter(memory)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	return memory, provider.Tracer("test")
}

func dump(memory *tracetest.InMemoryExporter) string {
	var b strings.Builder
	spans := memory.GetSpans()
	for i := range spans {
		s := &spans[i]
		fmt.Fprintf(&b, "name=%q status=%v/%q\n", s.Name, s.Status.Code, s.Status.Description)
		for _, kv := range s.Attributes {
			fmt.Fprintf(&b, "  attr %s=%s\n", kv.Key, kv.Value.String())
		}
		for _, e := range s.Events {
			fmt.Fprintf(&b, "  event %s\n", e.Name)
		}
		for _, l := range s.Links {
			for _, kv := range l.Attributes {
				fmt.Fprintf(&b, "  link %s=%s\n", kv.Key, kv.Value.String())
			}
		}
	}
	return b.String()
}

// Nothing personal leaves the process: markers planted in attributes (known
// and unknown), in an event, in a recorded error, in a status description and
// in a link are all gone from what the next exporter sees, and what the
// allowlist names survives.
func TestNoPersonalDataLeavesAnExportedSpan(t *testing.T) {
	memory, tracer := exported(t)
	_, other := tracer.Start(context.Background(), "other")
	other.End()

	_, span := tracer.Start(context.Background(), "tick",
		trace.WithLinks(trace.Link{SpanContext: other.SpanContext(), Attributes: []attribute.KeyValue{attribute.String("peer", markers[0])}}))
	span.SetAttributes(
		attribute.String(telemetry.AttrTargetKind, "github-tick"),
		attribute.String(telemetry.AttrTargetID, "truvity"),
		attribute.String("enduser.id", markers[4]),
		attribute.String("user.email", markers[0]),
		attribute.String("group", markers[1]),
		attribute.String("token.hash", markers[2]),
		attribute.String("client.address", markers[3]),
		attribute.String("url.path", "/login/"+markers[0]),
		attribute.String("http.url", "https://issuer.example/x?code="+markers[2]),
	)
	span.AddEvent("sign-in refused for "+markers[0], trace.WithAttributes(attribute.String("groups", markers[1])))
	span.RecordError(errors.New("refused " + markers[0] + " in " + markers[1]))
	span.SetStatus(codes.Error, "subject "+markers[4]+" from "+markers[3])
	span.End()

	out := dump(memory)
	for _, marker := range markers {
		if strings.Contains(out, marker) {
			t.Errorf("%q left the process:\n%s", marker, out)
		}
	}
	for _, want := range []string{"access_roster.target.kind=github-tick", "access_roster.target.id=truvity"} {
		if !strings.Contains(out, want) {
			t.Errorf("the allowlisted %s was dropped:\n%s", want, out)
		}
	}
}

// The HTTP server's own instrumentation records the raw path, the client's
// address and the user agent; the span is named for the route and carries none
// of them, and a handler's recorded error does not leave.
func TestAnHTTPSpanIsNamedForTheRouteAndCarriesNoRawPath(t *testing.T) {
	memory, _ := exported(t)
	handler := telemetry.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace.SpanFromContext(r.Context()).RecordError(errors.New("failed for " + markers[0]))
		trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("email", markers[0]))
		w.WriteHeader(http.StatusBadGateway)
	}), "test", func(*http.Request) string { return "login_callback" })

	request := httptest.NewRequest(http.MethodGet, "/login/"+markers[2]+"/callback?code="+markers[1], nil)
	request.RemoteAddr = markers[3] + ":4444"
	request.Header.Set("User-Agent", markers[4])
	handler.ServeHTTP(httptest.NewRecorder(), request)

	out := dump(memory)
	for _, marker := range markers {
		if strings.Contains(out, marker) {
			t.Errorf("%q left the process:\n%s", marker, out)
		}
	}
	for _, want := range []string{`name="GET login_callback"`, "http.route=login_callback", "http.response.status_code=502"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s:\n%s", want, out)
		}
	}
}

// A handler that learns more than the path renames the request: the span is
// named and labelled for the new route, not the one the path gave.
func TestAHandlerCanRenameItsRoute(t *testing.T) {
	memory, _ := exported(t)
	handler := telemetry.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		telemetry.SetRoute(r.Context(), "token_mint")
		w.WriteHeader(http.StatusOK)
	}), "test", func(*http.Request) string { return "token" })
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/token", nil))

	out := dump(memory)
	for _, want := range []string{`name="POST token_mint"`, "http.route=token_mint"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "http.route=token\n") || strings.Contains(out, `name="POST token"`) {
		t.Errorf("the path's route is still on the span:\n%s", out)
	}
	// Outside HTTPHandler it does nothing.
	telemetry.SetRoute(t.Context(), "token_mint")
}

// A Connect call is a server span with the RPC's own attributes, whose handler
// error message (which may quote a group) does not leave.
func TestAConnectSpanKeepsTheMethodAndDropsTheError(t *testing.T) {
	memory, _ := exported(t)
	rpc := connect.NewServer(telemetry.ConnectServerInterceptor())
	directoryv1connect.RegisterDirectoryServiceHandler(rpc, refusingDirectory{})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)
	server := httptest.NewServer(mux)
	defer server.Close()

	client := directoryv1connect.NewDirectoryServiceClient(connect.NewClient(
		connecthttp.NewTransport(server.Client(), server.URL), telemetry.ConnectClientInterceptor()))
	_, err := client.Describe(context.Background(), &directoryv1.DescribeRequest{})
	if err == nil {
		t.Fatal("the handler's refusal did not arrive")
	}

	out := dump(memory)
	for _, marker := range markers {
		if strings.Contains(out, marker) {
			t.Errorf("%q left the process:\n%s", marker, out)
		}
	}
	if !strings.Contains(out, "rpc.method=directory.v1.DirectoryService/Describe") {
		t.Errorf("the RPC's method was dropped:\n%s", out)
	}
}

// Every attribute a span may carry is one of ours or the shape of a request; a
// name nobody reviewed is not on the list.
func TestTheAllowlistHoldsNothingPersonal(t *testing.T) {
	for key := range telemetry.SpanAttributeAllowlist {
		for _, banned := range []string{"email", "user", "subject", "group", "token", "address", "path", "url", "agent"} {
			if strings.Contains(strings.ToLower(string(key)), banned) {
				t.Errorf("%s is allowlisted and reads as personal (%s)", key, banned)
			}
		}
	}
}

// refusingDirectory refuses with a message that quotes a person and a group.
type refusingDirectory struct {
	directoryv1connect.UnimplementedDirectoryServiceHandler
}

func (refusingDirectory) Describe(context.Context, *directoryv1.DescribeRequest) (*directoryv1.DescribeResponse, error) {
	return nil, connect.NewError(connect.CodePermissionDenied, markers[0]+" is not in "+markers[1])
}
