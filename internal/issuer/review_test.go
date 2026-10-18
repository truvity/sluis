package issuer_test

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/signer"
	"github.com/truvity/sluis/policy"
)

// forgedHint is an ID token the issuer under key signed, for a subject, with
// its lifetime ending at exp.
func forgedHint(t *testing.T, key *signer.SigningKey, sub string, exp time.Time) string {
	t.Helper()

	s, err := jose.NewSigner(jose.SigningKey{Algorithm: key.SignatureAlgorithm(), Key: key.Key()},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", key.ID()))
	if err != nil {
		t.Fatal(err)
	}

	payload := `{"iss":"http://issuer.example","sub":"` + sub + `","aud":["local-dev"],"azp":"local-dev",` +
		`"iat":` + itoa(exp.Add(-time.Hour).Unix()) + `,"exp":` + itoa(exp.Unix()) + `,"auth_time":` + itoa(exp.Add(-time.Hour).Unix()) + `}`

	signed, err := s.Sign([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}

	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}

	return token
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func signedOut(b *browser) bool { return b.cookies[issuer.SSOCookieName] == "" }

// An id_token_hint ends the sign-in without asking only when it names the
// person this browser is signed in as. Any other ID token from this issuer, an
// expired one included, is not the relying party's word for THIS browser.
func TestEndSessionAsksUnlessTheHintIsTheBrowsersOwnSignIn(t *testing.T) {
	t.Parallel()
	key := mustSoftKey(t, false)

	ada := newBrowser(t, hintServer(t, "ada@north.example", key))
	ada.signIn()

	graceServer := hintServer(t, "grace@north.example", key)
	grace := newBrowser(t, graceServer)
	grace.signIn()

	fresh := forgedHint(t, key, "ada@north.example", time.Now().Add(time.Hour))
	expired := forgedHint(t, key, "ada@north.example", time.Now().Add(-time.Hour))

	// Somebody else's token, expired or not: the page, and nothing ended.
	for name, hint := range map[string]string{"valid": fresh, "expired": expired} {
		status, location, body := grace.do(http.MethodGet, "/end_session?id_token_hint="+url.QueryEscape(hint))
		if status != http.StatusOK || location != "" || !strings.Contains(body, `action="/end_session"`) {
			t.Errorf("another person's %s hint: %d to %q, want the page that asks", name, status, location)
		}

		if signedOut(grace) {
			t.Fatalf("another person's %s hint signed the browser out", name)
		}

		if where := grace.authorize(""); !strings.Contains(where, "/authorize/callback") {
			t.Fatalf("another person's %s hint ended the sign-in (%q)", name, where)
		}
	}

	// A hint no key of this issuer signed is no better.
	forged := forgedHint(t, mustSoftKey(t, false), "grace@north.example", time.Now().Add(time.Hour))
	if status, _, body := grace.do(http.MethodGet, "/end_session?id_token_hint="+url.QueryEscape(forged)); signedOut(grace) ||
		status == http.StatusFound {
		t.Errorf("a hint of another issuer's key signed grace out: %d %s", status, body)
	}

	// The person's own, expired as the specification allows: no page.
	if status, location, body := ada.do(http.MethodGet, "/end_session?id_token_hint="+url.QueryEscape(expired)); status != http.StatusFound ||
		location != "/signed-out" || !signedOut(ada) {
		t.Errorf("the browser's own expired hint: %d to %q %s, want the sign-out", status, location, body)
	}

	// And a valid one, case-insensitively as the silent sign-in compares.
	ada2 := newBrowser(t, hintServer(t, "ada@north.example", key))
	ada2.signIn()

	upper := forgedHint(t, key, "ADA@north.example", time.Now().Add(time.Hour))
	if status, location, body := ada2.do(http.MethodGet, "/end_session?id_token_hint="+url.QueryEscape(upper)); status != http.StatusFound ||
		location != "/signed-out" || !signedOut(ada2) {
		t.Errorf("the browser's own hint: %d to %q %s, want the sign-out", status, location, body)
	}
}

// The page names the application only from the policy, and escapes what the
// request puts in it.
func TestTheSignOutPageEscapesTheRequestAndNamesTheClient(t *testing.T) {
	t.Parallel()
	server, _ := signInServer(t, "ada@north.example")
	b := newBrowser(t, server)
	b.signIn()

	const evil = `"><script>alert(1)</script>`

	_, _, body := b.do(http.MethodGet, "/end_session?client_id=argocd&state="+url.QueryEscape(evil))
	if strings.Contains(body, "<script>alert") || strings.Contains(body, `"><script`) {
		t.Errorf("the page carries the request's state unescaped: %s", body)
	}

	if !strings.Contains(body, html.EscapeString(evil)) {
		t.Errorf("the page lost the state it has to carry through: %s", body)
	}

	// The name is the policy's, for a client the policy declares; an
	// unknown one is "An application", never the request's words.
	if !strings.Contains(body, "<strong>argocd</strong>") {
		t.Errorf("the page does not name the declared client: %s", body)
	}

	_, _, body = b.do(http.MethodGet, "/end_session?client_id="+url.QueryEscape("<b>x</b>"))
	if strings.Contains(body, "<b>x</b>") || !strings.Contains(body, "An application") {
		t.Errorf("an unknown client was named from the request: %s", body)
	}
}

// A post_logout_redirect_uri the client did not register is refused before
// the page is shown, and a native client's loopback one may differ in port
// only.
func TestPostLogoutRedirectIsCheckedUpFrontAndLoopbackIsStrict(t *testing.T) {
	t.Parallel()

	withLanding := strings.Replace(demo.Policy,
		"    redirects: [http://localhost:8000/callback]\n    requires: [devel:k8s:viewer]",
		"    redirects: [http://localhost:8000/callback]\n    signed_out: [http://127.0.0.1/x]\n    requires: [devel:k8s:viewer]", 1)
	if withLanding == demo.Policy {
		t.Fatal("the demonstration policy changed; this test needs a native client with a landing page")
	}

	for name, tc := range map[string]struct {
		uri string
		ok  bool
	}{
		"as registered":                {"http://127.0.0.1/x", true},
		"another port":                 {"http://127.0.0.1:5000/x", true},
		"another loopback host":        {"http://localhost:5000/x", true},
		"another scheme":               {"https://localhost:1/x", false},
		"another scheme, same host":    {"https://127.0.0.1:1/x", false},
		"another path":                 {"http://127.0.0.1:5000/y", false},
		"a host that is not loopback":  {"http://evil.example/x", false},
		"an IPv4-mapped IPv6 address":  {"http://[::ffff:127.0.0.1]:1/x", false},
		"a name that embeds 127.0.0.1": {"http://127.0.0.1.nip.io:1/x", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// A server of its own: the directory stand-in counts calls without a lock.
			server, _ := signInServerWith(t, "ada@north.example", withLanding)
			b := newBrowser(t, server)
			b.signIn()

			query := "?client_id=local-dev&post_logout_redirect_uri=" + url.QueryEscape(tc.uri)
			status, _, body := b.do(http.MethodGet, "/end_session"+query)

			if !tc.ok {
				if status != http.StatusBadRequest || strings.Contains(body, `action="/end_session"`) {
					t.Errorf("%d %s, want it refused before any page that asks", status, body)
				}

				if signedOut(b) {
					t.Error("a refused request signed the browser out")
				}

				return
			}

			if status != http.StatusOK || !strings.Contains(body, `action="/end_session"`) {
				t.Fatalf("%d %s, want the page that asks", status, body)
			}

			status, location, _ := b.endSession("/end_session" + query)
			if status != http.StatusFound || !strings.HasPrefix(location, tc.uri) {
				t.Errorf("%d to %q, want the post-logout redirect to %s", status, location, tc.uri)
			}
		})
	}
}

// What the loopback table lets through to the library is refused there:
// spellings that are not loopback addresses.
func TestNotLoopbackSpellingsAreRefusedAtAuthorize(t *testing.T) {
	t.Parallel()
	server, _ := signInServer(t, "ada@north.example")

	for _, redirect := range []string{
		"http://LOCALHOST:1/callback", "http://localhost.:1/callback", "http://localhost.evil.com:1/callback",
		"http://127.0.0.1.nip.io:1/callback", "http://[::ffff:127.0.0.1]:1/callback", "http://127.0.0.2:1/callback",
	} {
		b := newBrowser(t, server)
		status, location, body := b.do(http.MethodGet, "/authorize?"+url.Values{
			"client_id": {"local-dev"}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {"openid"},
			"code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "code_challenge_method": {"S256"},
		}.Encode())

		if status != http.StatusBadRequest || location != "" {
			t.Errorf("%s: %d to %q %s, want the 400 page and no redirect", redirect, status, location, body)
		}
	}
}

// Spellings of the unserved routes that a router might still reach, through the
// whole handler.
func TestUnservedRouteSpellingsAreNotFoundThroughTheWholeHandler(t *testing.T) {
	t.Parallel()
	server, _ := signInServer(t, "ada@north.example")

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, path := range []string{
		"/introspect", "/%69ntrospect", "/introspect/", "//introspect", "/INTROSPECT", "/oauth/introspect/",
		"/oauth/%69ntrospect", "/OAuth/Introspect", "/device_authorization/", "/DEVICE_AUTHORIZATION",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader("token=x"))
			if err != nil {
				t.Fatal(err)
			}

			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetBasicAuth("argocd", "right")

			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}

			_ = resp.Body.Close()

			// A router that cleans the path answers with a redirect to the clean
			// one, which is then a 404; anything else is an endpoint.
			if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently ||
				resp.StatusCode == http.StatusPermanentRedirect || resp.StatusCode == http.StatusTemporaryRedirect {
				follow, err := http.NewRequestWithContext(t.Context(), method, server.URL+resp.Header.Get("Location"), strings.NewReader("token=x"))
				if err != nil {
					t.Fatal(err)
				}

				follow.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				follow.SetBasicAuth("argocd", "right")

				resp, err = client.Do(follow)
				if err != nil {
					t.Fatal(err)
				}

				_ = resp.Body.Close()
			}

			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", method, path, resp.StatusCode)
			}
		}
	}
}

