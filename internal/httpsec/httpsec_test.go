package httpsec_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/truvity/sluis/internal/httpsec"
)

func TestHeadersAreOnEveryResponse(t *testing.T) {
	t.Parallel()

	handler := httpsec.Headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}

		http.Error(w, "nope", http.StatusTeapot)
	}))

	for _, path := range []string{"/", "/redirect"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		header := recorder.Header()
		policy := header.Get("Content-Security-Policy")

		for _, want := range []string{
			"default-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'self'", "object-src 'none'",
		} {
			if !strings.Contains(policy, want) {
				t.Errorf("%s: policy %q lacks %s", path, policy, want)
			}
		}

		if header.Get("X-Frame-Options") != "DENY" || header.Get("Referrer-Policy") != "no-referrer" ||
			header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: headers = %v", path, header)
		}
	}
}

func TestAPolicyForAPageWhoseFormEndsInARedirect(t *testing.T) {
	t.Parallel()

	policy := httpsec.Policy{ScriptNonce: "abc", FormsMayRedirect: true}.String()

	if strings.Contains(policy, "form-action") {
		t.Errorf("policy %q has form-action, which stops the redirect after the form", policy)
	}

	if !strings.Contains(policy, "script-src 'nonce-abc'") || !strings.Contains(policy, "frame-ancestors 'none'") {
		t.Errorf("policy %q lacks the nonce or frame-ancestors", policy)
	}

	if strings.Contains(httpsec.Policy{}.String(), "script-src") {
		t.Error("the default policy names script-src; scripts are this origin's own, by default-src")
	}
}

func TestLimitBodyRefusesOversizeBodies(t *testing.T) {
	t.Parallel()

	const limit = 16

	var read error

	handler := httpsec.LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, read = io.Copy(io.Discard, r.Body)
		if read != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}), limit)

	t.Run("declared over", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", limit+1))))

		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", recorder.Code)
		}
	})

	t.Run("of unknown length", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(strings.NewReader(strings.Repeat("x", limit+1))))
		request.ContentLength = -1
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusRequestEntityTooLarge || read == nil {
			t.Errorf("status = %d, read error = %v; want 413 and an error", recorder.Code, read)
		}
	})

	t.Run("within", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("small")))

		if recorder.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204", recorder.Code)
		}
	})
}

func TestServerHoldsItsLimits(t *testing.T) {
	t.Parallel()

	server := httpsec.NewServer(":0", http.NotFoundHandler())

	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Errorf("a timeout is unset: %+v", server)
	}

	if server.MaxHeaderBytes <= 0 || server.MaxHeaderBytes >= http.DefaultMaxHeaderBytes {
		t.Errorf("MaxHeaderBytes = %d, want a limit below net/http's %d", server.MaxHeaderBytes, http.DefaultMaxHeaderBytes)
	}

	// A backup runs as one synchronous call of up to 16 minutes.
	if server.WriteTimeout.Minutes() < 17 {
		t.Errorf("WriteTimeout = %s would cut a running backup", server.WriteTimeout)
	}
}

func TestNoListingAnswersADirectoryWith404(t *testing.T) {
	t.Parallel()

	files := http.FileServerFS(fstest.MapFS{"assets/app.js": {Data: []byte("x")}})
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", httpsec.NoListing(files))

	for path, want := range map[string]int{
		"/assets/":        http.StatusNotFound,
		"/assets/sub/":    http.StatusNotFound,
		"/assets/app.js":  http.StatusOK,
		"/assets/gone.js": http.StatusNotFound,
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if recorder.Code != want {
			t.Errorf("GET %s = %d, want %d", path, recorder.Code, want)
		}

		if strings.Contains(recorder.Body.String(), "app.js") && path != "/assets/app.js" {
			t.Errorf("GET %s listed the directory: %s", path, recorder.Body.String())
		}
	}
}
