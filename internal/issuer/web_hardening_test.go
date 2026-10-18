package issuer_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// requiredDirectives are what every page must carry in its policy.
var requiredDirectives = []string{
	"default-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "object-src 'none'",
}

// checkHeaders asserts the four headers every response of the issuer has.
func checkHeaders(t *testing.T, what string, header http.Header, wantFormAction bool) {
	t.Helper()

	policy := header.Get("Content-Security-Policy")
	for _, directive := range requiredDirectives {
		if !strings.Contains(policy, directive) {
			t.Errorf("%s: Content-Security-Policy = %q, want %s", what, policy, directive)
		}
	}

	if got := strings.Contains(policy, "form-action 'self'"); got != wantFormAction {
		t.Errorf("%s: Content-Security-Policy = %q, form-action 'self' present = %v, want %v", what, policy, got, wantFormAction)
	}

	for name, want := range map[string]string{
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := header.Get(name); got != want {
			t.Errorf("%s: %s = %q, want %q", what, name, got, want)
		}
	}
}

// Every response of the issuer carries the headers that keep a page from
// being framed, sniffed or leaked by referrer: the sign-in pages, the
// pages that refuse, the redirects and the protocol's own documents.
func TestEveryIssuerResponseCarriesTheSecurityHeaders(t *testing.T) {
	t.Parallel()

	server, _ := signInServer(t, "ada@north.example")
	b := newBrowser(t, server)
	login, _ := startRequestWithHeader(t, b)

	for name, path := range map[string]string{
		"the sign-in page with no request": "/login",
		"the sign-in page":                 login,
		"a request that is not valid":      "/login?auth=nonsense",
		"the provider start":               strings.Replace(login, "/login?auth=", "/login/google/start?auth=", 1),
		"the signed-out page":              "/signed-out",
		"the discovery document":           "/.well-known/openid-configuration",
		"the key set":                      "/keys",
		"an unknown path":                  "/no-such-page",
	} {
		_, _, _, header := b.send(http.MethodGet, path, nil)
		checkHeaders(t, name, header, true)
	}

	// The refusal of /authorize, as a page for a browser.
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/authorize?client_id=nobody", nil)
	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Accept", "text/html")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}

	_ = response.Body.Close()

	checkHeaders(t, "the refusal of /authorize", response.Header, true)
}

// The protocol endpoints answer a browser's page from any origin, and never
// with credentials: the library's own default reflects the asking Origin and
// allows credentials with it.
func TestCrossOriginAnswersNeverAllowCredentials(t *testing.T) {
	t.Parallel()

	server, _ := signInServer(t, "ada@north.example")

	const origin = "https://attacker.example"

	for _, path := range []string{"/keys", "/.well-known/openid-configuration", "/userinfo", "/token"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
			request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(url.Values{}.Encode()))
			if err != nil {
				t.Fatal(err)
			}

			request.Header.Set("Origin", origin)

			if method == http.MethodOptions {
				request.Header.Set("Access-Control-Request-Method", http.MethodPost)
				request.Header.Set("Access-Control-Request-Headers", "authorization")
			}

			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}

			_ = response.Body.Close()

			if got := response.Header.Get("Access-Control-Allow-Credentials"); got != "" {
				t.Errorf("%s %s: Access-Control-Allow-Credentials = %q, want none", method, path, got)
			}

			if got := response.Header.Get("Access-Control-Allow-Origin"); got == origin {
				t.Errorf("%s %s: Access-Control-Allow-Origin reflects the asking origin %q", method, path, got)
			}

			if got := response.Header.Get("Access-Control-Allow-Origin"); got != "*" {
				t.Errorf("%s %s: Access-Control-Allow-Origin = %q, want *", method, path, got)
			}
		}
	}
}
