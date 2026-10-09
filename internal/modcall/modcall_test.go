package modcall_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/modcall"
)

type echoReq struct {
	Word string `json:"word"`
}
type echoRes struct {
	Said    string `json:"said"`
	Subject string `json:"subject"`
}

func server() *modcall.Server {
	s := modcall.NewServer("echo")
	modcall.Handle(s, "say", func(ctx context.Context, r echoReq) (echoRes, error) {
		return echoRes{Said: r.Word, Subject: modcall.CallerOf(ctx)}, nil
	})
	modcall.Handle(s, "refuse", func(context.Context, echoReq) (echoRes, error) {
		return echoRes{}, modcall.Coded("not_granted", "no")
	})
	modcall.Handle(s, "crash", func(context.Context, echoReq) (echoRes, error) {
		return echoRes{}, errors.New("secret detail")
	})
	return s
}

func TestLocalRoundTripsTypedCalls(t *testing.T) {
	c := modcall.Local{"echo": server()}
	got, err := modcall.Do[echoReq, echoRes](context.Background(), c, "echo", "say", echoReq{Word: "hi"})
	if err != nil || got.Said != "hi" {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestACodedErrorCrossesAndAnyOtherIsInternalWithNoDetail(t *testing.T) {
	c := modcall.Local{"echo": server()}
	_, err := modcall.Do[echoReq, echoRes](context.Background(), c, "echo", "refuse", echoReq{})
	var e *modcall.Error
	if !errors.As(err, &e) || e.Code != "not_granted" || !errors.Is(err, modcall.Coded("not_granted", "")) {
		t.Fatalf("%v", err)
	}
	_, err = modcall.Do[echoReq, echoRes](context.Background(), c, "echo", "crash", echoReq{})
	if !errors.As(err, &e) || e.Code != modcall.CodeInternal || e.Message != "" {
		t.Fatalf("%v", err)
	}
}

func TestUnknownModuleAndMethodAreNoSuchMethod(t *testing.T) {
	c := modcall.Local{"echo": server()}
	for _, m := range [][2]string{{"echo", "nope"}, {"other", "say"}} {
		_, err := c.Call(context.Background(), m[0], m[1], nil)
		var e *modcall.Error
		if !errors.As(err, &e) || e.Code != modcall.CodeNoSuchMethod {
			t.Errorf("%v: %v", m, err)
		}
	}
}

func TestAMethodRegisteredTwicePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	s := server()
	modcall.Handle(s, "say", func(context.Context, echoReq) (echoRes, error) { return echoRes{}, nil })
}

func verify(_ context.Context, bearer string) (string, error) {
	if bearer != "good" {
		return "", errors.New("no")
	}
	return "system:serviceaccount:ns:issuer", nil
}

func TestHTTPCallerAuthenticatesAndTheServerNamesTheCaller(t *testing.T) {
	ts := httptest.NewServer(server().Handler(verify))
	defer ts.Close()
	var audience string
	c := &modcall.HTTPCaller{
		URLs: map[string]string{"echo": ts.URL}, Audiences: map[string]string{"echo": "sluis-echo"},
		Token: func(_ context.Context, a string) (string, error) { audience = a; return "good", nil },
	}
	got, err := modcall.Do[echoReq, echoRes](context.Background(), c, "echo", "say", echoReq{Word: "hi"})
	if err != nil || got.Said != "hi" || got.Subject != "system:serviceaccount:ns:issuer" || audience != "sluis-echo" {
		t.Fatalf("%+v %q, %v", got, audience, err)
	}
	if _, err = modcall.Do[echoReq, echoRes](context.Background(), c, "echo", "refuse", echoReq{}); !errors.Is(err, modcall.Coded("not_granted", "")) {
		t.Fatalf("%v", err)
	}
}

func TestHTTPServerRefusesABadOrMissingBearer(t *testing.T) {
	ts := httptest.NewServer(server().Handler(verify))
	defer ts.Close()
	c := &modcall.HTTPCaller{URLs: map[string]string{"echo": ts.URL}, Token: func(context.Context, string) (string, error) { return "bad", nil }}
	if _, err := c.Call(context.Background(), "echo", "say", nil); !errors.Is(err, modcall.ErrTransport) {
		t.Fatalf("%v", err)
	}
	c.Token = func(context.Context, string) (string, error) { return "", nil }
	if _, err := c.Call(context.Background(), "echo", "say", nil); !errors.Is(err, modcall.ErrTransport) {
		t.Fatalf("%v", err)
	}
}

type marker string

func (m marker) Call(context.Context, string, string, []byte) ([]byte, error) {
	return []byte(`"` + string(m) + `"`), nil
}

func TestRouterSendsEachModuleWhereTheConfigSays(t *testing.T) {
	cfg := modcall.Config{Modules: map[string]modcall.Target{"a": {Function: "f"}, "b": {URL: "http://b"}}}
	r, err := modcall.NewRouter(cfg, marker("local"), marker("lambda"), marker("http"))
	if err != nil {
		t.Fatal(err)
	}
	for module, want := range map[string]string{"a": `"lambda"`, "b": `"http"`, "c": `"local"`} {
		got, err := r.Call(context.Background(), module, "m", nil)
		if err != nil || string(got) != want {
			t.Errorf("%s: %s, %v", module, got, err)
		}
	}
	if cfg.Remote("c") || !cfg.Remote("a") {
		t.Error("Remote")
	}
	if _, err = modcall.NewRouter(modcall.Config{}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	r, _ = modcall.NewRouter(modcall.Config{}, nil, nil, nil)
	if _, err = r.Call(context.Background(), "x", "m", nil); !errors.Is(err, modcall.ErrNoRoute) {
		t.Fatalf("%v", err)
	}
}

func TestRouterRefusesATargetWithNoTransport(t *testing.T) {
	for name, cfg := range map[string]modcall.Config{
		"lambda": {Modules: map[string]modcall.Target{"a": {Function: "f"}}},
		"http":   {Modules: map[string]modcall.Target{"a": {URL: "http://a"}}},
		"both":   {Modules: map[string]modcall.Target{"a": {Function: "f", URL: "http://a"}}},
	} {
		if _, err := modcall.NewRouter(cfg, nil, nil, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAllowRefusesEveryOtherCallerClassOnEveryTransport(t *testing.T) {
	s := modcall.NewServer("echo")
	ran := 0
	modcall.Handle(s, "say", func(context.Context, echoReq) (echoRes, error) { ran++; return echoRes{}, nil },
		modcall.Allow("issuer", "console"))
	l := modcall.Local{"echo": s}
	for _, class := range []string{"issuer", "console"} {
		if _, err := l.As(class).Call(context.Background(), "echo", "say", nil); err != nil {
			t.Errorf("%s: %v", class, err)
		}
	}
	for _, class := range []string{"restore", ""} {
		_, err := l.As(class).Call(context.Background(), "echo", "say", nil)
		if !errors.Is(err, modcall.Coded(modcall.CodeForbidden, "")) {
			t.Errorf("%q: %v", class, err)
		}
	}
	if _, err := l.Call(context.Background(), "echo", "say", nil); !errors.Is(err, modcall.Coded(modcall.CodeForbidden, "")) {
		t.Errorf("a Local with no class: %v", err)
	}
	if ran != 2 {
		t.Errorf("ran %d", ran)
	}

	srv := httptest.NewServer(s.Handler(func(_ context.Context, b string) (string, error) { return b, nil }))
	t.Cleanup(srv.Close)
	for bearer, wantErr := range map[string]bool{"system:serviceaccount:sluis:issuer": false, "system:serviceaccount:sluis:other": true, "console": false, "x": true} {
		c := &modcall.HTTPCaller{URLs: map[string]string{"echo": srv.URL}, Token: func(context.Context, string) (string, error) { return bearer, nil }}
		_, err := c.Call(context.Background(), "echo", "say", nil)
		if (err != nil) != wantErr {
			t.Errorf("%s: %v", bearer, err)
		}
	}
	// WithClasses maps a subject whose name is not its class.
	srv2 := httptest.NewServer(s.Handler(func(_ context.Context, b string) (string, error) { return b, nil },
		modcall.WithClasses(map[string]string{"sluis-controller": "issuer"})))
	t.Cleanup(srv2.Close)
	c := &modcall.HTTPCaller{URLs: map[string]string{"echo": srv2.URL}, Token: func(context.Context, string) (string, error) { return "sluis-controller", nil }}
	if _, err := c.Call(context.Background(), "echo", "say", nil); err != nil {
		t.Error(err)
	}
}

func TestACallerWrittenInThePayloadOrTheEnvelopeIsNotTheCaller(t *testing.T) {
	s := server()
	resp := s.Dispatch(modcall.WithCaller(context.Background(), "console"), modcall.Request{
		V: modcall.Version, Kind: modcall.Kind, Module: "echo", Method: "say", Payload: []byte(`{"word":"x","caller":"issuer","subject":"issuer"}`)})
	raw, err := modcall.Result(resp)
	if err != nil || !strings.Contains(string(raw), `"subject":"console"`) {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestAVersionNewerThanTheServerIsRefusedAndAnAbsentOneIsV1(t *testing.T) {
	s := server()
	r := s.Dispatch(context.Background(), modcall.Request{V: modcall.Version + 1, Kind: modcall.Kind, Module: "echo", Method: "say"})
	if r.Error == nil || r.Error.Code != modcall.CodeBadRequest {
		t.Errorf("%+v", r)
	}
	if r = s.Dispatch(context.Background(), modcall.Request{Kind: modcall.Kind, Module: "echo", Method: "say"}); r.Error != nil {
		t.Errorf("%+v", r)
	}
}

func TestClassOfSubject(t *testing.T) {
	for in, want := range map[string]string{"system:serviceaccount:ns:issuer": "issuer", "issuer": "issuer", "system:serviceaccount:ns:": "system:serviceaccount:ns:"} {
		if got := modcall.ClassOfSubject(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
