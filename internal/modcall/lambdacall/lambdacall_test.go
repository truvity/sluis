package lambdacall_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/lambdacall"
)

// fn is a function: Invoke hands the event to Serve, as the platform does.
type fn struct {
	server *modcall.Server
	got    *awslambda.InvokeInput
	crash  string
}

func (f *fn) Invoke(ctx context.Context, in *awslambda.InvokeInput, _ ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
	f.got = in
	if f.crash != "" {
		return &awslambda.InvokeOutput{StatusCode: 200, FunctionError: &f.crash, Payload: []byte(`{"errorMessage":"x"}`)}, nil
	}
	out, err := lambdacall.Serve(ctx, f.server, in.Payload, *in.Qualifier)
	return &awslambda.InvokeOutput{StatusCode: 200, Payload: out}, err
}

type req struct{ N int }
type res struct {
	N       int
	Subject string
}

func setup() (*fn, *lambdacall.Caller) {
	s := modcall.NewServer("calc")
	modcall.Handle(s, "double", func(ctx context.Context, r req) (res, error) {
		return res{N: r.N * 2, Subject: modcall.CallerOf(ctx)}, nil
	})
	modcall.Handle(s, "refuse", func(context.Context, req) (res, error) { return res{}, modcall.Coded("no", "") })
	f := &fn{server: s}
	return f, lambdacall.New(f, modcall.Config{Modules: map[string]modcall.Target{"calc": {Function: "arn:calc"}}}).As("issuer")
}

func TestACallInvokesTheCallersAliasSynchronously(t *testing.T) {
	f, c := setup()
	got, err := modcall.Do[req, res](context.Background(), c, "calc", "double", req{N: 21})
	if err != nil || got.N != 42 || got.Subject != "issuer" {
		t.Fatalf("%+v, %v", got, err)
	}
	if *f.got.FunctionName != "arn:calc" || *f.got.Qualifier != "live-issuer" || f.got.InvocationType != types.InvocationTypeRequestResponse {
		t.Fatalf("%+v", f.got)
	}
}

func TestACodedErrorCrossesAndAFunctionErrorIsATransportFailure(t *testing.T) {
	f, c := setup()
	if _, err := modcall.Do[req, res](context.Background(), c, "calc", "refuse", req{}); !errors.Is(err, modcall.Coded("no", "")) {
		t.Fatalf("%v", err)
	}
	f.crash = "Unhandled"
	if _, err := c.Call(context.Background(), "calc", "double", nil); !errors.Is(err, modcall.ErrTransport) {
		t.Fatalf("%v", err)
	}
	if _, err := c.Call(context.Background(), "other", "double", nil); !errors.Is(err, modcall.ErrNoRoute) {
		t.Fatalf("%v", err)
	}
}

func TestServeLeavesAnEventThatIsNotACall(t *testing.T) {
	s := modcall.NewServer("calc")
	if _, err := lambdacall.Serve(context.Background(), s, []byte(`{"kind":"tick"}`), "live-issuer"); !errors.Is(err, lambdacall.ErrNotACall) {
		t.Fatalf("%v", err)
	}
}

func TestTheAliasIsTheCallerClass(t *testing.T) {
	for alias, want := range map[string]string{
		"live-issuer": "issuer", "live-console": "console", "live": "", "": "", "3": "", "$LATEST": "", "other-issuer": "",
	} {
		if got := lambdacall.ClassOf(alias); got != want {
			t.Errorf("ClassOf(%q) = %q, want %q", alias, got, want)
		}
	}
	if lambdacall.AliasOf("console") != "live-console" || lambdacall.AliasOf("") != "live" {
		t.Error("AliasOf")
	}
}

