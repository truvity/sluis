package modcall_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/truvity/sluis/internal/modcall"
)

// recorder installs a tracer provider that keeps its spans.
func recorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	return rec
}

// continued asserts that the recorded server span is the child of the client
// span, in one trace.
func continued(t *testing.T, rec *tracetest.SpanRecorder) {
	t.Helper()
	var client, serverSpan sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		switch s.SpanKind().String() {
		case "client":
			client = s
		case "server":
			serverSpan = s
		}
	}
	if client == nil || serverSpan == nil {
		t.Fatalf("want a client and a server span, got %d spans", len(rec.Ended()))
	}
	if serverSpan.SpanContext().TraceID() != client.SpanContext().TraceID() ||
		serverSpan.Parent().SpanID() != client.SpanContext().SpanID() || !serverSpan.Parent().IsRemote() {
		t.Errorf("the server span is not the client's child: client %v/%v, server %v parent %v",
			client.SpanContext().TraceID(), client.SpanContext().SpanID(), serverSpan.SpanContext().TraceID(), serverSpan.Parent())
	}
}

func TestTheTraceContinuesAcrossLocalAndHTTP(t *testing.T) {
	ts := httptest.NewServer(server().Handler(verify))
	defer ts.Close()
	callers := map[string]modcall.Caller{
		"local": modcall.Local{"echo": server()},
		"http": &modcall.HTTPCaller{URLs: map[string]string{"echo": ts.URL},
			Token: func(context.Context, string) (string, error) { return "good", nil }},
	}
	for name, c := range callers {
		t.Run(name, func(t *testing.T) {
			rec := recorder(t)
			if _, err := modcall.Do[echoReq, echoRes](context.Background(), c, "echo", "say", echoReq{Word: "hi"}); err != nil {
				t.Fatal(err)
			}
			continued(t, rec)
		})
	}
}