// An expired id_token_hint at /authorize is tolerated, as the specification
// has it, and is still the person it names.
func TestAnExpiredIDTokenHintAtAuthorizeIsStillAHint(t *testing.T) {
	t.Parallel()
	key := mustSoftKey(t, false)

	expired := func(sub string) string {
		return "&id_token_hint=" + url.QueryEscape(forgedHint(t, key, sub, time.Now().Add(-time.Hour)))
	}

	b := newBrowser(t, hintServer(t, "ada@north.example", key))
	b.signIn()

	if where := b.authorize(expired("ada@north.example")); !strings.Contains(where, "/authorize/callback") {
		t.Errorf("the person's own expired hint went to %q, want a silent completion", where)
	}

	where := b.authorize(expired("grace@north.example") + "&prompt=none")

	to, err := url.Parse(where)
	if err != nil || to.Query().Get("error") != "login_required" || to.Query().Has("code") {
		t.Errorf("another person's expired hint with prompt=none went to %q, want login_required", where)
	}
}

// A deployment that holds browser sign-ins and has no key to bind the
// confirmation refuses, and ends nothing.
func TestEndSessionWithNoKeyForTheConfirmationFailsClosed(t *testing.T) {
	t.Parallel()

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatal(err)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, &fakeDirectory{}, issuer.NewMemoryState())
	sso := issuer.NewSSO(issuer.NewMemoryState(), time.Hour)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/end_session", nil)

	if issuer.ConfirmedEndSessionForTest(issuer.SignInDeps{Issuer: iss, SSO: sso, State: (*access.StateCodec)(nil)}, rec, req) {
		t.Fatal("a deployment with sign-ins and no confirmation key went on to end the sign-in")
	}

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
