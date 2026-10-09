package issuer

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// RFC 9207: a redirect that carries an authorization result back to a
// client names the issuer that sent it; no other redirect is touched.
func TestAuthorizationResponsesCarryIss(t *testing.T) {
	t.Parallel()

	const issuer = "https://id.example"
	escaped := url.QueryEscape(issuer)

	for _, tc := range []struct{ name, location, want string }{
		{"success", "https://app.example/cb?code=abc&state=s", "https://app.example/cb?code=abc&state=s&iss=" + escaped},
		{"error", "https://app.example/cb?error=access_denied&state=s", "https://app.example/cb?error=access_denied&state=s&iss=" + escaped},
		{"loopback with port", "http://127.0.0.1:5000/cb?code=abc", "http://127.0.0.1:5000/cb?code=abc&iss=" + escaped},
		{"custom scheme", "app://cb?code=abc", "app://cb?code=abc&iss=" + escaped},
		{"fragment mode", "https://app.example/cb#code=abc&state=s", "https://app.example/cb#code=abc&state=s&iss=" + escaped},
		{"already present", "https://app.example/cb?code=abc&iss=x", "https://app.example/cb?code=abc&iss=x"},
		{"login page", "/login?id=1", "/login?id=1"},
		{"callback page", "https://id.example/authorize/callback?id=1", "https://id.example/authorize/callback?id=1"},
		{"upstream provider", "https://github.example/login/oauth/authorize?client_id=x&state=y", "https://github.example/login/oauth/authorize?client_id=x&state=y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handler := authorizationResponseIssuer(issuer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, tc.location, http.StatusFound)
			}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

			if got := rec.Header().Get("Location"); got != tc.want {
				t.Errorf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthorizationResponseIssLeavesNonRedirectsAlone(t *testing.T) {
	t.Parallel()

	handler := authorizationResponseIssuer("https://id.example", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://app.example/cb?code=abc")
		w.WriteHeader(http.StatusCreated)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if got := rec.Header().Get("Location"); got != "https://app.example/cb?code=abc" {
		t.Errorf("a non-redirect was rewritten: %q", got)
	}
}
