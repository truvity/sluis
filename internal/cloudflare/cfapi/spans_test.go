package cfapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/cfapi"
	"github.com/truvity/sluis/internal/telemetry"
)

// recordSpans installs a tracer provider that keeps every span, unfiltered, for
// the length of the test.
func recordSpans(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	memory := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(memory))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})
	return memory
}

func spanDump(memory *tracetest.InMemoryExporter) string {
	var b strings.Builder
	spans := memory.GetSpans()
	for i := range spans {
		s := &spans[i]
		fmt.Fprintf(&b, "name=%q status=%v/%q\n", s.Name, s.Status.Code, s.Status.Description)
		for _, kv := range s.Attributes {
			fmt.Fprintf(&b, "  %s=%s\n", kv.Key, kv.Value.String())
		}
		for _, e := range s.Events {
			fmt.Fprintf(&b, "  event %s %v\n", e.Name, e.Attributes)
		}
	}
	return b.String()
}

// Every call is a span named for the method, every attempt a child span with
// its status and number, and a listing says how many pages and entries it
// read. No span carries the account, the minter's token, a token's id, name
// or value, or the URL, before any exporter filter; and every attribute is
// one the exporter lets out, so what is recorded is what leaves.
func TestTheSpansCarryNoSecretOrIdentifier(t *testing.T) {
	memory := recordSpans(t)
	var calls []call
	srv := server(t, &calls)
	ctx := t.Context()
	api, err := cfapi.Dial(cfapi.WithBaseURL(srv.URL))(ctx, acct, "minter-token-value")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.GetToken(ctx, "proto"); err != nil {
		t.Fatal(err)
	}
	if _, err = api.CreateToken(ctx, cloudflare.NewToken{
		Name: "sluis/instance/preset/minted-name", ExpiresOn: time.Now().Add(time.Hour),
		Policies: json.RawMessage(`[{"effect":"allow","resources":{"r":"*"},"permission_groups":[{"id":"g1"}]}]`),
	}); err != nil {
		t.Fatal(err)
	}
	if err = api.DeleteToken(ctx, "gone-token-id"); err != nil {
		t.Fatal(err)
	}
	if _, err = api.ListTokens(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = api.PermissionGroups(ctx); err != nil {
		t.Fatal(err)
	}

	out := spanDump(memory)
	for _, secret := range []string{
		acct, "minter-token-value", "Bearer", "secret-value", "minted-name", "sluis/instance", "gone-token-id",
		"proto", "new1", "accounts/", "/tokens", "include_expired", "DNS Write", srv.URL,
	} {
		if strings.Contains(out, secret) {
			t.Errorf("%q is on a span:\n%s", secret, out)
		}
	}
	for _, want := range []string{
		`name="cloudflare GetToken"`, `name="cloudflare CreateToken"`, `name="cloudflare DeleteToken"`,
		`name="cloudflare ListTokens"`, `name="cloudflare PermissionGroups"`, `name="cloudflare GET"`, `name="cloudflare POST"`,
		"rpc.method=ListTokens", "sluis.cloudflare.pages=2", "sluis.cloudflare.listed=2",
		"http.response.status_code=200", "http.request.resend_count=0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s:\n%s", want, out)
		}
	}
	spans := memory.GetSpans()
	for i := range spans {
		s := &spans[i]
		for _, kv := range s.Attributes {
			if !telemetry.SpanAttributeAllowlist[kv.Key] {
				t.Errorf("span %q carries %s, which the exporter drops", s.Name, kv.Key)
			}
		}
	}
}

// A retried call is one span per attempt, numbered, each with its status.
func TestARetryIsASpanPerAttempt(t *testing.T) {
	memory := recordSpans(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":1,"message":"busy"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"g1","name":"DNS Write"}]}`)
	}))
	t.Cleanup(srv.Close)
	api, err := cfapi.Dial(cfapi.WithBaseURL(srv.URL), cfapi.WithBackoff(time.Millisecond))(t.Context(), acct, "minter-token-value")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.PermissionGroups(t.Context()); err != nil {
		t.Fatal(err)
	}
	out := spanDump(memory)
	for _, want := range []string{
		"http.request.resend_count=0", "http.response.status_code=503", "error.type=503",
		"http.request.resend_count=1", "http.response.status_code=200",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s:\n%s", want, out)
		}
	}
}
