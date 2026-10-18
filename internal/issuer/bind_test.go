package issuer_test

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
)

// startRequest makes an authorization request in b's browser and returns
// the sign-in address /authorize sent it to.
func startRequest(t *testing.T, b *browser) string {
	t.Helper()

	where, _ := startRequestWithHeader(t, b)

	return where
}

// startRequestWithHeader is startRequest, with the response's headers too.
func startRequestWithHeader(t *testing.T, b *browser) (string, http.Header) {
	t.Helper()

	sum := sha256.Sum256([]byte(pkceVerifier))
	query := url.Values{
		"client_id":             {"local-dev"},
		"redirect_uri":          {"http://localhost:8000/callback"},
		"response_type":         {"code"},
		"scope":                 {"openid profile email"},
		"state":                 {"bind-test"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}

	status, where, body, header := b.send(http.MethodGet, "/authorize?"+query.Encode(), nil)
	if status != http.StatusFound || !strings.HasPrefix(where, "/login?auth=") {
		t.Fatalf("/authorize: %d to %q (%s)", status, where, body)
	}

	return where, header
}

// The request id is in a URL, so a link to somebody else's request must not
// be completed by the sign-in of whoever opens it. This is the attack: an
// attacker starts /authorize and sends the victim the /login link; the
// victim's silent sign-in used to complete the attacker's request as the
// victim, and the code went to the attacker's redirect.
func TestAnAuthorizationRequestCompletesOnlyInTheBrowserThatStartedIt(t *testing.T) {
	t.Parallel()

	server, _ := signInServer(t, "ada@north.example")
	attacker := newBrowser(t, server)
	victim := newBrowser(t, server)
	victim.signIn() // a live sign-in, which answers /authorize silently

	link := startRequest(t, attacker)
	request := strings.TrimPrefix(link, "/login?auth=")
	provider := "/login/google/start?auth=" + request
	callback := "/authorize/callback?id=" + request

	// The same request in the victim's browser: nothing completes.
	for name, path := range map[string]string{
		"the login link":     link,
		"the provider start": provider,
		"the code hand-over": callback,
	} {
		status, where, body := victim.do(http.MethodGet, path)
		if status != http.StatusBadRequest {
			t.Errorf("%s in the victim's browser = %d to %q, want a 400 page", name, status, where)
		}

		if where != "" {
			t.Errorf("%s in the victim's browser redirected to %q", name, where)
		}

		if !strings.Contains(body, "This sign-in was started in another browser") {
			t.Errorf("%s: the page does not say so: %s", name, body)
		}

		if strings.Contains(body, request) {
			t.Errorf("%s: the page repeats the request id", name)
		}
	}

	// And it is still waiting: the attacker's own browser, which started it,
	// goes on to the provider (no sign-in of its own, so not silently).
	status, where, _ := attacker.do(http.MethodGet, link)
	if status != http.StatusFound || !strings.Contains(where, "/login/google/start") {
		t.Errorf("the request in its own browser = %d to %q, want the provider", status, where)
	}
}

// The normal flow is the one the cookie is made for: the browser that
// started the request is silently completed, and a second tab does not
// strand the first.
func TestTheBrowserThatStartedTheRequestCompletesItSilently(t *testing.T) {
	t.Parallel()

	server, _ := signInServer(t, "ada@north.example")
	b := newBrowser(t, server)
	b.signIn()

	first := startRequest(t, b)
	second := startRequest(t, b) // another tab

	for _, link := range []string{first, second} {
		status, where, body := b.do(http.MethodGet, link)
		if status != http.StatusFound || !strings.Contains(where, "/authorize/callback") {
			t.Fatalf("%s = %d to %q (%s), want the silent completion", link, status, where, body)
		}

		// The hand-over from the same browser reaches the library, which
		// answers with the code at the client's redirect.
		status, where, body = b.do(http.MethodGet, strings.TrimPrefix(where, "http://issuer.example"))
		if status != http.StatusFound || !strings.HasPrefix(where, "http://localhost:8000/callback?code=") {
			t.Fatalf("the hand-over = %d to %q (%s), want the code at the client", status, where, body)
		}
	}
}

// What /authorize sets is short-lived, and carries nothing but a MAC of the
// request id.
func TestTheAuthorizeCookieIsBoundToTheRequestAndNothingElse(t *testing.T) {
	t.Parallel()

	server, _ := signInServer(t, "ada@north.example")
	b := newBrowser(t, server)

	link, header := startRequestWithHeader(t, b)
	request := strings.TrimPrefix(link, "/login?auth=")

	var set *http.Cookie

	for _, line := range header.Values("Set-Cookie") {
		parsed, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}

		if parsed.Name == access.AuthRequestCookieName {
			set = parsed
		}
	}

	if set == nil {
		t.Fatalf("/authorize set no request cookie: %v", header.Values("Set-Cookie"))
	}

	if strings.Contains(set.Value, request) {
		t.Errorf("the cookie carries the request id: %q", set.Value)
	}

	if !set.HttpOnly || set.SameSite != http.SameSiteLaxMode || set.Path != "/" || set.Domain != "" {
		t.Errorf("cookie attributes = %+v, want HttpOnly, SameSite=Lax, Path=/ and no Domain", set)
	}

	if set.MaxAge <= 0 || set.MaxAge > int((30*time.Minute).Seconds()) {
		t.Errorf("cookie MaxAge = %d, want within the request's own lifetime", set.MaxAge)
	}

	// A refused /authorize carries no request, so it binds nothing.
	bad := url.Values{"client_id": {"local-dev"}, "redirect_uri": {"http://localhost:8000/callback"}, "response_type": {"nonsense"}}
	fresh := newBrowser(t, server)
	fresh.do(http.MethodGet, "/authorize?"+bad.Encode())

	if got := fresh.cookies[access.AuthRequestCookieName]; got != "" {
		t.Errorf("a refused /authorize set the request cookie %q", got)
	}
}