func TestAMethodAnswersOnlyTheAliasesItAllows(t *testing.T) {
	s := modcall.NewServer("calc")
	modcall.Handle(s, "double", func(_ context.Context, r req) (res, error) { return res{N: r.N * 2}, nil }, modcall.Allow("issuer"))
	f := &fn{server: s}
	c := lambdacall.New(f, modcall.Config{Modules: map[string]modcall.Target{"calc": {Function: "arn:calc"}}})
	if _, err := modcall.Do[req, res](context.Background(), c.As("issuer"), "calc", "double", req{N: 1}); err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"console", ""} {
		_, err := modcall.Do[req, res](context.Background(), c.As(class), "calc", "double", req{N: 1})
		if !errors.Is(err, modcall.Coded(modcall.CodeForbidden, "")) {
			t.Errorf("class %q: %v", class, err)
		}
	}
}

func TestAClassWrittenInTheEventIsIgnored(t *testing.T) {
	s := modcall.NewServer("calc")
	modcall.Handle(s, "who", func(ctx context.Context, _ req) (res, error) { return res{Subject: modcall.CallerOf(ctx)}, nil })
	out, err := lambdacall.Serve(context.Background(), s,
		[]byte(`{"v":2,"kind":"rpc","module":"calc","method":"who","caller":"issuer","payload":{"caller":"issuer"}}`), "live-console")
	if err != nil || !strings.Contains(string(out), `"Subject":"console"`) {
		t.Fatalf("%s %v", out, err)
	}
}

func TestTheTraceContinuesIntoTheFunction(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) }()

	_, c := setup()
	if _, err := modcall.Do[req, res](context.Background(), c, "calc", "double", req{N: 1}); err != nil {
		t.Fatal(err)
	}
	var client, srv sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.SpanKind() == trace.SpanKindClient {
			client = s
		} else if s.SpanKind() == trace.SpanKindServer {
			srv = s
		}
	}
	if client == nil || srv == nil || srv.SpanContext().TraceID() != client.SpanContext().TraceID() ||
		srv.Parent().SpanID() != client.SpanContext().SpanID() {
		t.Fatalf("the function's span does not continue the caller's trace: %v %v", client, srv)
	}
}

func TestSizeGuardsAndTheDeadlineHoldOnLambda(t *testing.T) {
	f, c := setup()
	big := strings.Repeat("x", modcall.DefaultMaxBytes)
	var e *modcall.Error
	// The caller refuses before invoking.
	f.got = nil
	_, err := c.Call(context.Background(), "calc", "double", []byte(`{"N":1,"Pad":"`+big+`"}`))
	if !errors.As(err, &e) || e.Code != modcall.CodeBadRequest || f.got != nil {
		t.Fatalf("%v invoked=%v", err, f.got != nil)
	}
	// The function refuses a payload that got past a caller with a higher bound.
	c.MaxBytes = modcall.HardMaxBytes
	_, err = c.Call(context.Background(), "calc", "double", []byte(`{"N":1,"Pad":"`+big+`"}`))
	if !errors.As(err, &e) || e.Code != modcall.CodeBadRequest || strings.Contains(err.Error(), "xxx") {
		t.Fatalf("%v", err)
	}
	// An expired deadline never invokes.
	f.got = nil
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err = c.Call(ctx, "calc", "double", nil); !errors.Is(err, modcall.Coded(modcall.CodeDeadlineExceeded, "")) || f.got != nil {
		t.Fatalf("%v", err)
	}
	// A deadline that passed in flight is refused by the function.
	out, err := lambdacall.Serve(context.Background(), f.server,
		[]byte(`{"v":2,"kind":"rpc","module":"calc","method":"double","deadline":1}`), "live-issuer")
	if err != nil || !strings.Contains(string(out), modcall.CodeDeadlineExceeded) {
		t.Fatalf("%s %v", out, err)
	}
}

func TestAV1EventIsAnsweredInV1(t *testing.T) {
	f, _ := setup()
	out, err := lambdacall.Serve(context.Background(), f.server,
		[]byte(`{"kind":"rpc","module":"calc","method":"double","payload":{"N":2}}`), "live-issuer")
	if err != nil || strings.Contains(string(out), `"v"`) || !strings.Contains(string(out), `"N":4`) {
		t.Fatalf("%s %v", out, err)
	}
}
