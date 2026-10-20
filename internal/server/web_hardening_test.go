package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/httpsec"
)

// consoleWithUI is the console with a built shell and one asset.
func consoleWithUI(t *testing.T) http.Handler {
	t.Helper()

	console := signInHarness(t, "ada@north.example")
	console.consoleUI = fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>sluis</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}

	return console.Handler()
}

// Every page the console serves carries the headers that keep it from being
// framed, sniffed or leaked by referrer: its sign-in page, a refusal, the
// shell, a redirect and a file.
func TestEveryConsoleResponseCarriesTheSecurityHeaders(t *testing.T) {
	t.Parallel()

	handler := consoleWithUI(t)

	for name, path := range map[string]string{
		"the sign-in page": "/login",
		"the shell":        "/",
		"a script":         "/assets/app.js",
		"an unknown path":  "/no-such-page",
		"a refused start":  "/login/nowhere/start",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		header := recorder.Header()
		policy := header.Get("Content-Security-Policy")

		for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'self'", "object-src 'none'"} {
			if !strings.Contains(policy, want) {
				t.Errorf("%s (%d): policy %q lacks %s", name, recorder.Code, policy, want)
			}
		}

		if header.Get("X-Frame-Options") != "DENY" || header.Get("Referrer-Policy") != "no-referrer" ||
			header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s (%d): headers = %v", name, recorder.Code, header)
		}
	}
}

// signedInShell is the console's shell as a signed-in person gets it.
func signedInShell(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	handler := consoleWithUI(t)

	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/login/demo/start", nil))
	cookie := start.Result().Cookies()[0] //nolint:bodyclose // a recorder's result has no body to close

	done := httptest.NewRecorder()
	callback := httptest.NewRequest(http.MethodGet, "/login/demo/callback?code=x&state="+cookie.Value, nil)
	callback.AddCookie(cookie)
	handler.ServeHTTP(done, callback)

	var session *http.Cookie
	for _, c := range done.Result().Cookies() { //nolint:bodyclose // a recorder's result has no body to close
		if c.Name == access.CookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("no session was issued (%d)", done.Code)
	}

	shell := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(session)
	handler.ServeHTTP(shell, request)

	return shell
}

// Creating a GitHub App is a form the shell POSTs to GitHub, and a browser
// refuses it when the page's policy says `form-action 'self'` alone. The
// shell may submit to GitHub's origin and to no other, and keeps the rest of
// the policy; every other page of the console still submits to itself only.
func TestTheShellMayPostAnAppManifestToGitHubAndNowhereElse(t *testing.T) {
	t.Parallel()

	shell := signedInShell(t)
	if shell.Code != http.StatusOK {
		t.Fatalf("shell = %d, want the page", shell.Code)
	}

	policy := shell.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'", "style-src 'self' 'unsafe-inline'", "img-src 'self' data:", "frame-ancestors 'none'",
		"base-uri 'none'", "object-src 'none'", "form-action 'self' https://github.com",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("shell policy %q lacks %s", policy, want)
		}
	}
	if strings.Contains(policy, "script-src") {
		t.Errorf("shell policy %q names script-src; scripts are this origin's own", policy)
	}
	if strings.Count(policy, "https://") != 1 {
		t.Errorf("shell policy %q names more than GitHub's origin", policy)
	}

	handler := consoleWithUI(t)
	for _, path := range []string{"/login", "/assets/app.js", "/no-such-page"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		other := recorder.Header().Get("Content-Security-Policy")
		if !strings.HasSuffix(other, "form-action 'self'") || strings.Contains(other, "github.com") {
			t.Errorf("%s policy %q is not the default `form-action 'self'`", path, other)
		}
	}
}

// The console says Strict-Transport-Security on every kind of response it
// serves when the request came over https, and not over plain http.
func TestTheConsoleSendsHSTSOnlyToARequestThatCameOverHTTPS(t *testing.T) {
	t.Parallel()

	console := signInHarness(t, "ada@north.example")
	console.consoleUI = fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>sluis</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	console.hsts = httpsec.HSTS{
		MaxAge: httpsec.DefaultHSTSMaxAge, IncludeSubDomains: true, PublicURL: "https://access.example/console",
	}
	handler := console.Handler()

	for _, path := range []string{"/login", "/", "/assets/app.js", "/no-such-page", "/login/nowhere/start"} {
		for name, c := range map[string]struct {
			host, forward string
			want          string
		}{
			"published":        {"access.example", "", "max-age=31536000; includeSubDomains"},
			"front said http":  {"access.example", "http", ""},
			"another host":     {"elsewhere.example", "", ""},
			"a loopback host":  {"localhost:8081", "", ""},
			"a client's claim": {"elsewhere.example", "https", ""},
		} {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Host = c.host

			if c.forward != "" {
				request.Header.Set("X-Forwarded-Proto", c.forward)
			}

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if got := recorder.Header().Get("Strict-Transport-Security"); got != c.want {
				t.Errorf("%s %s (%d): Strict-Transport-Security = %q, want %q", name, path, recorder.Code, got, c.want)
			}
		}
	}
}

// The bundle is one script from this origin and styles written at run time
// by its component library; the policy must allow exactly that.
func TestTheConsolePolicyAllowsTheBuiltBundleAndNothingMore(t *testing.T) {
	t.Parallel()

	policy := httpsec.Policy{}.String()

	for _, want := range []string{"style-src 'self' 'unsafe-inline'"} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy %q lacks %s", policy, want)
		}
	}

	for _, banned := range []string{"script-src", "'unsafe-eval'", "*"} {
		if strings.Contains(policy, banned) {
			t.Errorf("policy %q contains %s", policy, banned)
		}
	}
}

// A request for the assets directory is a 404, not a listing of the files.
func TestTheAssetsDirectoryIsNotListed(t *testing.T) {
	t.Parallel()

	handler := consoleWithUI(t)

	for _, path := range []string{"/assets/", "/assets"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		// `/assets` is sent on to `/assets/`, which is the 404.
		if recorder.Code != http.StatusNotFound && recorder.Code != http.StatusTemporaryRedirect {
			t.Errorf("GET %s = %d, want 404", path, recorder.Code)
		}

		if strings.Contains(recorder.Body.String(), "app.js") {
			t.Errorf("GET %s listed the directory: %s", path, recorder.Body.String())
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))

	if recorder.Code != http.StatusOK {
		t.Errorf("GET /assets/app.js = %d, want the file", recorder.Code)
	}
}

// A message over the limit is refused as it is decoded, before the
// service is reached, under either name of the services.
func TestAnOversizeConsoleCallIsRefusedBeforeTheServiceIsReached(t *testing.T) {
	t.Parallel()

	handler := consoleWithUI(t)
	big := `{"workspace":"` + strings.Repeat("x", httpsec.MaxRequestBody+1) + `"}`

	for _, path := range []string{
		"/sluis.v1.WorkspaceService/ListWorkspaces",
		"/directoryroster.v1.WorkspaceService/ListWorkspaces",
	} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(big))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "resource_exhausted") {
			t.Errorf("POST %s = %d %s, want resource_exhausted", path, recorder.Code, recorder.Body.String())
		}
	}
}
