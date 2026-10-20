package httpsec_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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

func TestAPolicyThatLetsAFormGoToAnotherOrigin(t *testing.T) {
	t.Parallel()

	policy := httpsec.Policy{FormTargets: []string{"https://github.com"}}.String()

	if !strings.HasSuffix(policy, "form-action 'self' https://github.com") {
		t.Errorf("policy %q does not end in form-action 'self' https://github.com", policy)
	}

	for _, want := range []string{
		"default-src 'self'", "style-src 'self' 'unsafe-inline'", "img-src 'self' data:",
		"frame-ancestors 'none'", "base-uri 'none'", "object-src 'none'",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy %q lacks %s", policy, want)
		}
	}

	// The issuer's policies are what they were: the default names the page's
	// own origin only, and the redirecting pages have no form-action at all.
	if got := (httpsec.Policy{}).String(); !strings.HasSuffix(got, "form-action 'self'") || strings.Contains(got, "github") {
		t.Errorf("default policy %q is not `form-action 'self'`", got)
	}

	if got := (httpsec.Policy{FormsMayRedirect: true, FormTargets: []string{"https://github.com"}}).String(); strings.Contains(got, "form-action") {
		t.Errorf("policy %q has form-action, which stops the redirect after the form", got)
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

func hstsHandler(hsts httpsec.HSTS) http.Handler {
	return httpsec.HeadersWith(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upstream" {
			w.Header().Set("Strict-Transport-Security", "max-age=1")
		}

		w.WriteHeader(http.StatusNoContent)
	}), hsts)
}

func hstsAnswer(t *testing.T, handler http.Handler, r *http.Request) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r)

	return recorder.Header().Get("Strict-Transport-Security")
}

func TestHSTSIsSentWithItsDefaultsOverTLS(t *testing.T) {
	t.Parallel()

	handler := hstsHandler(httpsec.HSTS{MaxAge: httpsec.DefaultHSTSMaxAge, IncludeSubDomains: true})

	request := httptest.NewRequest(http.MethodGet, "https://issuer.example/", nil)
	request.TLS = &tls.ConnectionState{}

	got := hstsAnswer(t, handler, request)
	if got != "max-age=31536000; includeSubDomains" {
		t.Errorf("Strict-Transport-Security = %q", got)
	}

	if strings.Contains(got, "preload") {
		t.Errorf("Strict-Transport-Security = %q carries preload", got)
	}
}

func TestHSTSIsAbsentOverPlainHTTPOffTheLoopback(t *testing.T) {
	t.Parallel()

	handler := hstsHandler(httpsec.HSTS{MaxAge: httpsec.DefaultHSTSMaxAge, IncludeSubDomains: true})

	// No TLS, no public URL, and a front that says https: a client can send
	// that header, and it grants nothing.
	request := httptest.NewRequest(http.MethodGet, "http://issuer.example/", nil)
	request.Header.Set("X-Forwarded-Proto", "https")

	if got := hstsAnswer(t, handler, request); got != "" {
		t.Errorf("Strict-Transport-Security = %q over plain http", got)
	}
}

func TestHSTSIsNeverSentToALoopbackHost(t *testing.T) {
	t.Parallel()

	handler := hstsHandler(httpsec.HSTS{
		MaxAge: httpsec.DefaultHSTSMaxAge, IncludeSubDomains: true, PublicURL: "https://localhost:8443",
	})

	for _, host := range []string{
		"localhost", "localhost:8443", "LOCALHOST.", "sluis.localhost", "127.0.0.1", "127.0.0.1:8080", "[::1]:8443", "[::1]", "127.1.2.3",
	} {
		for name, tlsOn := range map[string]bool{"tls": true, "plain": false} {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Host = host

			if tlsOn {
				request.TLS = &tls.ConnectionState{}
			}

			if got := hstsAnswer(t, handler, request); got != "" {
				t.Errorf("%s over %s: Strict-Transport-Security = %q", host, name, got)
			}
		}
	}
}

