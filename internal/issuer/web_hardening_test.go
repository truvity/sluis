package issuer_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/httpsec"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
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

// The issuer says Strict-Transport-Security on what reached it over https as
// its public URL says, and on nothing else.
func TestTheIssuerSendsHSTSOnlyToARequestThatCameOverHTTPS(t *testing.T) {
	t.Parallel()

	published, _ := signInServerConfigured(t, "ada@north.example", demo.Policy, issuer.Config{
		URL: "https://issuer.example", AllowInsecure: true,
		HSTS: httpsec.HSTS{MaxAge: httpsec.DefaultHSTSMaxAge, IncludeSubDomains: true},
	})
	plain, _ := signInServerConfigured(t, "ada@north.example", demo.Policy, issuer.Config{
		URL: "http://issuer.example", AllowInsecure: true,
		HSTS: httpsec.HSTS{MaxAge: httpsec.DefaultHSTSMaxAge, IncludeSubDomains: true},
	})

	for name, c := range map[string]struct {
		server  *httptest.Server
		host    string
		forward string
		want    string
	}{
		"the published host":              {published, "issuer.example", "", "max-age=31536000; includeSubDomains"},
		"the published host, front https": {published, "issuer.example", "https", "max-age=31536000; includeSubDomains"},
		"the published host, front http":  {published, "issuer.example", "http", ""},
		"another host":                    {published, "elsewhere.example", "", ""},
		"an http issuer":                  {plain, "issuer.example", "https", ""},
	} {
		for _, path := range []string{"/.well-known/openid-configuration", "/login", "/no-such-page"} {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, c.server.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}

			request.Host = c.host
			if c.forward != "" {
				request.Header.Set("X-Forwarded-Proto", c.forward)
			}

			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}

			_ = response.Body.Close()

			if got := response.Header.Get("Strict-Transport-Security"); got != c.want {
				t.Errorf("%s, %s: Strict-Transport-Security = %q, want %q", name, path, got, c.want)
			}
		}
	}
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

// A browser session store with no state codec fails open: the binding of a
// request to its browser is off and a sign-out cannot be confirmed. It is
// refused where the routes are built.
func TestASessionStoreWithNoStateCodecIsRefused(t *testing.T) {
	t.Parallel()

	sso := issuer.NewSSO(issuer.NewMemoryState(), time.Hour)

	t.Run("the routes", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if recover() == nil {
				t.Error("SignInRoutes accepted an SSO with no State")
			}
		}()

		issuer.SignInRoutes(http.NewServeMux(), issuer.SignInDeps{SSO: sso, Log: slog.New(slog.DiscardHandler)})
	})

	t.Run("the handler", func(t *testing.T) {
		t.Parallel()

		declared, err := policy.Parse([]byte(demo.Policy))
		if err != nil {
			t.Fatal(err)
		}

		set, err := policy.NewSet(declared)
		if err != nil {
			t.Fatal(err)
		}

		iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set,
			&fakeDirectory{}, issuer.NewMemoryState())

		storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}

		// The issuer's own session store is used when none is given, so
		// an empty SignInDeps is a store with no codec as well.
		if _, err = issuer.HandlerWithSignIn(iss, storage, issuer.SignInDeps{}); err == nil {
			t.Error("HandlerWithSignIn accepted a session store with no State")
		}

		// And with one it is served.
		if _, err = issuer.HandlerWithSignIn(iss, storage, issuer.SignInDeps{
			State: access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0),
		}); err != nil {
			t.Errorf("HandlerWithSignIn with a State: %v", err)
		}

	})
}
