package issuer

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// attrsOf is a refusal's attributes by key, failing on a key set twice.
func attrsOf(t *testing.T, args []any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, arg := range args {
		attr, ok := arg.(slog.Attr)
		if !ok {
			t.Fatalf("%v is not an attribute", arg)
		}
		if _, dup := out[attr.Key]; dup {
			t.Errorf("%s is set twice", attr.Key)
		}
		out[attr.Key] = attr.Value.String()
	}
	return out
}

func TestWithRequestNamesTheRequestAndNothingInItsQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet,
		"/authorize/callback?id=handle-0123&client_id=local-dev&state=s-0123&nonce=n-0123&code=c-0123&code_challenge=cc-0123", nil)
	r.Header.Set("User-Agent", strings.Repeat("a", 300))
	r.Header.Set("Cookie", "sluis_session=cookie-0123")

	var got map[string]string
	withRequestFacts(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = attrsOf(t, withRequest(r.Context(), nil, []any{slog.String("reason", "why")}))
	})).ServeHTTP(httptest.NewRecorder(), r)

	for key, want := range map[string]string{
		"reason": "why", "method": "GET", "path": "/authorize/callback", "client_id": "local-dev",
		"user_agent": strings.Repeat("a", maxUserAgent) + "…",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	for key, value := range got {
		for _, secret := range []string{"handle-0123", "s-0123", "n-0123", "c-0123", "cc-0123", "cookie-0123", "?"} {
			if strings.Contains(value, secret) {
				t.Errorf("%s = %q carries %q", key, value, secret)
			}
		}
	}
}

// A refusal that names its client itself keeps its own field, under its
// own name, and is not given a second.
func TestWithRequestKeepsTheCallersFields(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/token?client_id=from-query", nil)
	for _, fields := range [][]any{
		{slog.String("client", "named")},
		{slog.String("client_id", "named")},
		{"client_id", "named"},
	} {
		got := withRequest(context.Background(), r, fields)
		keys := keysOf(got)
		if keys["client"] && keys["client_id"] {
			t.Errorf("%v: given both client and client_id: %v", fields, got)
		}
		for _, arg := range got {
			if attr, ok := arg.(slog.Attr); ok && attr.Value.String() == "from-query" {
				t.Errorf("%v: the request's client replaced or joined the caller's: %v", fields, got)
			}
		}
	}

	// A path the caller named is kept as it is.
	got := attrsOf(t, withRequest(context.Background(), r, []any{slog.String("path", "/kept")}))
	if got["path"] != "/kept" || got["method"] != http.MethodPost {
		t.Errorf("path, method = %q, %q, want /kept, POST", got["path"], got["method"])
	}
}

// The client a pending authorization names reaches a refusal later in the
// same request.
func TestNoteClientReachesTheRefusal(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/login?auth=handle-0123", nil)
	withRequestFacts(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		noteClient(r.Context(), "argocd")
		if got := attrsOf(t, withRequest(r.Context(), nil, nil)); got["client_id"] != "argocd" || got["path"] != "/login" {
			t.Errorf("client_id, path = %q, %q, want argocd, /login", got["client_id"], got["path"])
		}
	})).ServeHTTP(httptest.NewRecorder(), r)
}
