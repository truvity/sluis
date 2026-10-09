package lambdaapp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/lambdacontext"

	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/modcall"
)

func echoServer() *modcall.Server {
	s := modcall.NewServer("cloudflare")
	modcall.Handle(s, "echo", func(ctx context.Context, in map[string]string) (map[string]string, error) {
		return map[string]string{"said": in["say"], "caller": modcall.CallerOf(ctx)}, nil
	})
	return s
}

func rpcEvent(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(modcall.Request{Kind: modcall.Kind, Module: "cloudflare", Method: "echo", Payload: json.RawMessage(`{"say":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAnRPCEventIsAnsweredByTheModuleServerWithTheInvokedAlias(t *testing.T) {
	h := lambdaapp.NewHTTP(http.NotFoundHandler(), nil, nil).WithRPC(echoServer())
	ctx := lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{
		InvokedFunctionArn: strings.Join([]string{"arn", "partition", "lambda", "region", "account", "function", "cloudflare", "live"}, ":")})
	out, err := h.Handle(ctx, rpcEvent(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	var res modcall.Response
	if err = json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	body, err := modcall.Result(res)
	if err != nil || !strings.Contains(string(body), `"said":"hi"`) || !strings.Contains(string(body), `"caller":"live"`) {
		t.Fatalf("%s %v", body, err)
	}
}

func TestAFunctionThatRunsNoModuleRefusesRPCEvents(t *testing.T) {
	// The issuer function is built with no server.
	h := lambdaapp.NewHTTP(http.NotFoundHandler(), nil, nil)
	if _, err := h.Handle(context.Background(), rpcEvent(t)); err == nil || !strings.Contains(err.Error(), "runs no module") {
		t.Fatalf("%v", err)
	}
}

func TestOtherEventsAreUntouchedByAnRPCServer(t *testing.T) {
	h := lambdaapp.NewHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }), nil, nil).
		WithRPC(echoServer())
	if _, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"nope"}`)); err == nil {
		t.Errorf("an unknown kind: %v", err)
	}
	out, err := h.Handle(context.Background(), json.RawMessage(
		`{"version":"2.0","rawPath":"/x","requestContext":{"http":{"method":"GET"}},"headers":{"host":"h"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(out); !strings.Contains(string(raw), `"statusCode":418`) {
		t.Errorf("%s", raw)
	}
}