func TestHSTSFollowsThePublicHTTPSURLUnlessTheFrontSaysHTTP(t *testing.T) {
	t.Parallel()

	handler := hstsHandler(httpsec.HSTS{
		MaxAge: httpsec.DefaultHSTSMaxAge, IncludeSubDomains: true, PublicURL: "https://issuer.example/console",
	})

	for name, c := range map[string]struct {
		host, forward, want string
	}{
		"the published host":            {"issuer.example", "", "max-age=31536000; includeSubDomains"},
		"the published host and a port": {"issuer.example:443", "", "max-age=31536000; includeSubDomains"},
		"in another case":               {"Issuer.Example", "", "max-age=31536000; includeSubDomains"},
		"the front said https":          {"issuer.example", "https", "max-age=31536000; includeSubDomains"},
		"the front said http":           {"issuer.example", "http", ""},
		"the front said HTTP":           {"issuer.example", "HTTP", ""},
		"another host":                  {"elsewhere.example", "", ""},
		"another host, front https":     {"elsewhere.example", "https", ""},
	} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Host = c.host

		if c.forward != "" {
			request.Header.Set("X-Forwarded-Proto", c.forward)
		}

		if got := hstsAnswer(t, handler, request); got != c.want {
			t.Errorf("%s: Strict-Transport-Security = %q, want %q", name, got, c.want)
		}
	}

	// A public URL that is not https says nothing about the browser's scheme.
	plain := hstsHandler(httpsec.HSTS{MaxAge: httpsec.DefaultHSTSMaxAge, PublicURL: "http://issuer.example"})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "issuer.example"

	if got := hstsAnswer(t, plain, request); got != "" {
		t.Errorf("an http public URL: Strict-Transport-Security = %q", got)
	}
}

func TestHSTSIsConfigurable(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		hsts httpsec.HSTS
		want string
	}{
		"a shorter age":     {httpsec.HSTS{MaxAge: 5 * time.Minute, IncludeSubDomains: true}, "max-age=300; includeSubDomains"},
		"no subdomains":     {httpsec.HSTS{MaxAge: time.Hour}, "max-age=3600"},
		"switched off":      {httpsec.HSTS{IncludeSubDomains: true}, ""},
		"the zero value":    {httpsec.HSTS{}, ""},
		"under a second":    {httpsec.HSTS{MaxAge: time.Millisecond}, ""},
		"whole seconds out": {httpsec.HSTS{MaxAge: 90*time.Second + time.Millisecond}, "max-age=90"},
	} {
		hsts := c.hsts
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.TLS = &tls.ConnectionState{}

		if got := hstsAnswer(t, hstsHandler(hsts), request); got != c.want {
			t.Errorf("%s: Strict-Transport-Security = %q, want %q", name, got, c.want)
		}
	}
}

func TestHSTSAnUpstreamHeaderIsKept(t *testing.T) {
	t.Parallel()

	handler := hstsHandler(httpsec.HSTS{MaxAge: httpsec.DefaultHSTSMaxAge, IncludeSubDomains: true})

	// Already on the response before the middleware runs.
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=60")
		handler.ServeHTTP(w, r)
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.TLS = &tls.ConnectionState{}

	if got := hstsAnswer(t, outer, request); got != "max-age=60" {
		t.Errorf("Strict-Transport-Security = %q, want the upstream's", got)
	}

	// A handler that sets its own wins too.
	request = httptest.NewRequest(http.MethodGet, "/upstream", nil)
	request.TLS = &tls.ConnectionState{}

	if got := hstsAnswer(t, handler, request); got != "max-age=1" {
		t.Errorf("Strict-Transport-Security = %q, want the handler's", got)
	}
}

func TestHSTSTrustedFrontIsOnlyWhatTheProcessMarks(t *testing.T) {
	t.Parallel()

	handler := hstsHandler(httpsec.HSTS{MaxAge: httpsec.DefaultHSTSMaxAge, IncludeSubDomains: true})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "id.example.org"

	if got := hstsAnswer(t, handler, request); got != "" {
		t.Errorf("unmarked: Strict-Transport-Security = %q", got)
	}

	request = request.WithContext(httpsec.TrustHTTPS(request.Context()))
	if got := hstsAnswer(t, handler, request); got != "max-age=31536000; includeSubDomains" {
		t.Errorf("marked: Strict-Transport-Security = %q", got)
	}

	// The default middleware says nothing about HSTS.
	plain := httpsec.Headers(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if got := hstsAnswer(t, plain, request); got != "" {
		t.Errorf("Headers: Strict-Transport-Security = %q", got)
	}
}
