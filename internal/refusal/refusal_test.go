package refusal_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/sdk/emit"
	"github.com/truvity/sluis/internal/refusal"
)

// seen is what a handler under the middleware can read.
type seen struct{ ctxID, auditID string }

func serve(t *testing.T, inbound string) (*httptest.ResponseRecorder, seen) {
	t.Helper()
	var got seen
	h := refusal.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.ctxID = refusal.ID(r.Context())
		got.auditID = emit.FromRequest(r, 0).GetRequestId()
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if inbound != "" {
		req.Header.Set(refusal.Header, inbound)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, got
}

func TestMiddlewareAdoptsAWellFormedInboundID(t *testing.T) {
	t.Parallel()
	rec, got := serve(t, "gw-1234:abcd.EF_9")
	for name, id := range map[string]string{
		"response": rec.Header().Get(refusal.Header), "context": got.ctxID, "audit": got.auditID,
	} {
		if id != "gw-1234:abcd.EF_9" {
			t.Errorf("%s id = %q, want the inbound one", name, id)
		}
	}
}

func TestMiddlewareReplacesAnIDItDoesNotTrust(t *testing.T) {
	t.Parallel()
	for name, inbound := range map[string]string{
		"too short":      "abc",
		"too long":       strings.Repeat("a", 65),
		"a space":        "has a space in it",
		"a newline":      "line\nbreak-12345",
		"markup":         "<script>alert1</script>",
		"non-ascii":      "идентификатор-12",
		"a json quote":   `abc"def-12345`,
		"a percent form": "abc%0d%0a-12345",
	} {
		rec, got := serve(t, inbound)
		id := rec.Header().Get(refusal.Header)
		if id == inbound || !refusal.WellFormed(id) {
			t.Errorf("%s: response id = %q, want a generated well-formed one", name, id)
		}
		if got.ctxID != id || got.auditID != id {
			t.Errorf("%s: context %q and audit %q must carry the response's %q", name, got.ctxID, got.auditID, id)
		}
	}
}

func TestMiddlewareGeneratesADifferentIDPerRequest(t *testing.T) {
	t.Parallel()
	first, _ := serve(t, "")
	second, _ := serve(t, "")
	a, b := first.Header().Get(refusal.Header), second.Header().Get(refusal.Header)
	if a == "" || a == b {
		t.Errorf("ids %q and %q: want two distinct non-empty ids", a, b)
	}
}

func TestMiddlewareIsIdempotentWhenNested(t *testing.T) {
	t.Parallel()
	var inner string
	h := refusal.Middleware(refusal.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner = refusal.ID(r.Context())
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if inner == "" || inner != rec.Header().Get(refusal.Header) {
		t.Errorf("inner id %q, response %q: want one id", inner, rec.Header().Get(refusal.Header))
	}
}
