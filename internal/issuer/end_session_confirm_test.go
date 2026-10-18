package issuer_test

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
)

// OpenID Connect RP-Initiated Logout 1.0 2: without an id_token_hint nothing
// says the request is the relying party's, so the endpoint asks. A GET ends
// nothing and shows the button; only the POST the button makes, with the token
// bound to the sign-in cookie, ends the sign-in, and it then does what the
// request asked.
func TestEndSessionWithoutAHintAsksFirst(t *testing.T) {
	t.Parallel()
	server, _ := signInServer(t, "ada@north.example")
	b := newBrowser(t, server)
	b.signIn()

	stillSignedIn := func(stage string) {
		t.Helper()

		if b.cookies[issuer.SSOCookieName] == "" {
			t.Fatalf("%s: the browser's sign-in cookie is gone", stage)
		}

		if where := b.authorize(""); !strings.Contains(where, "/authorize/callback") {
			t.Fatalf("%s: the sign-in no longer answers silently (%q)", stage, where)
		}
	}

	query := "?client_id=argocd&state=xyz&post_logout_redirect_uri=" + url.QueryEscape("https://argocd.demo.example/")

	// The GET asks and ends nothing.
	status, location, body := b.do(http.MethodGet, "/end_session"+query)
	if status != http.StatusOK || location != "" || !strings.Contains(body, `action="/end_session"`) {
		t.Fatalf("GET = %d to %q, want the page that asks: %s", status, location, body)
	}

	stillSignedIn("after the GET")

	// The page carries the request through, and a token.
	form := url.Values{}
	for _, field := range hiddenField.FindAllStringSubmatch(body, -1) {
		form.Set(field[1], html.UnescapeString(field[2]))
	}

	if form.Get("csrf") == "" || form.Get("client_id") != "argocd" || form.Get("state") != "xyz" ||
		form.Get("post_logout_redirect_uri") != "https://argocd.demo.example/" {
		t.Fatalf("the page's form = %v, want the token and the request's own parameters", form)
	}

	// A POST that did not come from the page ends nothing.
	for name, bad := range map[string]url.Values{
		"no token":    {"client_id": {"argocd"}},
		"a bad token": {"client_id": {"argocd"}, "csrf": {"nope"}},
	} {
		if status, _, body, _ = b.post("/end_session", bad); status != http.StatusBadRequest {
			t.Errorf("POST with %s = %d %s, want 400", name, status, body)
		}

		stillSignedIn("after the POST with " + name)
	}

	// Another browser's token is no use here.
	other := newBrowser(t, server)
	other.signIn()

	_, _, otherBody := other.do(http.MethodGet, "/end_session")
	otherForm := url.Values{}

	for _, field := range hiddenField.FindAllStringSubmatch(otherBody, -1) {
		otherForm.Set(field[1], html.UnescapeString(field[2]))
	}

	if status, _, _, _ = b.post("/end_session", otherForm); status != http.StatusBadRequest {
		t.Errorf("POST with another browser's token = %d, want 400", status)
	}

	stillSignedIn("after the POST with another browser's token")

	// The button ends the sign-in, and the request is carried out.
	status, location, body, _ = b.post("/end_session", form)
	if status != http.StatusFound || !strings.HasPrefix(location, "https://argocd.demo.example/") ||
		!strings.Contains(location, "state=xyz") {
		t.Fatalf("POST from the page = %d to %q %s, want the post-logout redirect with the state", status, location, body)
	}

	if b.cookies[issuer.SSOCookieName] != "" {
		t.Error("the sign-out left the sign-in cookie behind")
	}

	if where := b.authorize(""); strings.Contains(where, "/authorize/callback") {
		t.Errorf("the sign-in survived the sign-out: %q", where)
	}
}

// A browser with no sign-in has nothing to ask about.
func TestEndSessionWithoutASignInDoesNotAsk(t *testing.T) {
	t.Parallel()
	server, _ := signInServer(t, "ada@north.example")
	b := newBrowser(t, server)

	if status, location, body := b.do(http.MethodGet, "/end_session"); status != http.StatusFound || location != "/signed-out" {
		t.Errorf("end_session = %d to %q %s, want 302 to /signed-out", status, location, body)
	}
}
