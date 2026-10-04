package issuerapp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The bare origin sends a person to the console, for GET and HEAD, and for
// that exact path only: everything the issuer serves keeps answering.
func TestTheRootRedirectsToTheConsoleAndShadowsNothing(t *testing.T) {
	issuer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-By", "issuer")
		w.WriteHeader(http.StatusTeapot)
	})
	console := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-By", "console:"+r.URL.Path)
	})
	h := mount(issuer, console)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/", nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/console/" {
			t.Errorf("%s /: %d to %q, want 302 to /console/", method, rec.Code, rec.Header().Get("Location"))
		}
	}

	for _, path := range []string{"/.well-known/openid-configuration", "/authorize", "/token", "/login", "/jwks.json", "/anything"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Header().Get("X-Served-By") != "issuer" || rec.Code != http.StatusTeapot {
			t.Errorf("GET %s was shadowed: %d %q", path, rec.Code, rec.Header().Get("X-Served-By"))
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Header().Get("X-Served-By") != "issuer" {
		t.Errorf("POST / must reach the issuer, got %d", rec.Code)
	}

	// Without a console there is nothing to send anyone to.
	rec = httptest.NewRecorder()
	mount(issuer, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("no console: GET / = %d, want the issuer's own answer", rec.Code)
	}
}