func TestACallerWithNoSpanStartsATraceAndTheEnvelopeCarriesIt(t *testing.T) {
	recorder(t)
	_, req, p, err := modcall.Begin(context.Background(), "echo", "say", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Finish(nil, nil) //nolint:errcheck // ends the span
	if !strings.HasPrefix(req.Traceparent, "00-") || req.V != modcall.Version {
		t.Errorf("%+v", req)
	}
}

func sized(max int) *modcall.Server {
	s := modcall.NewServer("echo")
	modcall.Handle(s, "say", func(_ context.Context, r echoReq) (echoRes, error) { return echoRes{Said: r.Word}, nil })
	modcall.Handle(s, "big", func(_ context.Context, r echoReq) (echoRes, error) { return echoRes{Said: r.Word}, nil }, modcall.MaxBytes(max))
	modcall.Handle(s, "leak", func(_ context.Context, r echoReq) (echoRes, error) { return echoRes{}, errors.New(r.Word) })
	return s
}

func TestAnOversizeRequestIsBadRequestOnEveryTransportAndIsNotEchoed(t *testing.T) {
	ts := httptest.NewServer(sized(0 + modcall.DefaultMaxBytes*2).Handler(verify))
	defer ts.Close()
	huge := echoReq{Word: "SECRET" + strings.Repeat("x", modcall.DefaultMaxBytes)}
	callers := map[string]modcall.Caller{
		"local": modcall.Local{"echo": sized(modcall.DefaultMaxBytes * 2)},
		"http": &modcall.HTTPCaller{URLs: map[string]string{"echo": ts.URL},
			Token: func(context.Context, string) (string, error) { return "good", nil }},
	}
	for name, c := range callers {
		t.Run(name, func(t *testing.T) {
			_, err := modcall.Do[echoReq, echoRes](context.Background(), c, "echo", "say", huge)
			var e *modcall.Error
			if !errors.As(err, &e) || e.Code != modcall.CodeBadRequest || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestTheCalleeRefusesAnOversizePayloadItself(t *testing.T) {
	payload := json.RawMessage(`{"word":"SECRET` + strings.Repeat("x", modcall.DefaultMaxBytes) + `"}`)
	r := sized(1).Dispatch(context.Background(), modcall.Request{V: 2, Kind: modcall.Kind, Module: "echo", Method: "say", Payload: payload})
	if r.Error == nil || r.Error.Code != modcall.CodeBadRequest || strings.Contains(r.Error.Message, "SECRET") {
		t.Fatalf("%+v", r.Error)
	}
}

func TestTheHTTPListenerAnswersABodyNoMethodCouldTakeAsBadRequest(t *testing.T) {
	ts := httptest.NewServer(sized(1).Handler(verify))
	defer ts.Close()
	body := `{"v":2,"kind":"rpc","module":"echo","method":"say","payload":{"word":"` + strings.Repeat("x", modcall.HardMaxBytes+10) + `"}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+modcall.RPCPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer good")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	var out modcall.Response
	if err = json.NewDecoder(res.Body).Decode(&out); err != nil || out.Error == nil || out.Error.Code != modcall.CodeBadRequest {
		t.Fatalf("%d %+v %v", res.StatusCode, out, err)
	}
}

func TestAnOversizeResultIsInternalWithNoEcho(t *testing.T) {
	s := modcall.NewServer("echo")
	modcall.Handle(s, "grow", func(_ context.Context, r echoReq) (echoRes, error) {
		return echoRes{Said: "SECRET" + strings.Repeat("y", modcall.DefaultMaxBytes)}, nil
	})
	_, err := modcall.Local{"echo": s}.Call(context.Background(), "echo", "grow", []byte(`{}`))
	var e *modcall.Error
	if !errors.As(err, &e) || e.Code != modcall.CodeInternal || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("%v", err)
	}
}

func TestAMethodMayRaiseItsBoundButNotPastTheHardOne(t *testing.T) {
	word := strings.Repeat("z", modcall.DefaultMaxBytes+10)
	c := modcall.Local{"echo": sized(modcall.HardMaxBytes)}
	if got, err := modcall.Do[echoReq, echoRes](context.Background(), c, "echo", "big", echoReq{Word: word}); err != nil || got.Said != word {
		t.Fatalf("raised bound: %v", err)
	}
	if _, err := modcall.Do[echoReq, echoRes](context.Background(), c, "echo", "say", echoReq{Word: word}); err == nil {
		t.Fatal("the default bound did not apply to the other method")
	}
	defer func() {
		if recover() == nil {
			t.Error("a bound past the hard one must panic at registration")
		}
	}()
	sized(modcall.HardMaxBytes + 1)
}

func TestAnExpiredDeadlineShortCircuitsBeforeTheMethodRuns(t *testing.T) {
	ran := false
	s := modcall.NewServer("echo")
	modcall.Handle(s, "say", func(context.Context, echoReq) (echoRes, error) { ran = true; return echoRes{}, nil })
	r := s.Dispatch(context.Background(), modcall.Request{V: 2, Kind: modcall.Kind, Module: "echo", Method: "say",
		Deadline: time.Now().Add(-time.Second).UnixMilli()})
	if r.Error == nil || r.Error.Code != modcall.CodeDeadlineExceeded || ran {
		t.Fatalf("%+v ran=%v", r, ran)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := modcall.Local{"echo": s}.Call(ctx, "echo", "say", nil)
	if !errors.Is(err, modcall.Coded(modcall.CodeDeadlineExceeded, "")) || ran {
		t.Fatalf("%v ran=%v", err, ran)
	}
}

func TestTheCallersDeadlineTravelsAndBoundsTheMethod(t *testing.T) {
	var got time.Time
	var has bool
	s := modcall.NewServer("echo")
	modcall.Handle(s, "say", func(ctx context.Context, _ echoReq) (echoRes, error) {
		got, has = ctx.Deadline()
		return echoRes{}, nil
	})
	modcall.Handle(s, "wait", func(ctx context.Context, _ echoReq) (echoRes, error) {
		<-ctx.Done()
		return echoRes{}, ctx.Err()
	})
	ts := httptest.NewServer(s.Handler(verify))
	defer ts.Close()
	c := &modcall.HTTPCaller{URLs: map[string]string{"echo": ts.URL}, Token: func(context.Context, string) (string, error) { return "good", nil }}

	want := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), want)
	defer cancel()
	if _, err := c.Call(ctx, "echo", "say", nil); err != nil || !has || got.UnixMilli() != want.UnixMilli() {
		t.Fatalf("deadline %v (%v) want %v: %v", got, has, want, err)
	}

	// The callee gives up by the deadline even if the caller's wait is longer.
	dl := time.Now().Add(50 * time.Millisecond)
	r := s.Dispatch(context.Background(), modcall.Request{V: 2, Kind: modcall.Kind, Module: "echo", Method: "wait", Deadline: dl.UnixMilli()})
	if r.Error == nil || r.Error.Code != modcall.CodeDeadlineExceeded {
		t.Fatalf("%+v", r)
	}
}

func TestAV1RequestIsAnsweredInV1AndItsV2FieldsAreIgnored(t *testing.T) {
	s := server()
	ctx := modcall.WithCaller(context.Background(), "x")
	v1 := s.Dispatch(ctx, modcall.Request{Kind: modcall.Kind, Module: "echo", Method: "say", Payload: json.RawMessage(`{"word":"hi"}`),
		Deadline: time.Now().Add(-time.Hour).UnixMilli(), Traceparent: "garbage"})
	if v1.Error != nil || v1.V != 0 {
		t.Fatalf("%+v", v1)
	}
	if raw, _ := json.Marshal(v1); strings.Contains(string(raw), `"v"`) {
		t.Errorf("a v1 answer carries a version: %s", raw)
	}
	v2 := s.Dispatch(ctx, modcall.Request{V: 2, Kind: modcall.Kind, Module: "echo", Method: "say", Payload: json.RawMessage(`{"word":"hi"}`)})
	if v2.Error != nil || v2.V != 2 {
		t.Fatalf("%+v", v2)
	}
	if neg := s.Dispatch(ctx, modcall.Request{V: -1, Kind: modcall.Kind, Module: "echo", Method: "say"}); neg.Error == nil {
		t.Error("a negative version is accepted")
	}
}

func TestUnknownFieldsAreTolerated(t *testing.T) {
	ts := httptest.NewServer(server().Handler(verify))
	defer ts.Close()
	body := `{"v":2,"kind":"rpc","module":"echo","method":"say","payload":{"word":"hi","later":1},"future":{"a":1}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+modcall.RPCPath, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer good")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	var out modcall.Response
	if err = json.NewDecoder(res.Body).Decode(&out); err != nil || out.Error != nil || !strings.Contains(string(out.Result), `"hi"`) {
		t.Fatalf("%+v %v", out, err)
	}
	// And a response from a later callee, with a field this one does not know.
	var later modcall.Response
	if err = json.Unmarshal([]byte(`{"v":3,"result":{"a":1},"more":true}`), &later); err != nil {
		t.Fatal(err)
	}
}

// FuzzDispatch: whatever bytes arrive as an envelope, Dispatch answers one of
// a result or a coded error, never panics, and never echoes the payload.
func FuzzDispatch(f *testing.F) {
	for _, seed := range []string{
		`{"v":2,"kind":"rpc","module":"echo","method":"say","payload":{"word":"hi"}}`,
		`{"kind":"rpc","module":"echo","method":"leak","payload":{"word":"TOPSECRET"}}`,
		`{"v":2,"module":"echo","method":"leak","payload":{"word":"TOPSECRET"},"deadline":1}`,
		`{"v":2,"module":"echo","method":"say","payload":[1,2],"traceparent":"00-zz","tracestate":"a=b"}`,
		`{"v":9,"module":"echo","method":"say"}`,
		`{"v":-3,"module":"echo","method":"big","payload":"x"}`,
		`{"v":2,"module":"echo","method":"say","deadline":9223372036854775807}`,
	} {
		f.Add([]byte(seed))
	}
	s := sized(modcall.DefaultMaxBytes)
	f.Fuzz(func(t *testing.T, data []byte) {
		var req modcall.Request
		if json.Unmarshal(data, &req) != nil {
			return
		}
		r := s.Dispatch(modcall.WithCaller(context.Background(), "x"), req)
		if (r.Error == nil) == (r.Result == nil) {
			t.Fatalf("want exactly one of result and error: %+v", r)
		}
		if r.Error != nil && r.Error.Code == "" {
			t.Fatalf("an error with no code: %+v", r)
		}
		if r.Error != nil && strings.Contains(r.Error.Message, "TOPSECRET") {
			t.Fatalf("the payload is echoed: %+v", r.Error)
		}
		if _, err := json.Marshal(r); err != nil {
			t.Fatal(err)
		}
	})
}
