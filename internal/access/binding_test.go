package access_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
)

func codec(key string) *access.StateCodec {
	return access.NewStateCodec([]byte(strings.Repeat(key, 32)), time.Minute)
}

func requestWith(value string, secure bool) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	if value != "" {
		request.AddCookie(&http.Cookie{Name: access.CookieNameFor(access.AuthRequestCookieName, secure), Value: value})
	}

	return request
}

func TestARequestIsBoundToTheBrowserThatHoldsItsTag(t *testing.T) {
	t.Parallel()

	c := codec("a")

	value, err := c.AddRequest("", "request-1")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(value, "request-1") {
		t.Errorf("the cookie carries the request id: %q", value)
	}

	if !c.RequestStartedHere(requestWith(value, false), "request-1", false) {
		t.Error("the browser that holds the tag was not recognised")
	}

	for name, request := range map[string]*http.Request{
		"no cookie":     requestWith("", false),
		"another's tag": requestWith(mustAdd(t, c, "", "request-2"), false),
		"a guess":       requestWith("AAAAAAAAAAAAAAAAAAAAAA", false),
		"garbage":       requestWith("...", false),
	} {
		if c.RequestStartedHere(request, "request-1", false) {
			t.Errorf("%s was taken for the browser that started the request", name)
		}
	}

	// An empty id is bound to nothing, however the cookie is made.
	if c.RequestStartedHere(requestWith(value, false), "", false) {
		t.Error("an empty request id was taken as bound")
	}

	// A tag made under another key is not this installation's.
	if codec("b").RequestStartedHere(requestWith(value, false), "request-1", false) {
		t.Error("a tag from another key was accepted")
	}

	// And a tag is not a CSRF token or a state, nor the other way round.
	token, err := c.CSRFToken("logout", "request-1")
	if err != nil {
		t.Fatal(err)
	}

	if c.RequestStartedHere(requestWith(token[:22], false), "request-1", false) {
		t.Error("a CSRF token stood for a request tag")
	}
}

func mustAdd(t *testing.T, c *access.StateCodec, held, id string) string {
	t.Helper()

	value, err := c.AddRequest(held, id)
	if err != nil {
		t.Fatal(err)
	}

	return value
}

func TestSeveralRequestsCanBeOpenAtOnceButNotWithoutLimit(t *testing.T) {
	t.Parallel()

	c := codec("a")

	held := ""
	for _, id := range []string{"r1", "r2", "r3"} {
		held = mustAdd(t, c, held, id)
	}

	for _, id := range []string{"r1", "r2", "r3"} {
		if !c.RequestStartedHere(requestWith(held, false), id, false) {
			t.Errorf("a second tab stranded %s", id)
		}
	}

	// The same request twice is one tag.
	if again := mustAdd(t, c, held, "r2"); strings.Count(again, ".") != 2 {
		t.Errorf("re-adding a request grew the cookie: %q", again)
	}

	// The cookie stays small: the oldest is forgotten.
	for i := range 40 {
		held = mustAdd(t, c, held, "n"+strings.Repeat("x", i))
	}

	if len(held) > 8*(22+1) {
		t.Errorf("the cookie grew to %d bytes", len(held))
	}

	if c.RequestStartedHere(requestWith(held, false), "r1", false) {
		t.Error("the oldest request was still bound after many newer ones")
	}
}

func TestTheRequestCookieIsHostLockedWhereCookiesAreSecure(t *testing.T) {
	t.Parallel()

	secure := access.AuthRequestCookie("v", true, time.Minute)
	if secure.Name != "__Host-"+access.AuthRequestCookieName || !secure.Secure || !secure.HttpOnly ||
		secure.Path != "/" || secure.Domain != "" || secure.SameSite != http.SameSiteLaxMode {
		t.Errorf("secure cookie = %+v", secure)
	}

	plain := access.AuthRequestCookie("v", false, time.Minute)
	if plain.Name != access.AuthRequestCookieName || plain.Secure {
		t.Errorf("plain cookie = %+v", plain)
	}

	c := codec("a")
	value := mustAdd(t, c, "", "r1")

	// Read under the name it was written under, and no other.
	if !c.RequestStartedHere(requestWith(value, true), "r1", true) {
		t.Error("the __Host- cookie was not read")
	}

	if c.RequestStartedHere(requestWith(value, false), "r1", true) {
		t.Error("an unprefixed cookie was taken where cookies are secure: a sibling host could have set it")
	}
}

func TestACSRFTokenIsBoundToItsPurposeAndItsValue(t *testing.T) {
	t.Parallel()

	c := codec("a")

	token, err := c.CSRFToken("logout", "cookie-1")
	if err != nil {
		t.Fatal(err)
	}

	if !c.CSRFValid("logout", "cookie-1", token) {
		t.Fatal("a token was not valid for what it was made for")
	}

	for name, ok := range map[string]bool{
		"another value":   c.CSRFValid("logout", "cookie-2", token),
		"another purpose": c.CSRFValid("other", "cookie-1", token),
		"empty":           c.CSRFValid("logout", "cookie-1", ""),
		"no value":        c.CSRFValid("logout", "", token),
		"another key":     codec("b").CSRFValid("logout", "cookie-1", token),
		"nil codec":       (*access.StateCodec)(nil).CSRFValid("logout", "cookie-1", token),
	} {
		if ok {
			t.Errorf("a token was valid for %s", name)
		}
	}
}
