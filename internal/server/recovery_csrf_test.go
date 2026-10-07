package server

import (
	"context"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
)

// A console's recovery form post is accepted only from the browser the
// sign-in page was served to. The form used to carry the proof and nothing
// else, so whoever held a valid proof could post it from a page of theirs
// and sign a victim's browser into this console as the ServiceAccount (a
// login CSRF): the victim then works in a session somebody else chose.

// recoveryFormServer is a console with recovery and a state codec, as every
// real deployment wires it, and the codec for a test to forge states with.
func recoveryFormServer(t *testing.T) (http.Handler, *access.StateCodec) {
	t.Helper()
	handler, codec, _ := recoveryFormServerWith(t, nil)
	return handler, codec
}

// identifyingNobody is a directory sign-in that counts the callbacks that
// reached it and identifies nobody.
type identifyingNobody struct {
	stubSignIn
	mu    sync.Mutex
	calls int
}

func (c *identifyingNobody) Identify(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return "", errors.New("the stand-in identifies nobody")
}

func (c *identifyingNobody) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// recoveryFormServerWith is [recoveryFormServer] with a directory sign-in
// beside the recovery, when one is given.
func recoveryFormServerWith(t *testing.T, signIn *identifyingNobody) (http.Handler, *access.StateCodec, *identifyingNobody) {
	t.Helper()
	console := githubConsole(t, nil)
	console.deps.Audit = audittest.New(t)
	sessions, err := access.NewSessions(make([]byte, access.SessionKeyBytes), time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	codec := access.NewStateCodec(make([]byte, 32), time.Minute)
	deps := ConsoleServerDeps{
		Console: console, Authorizer: console.deps.Authorizer, Sessions: sessions, State: codec,
		Recovery: oneRecovery{}, Log: slog.New(slog.DiscardHandler),
	}
	if signIn != nil {
		deps.Connectors, deps.SignIn = []Connector{signIn}, true
	}
	return NewConsoleServer(deps).Handler(), codec, signIn
}

var (
	recoveryCookie = access.CookieNameFor(access.RecoveryCookieName, true)
	loginCookie    = access.CookieNameFor(access.LoginCookieName, true)
	sessionCookie  = access.CookieNameFor(access.CookieName, true)
	formState      = regexp.MustCompile(`name="state" value="([^"]+)"`)
)

// postRecoveryForm posts the recovery form as a browser would, with the
// cookies given.
func postRecoveryForm(handler http.Handler, contentType, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/login/recovery", strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "text/html")
	for _, c := range cookies {
		request.AddCookie(c)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func setCookie(response *httptest.ResponseRecorder, name string) (*http.Cookie, bool) {
	for _, c := range response.Result().Cookies() {
		if c.Name == name {
			return c, true
		}
	}
	return nil, false
}

func TestAConsoleRecoveryFormPostFromAnotherBrowserIsRefused(t *testing.T) {
	t.Parallel()

	const urlencoded = "application/x-www-form-urlencoded"

	issue := func(t *testing.T, codec *access.StateCodec, purpose string) string {
		t.Helper()
		state, err := codec.IssueAs(access.Binding{Owner: purpose})
		if err != nil {
			t.Fatal(err)
		}
		return state
	}

	for name, forge := range map[string]func(t *testing.T, codec *access.StateCodec) (contentType, body string, cookies []*http.Cookie){
		"no login cookie": func(t *testing.T, codec *access.StateCodec) (string, string, []*http.Cookie) {
			form := url.Values{"state": {issue(t, codec, access.RecoveryPurpose)}, "proof": {"the-proof"}}
			return urlencoded, form.Encode(), nil
		},
		"a wrong login cookie": func(t *testing.T, codec *access.StateCodec) (string, string, []*http.Cookie) {
			form := url.Values{"state": {issue(t, codec, access.RecoveryPurpose)}, "proof": {"the-proof"}}
			return urlencoded, form.Encode(), []*http.Cookie{{Name: recoveryCookie, Value: "not-the-state"}}
		},
		"another flow's login cookie": func(t *testing.T, codec *access.StateCodec) (string, string, []*http.Cookie) {
			form := url.Values{"state": {issue(t, codec, access.RecoveryPurpose)}, "proof": {"the-proof"}}
			return urlencoded, form.Encode(), []*http.Cookie{{Name: recoveryCookie, Value: issue(t, codec, "")}}
		},
		"a provider round trip's state as both": func(t *testing.T, codec *access.StateCodec) (string, string, []*http.Cookie) {
			provider := issue(t, codec, "")
			form := url.Values{"state": {provider}, "proof": {"the-proof"}}
			return urlencoded, form.Encode(), []*http.Cookie{
				{Name: recoveryCookie, Value: provider}, {Name: loginCookie, Value: provider},
			}
		},
		"no state at all": func(*testing.T, *access.StateCodec) (string, string, []*http.Cookie) {
			return urlencoded, url.Values{"proof": {"the-proof"}}.Encode(), nil
		},
		"JSON in a text/plain post": func(*testing.T, *access.StateCodec) (string, string, []*http.Cookie) {
			return "text/plain", `{"proof":"the-proof"}`, nil
		},
		"a multipart form": func(*testing.T, *access.StateCodec) (string, string, []*http.Cookie) {
			body := "--b\r\nContent-Disposition: form-data; name=\"proof\"\r\n\r\nthe-proof\r\n--b--\r\n"
			return "multipart/form-data; boundary=b", body, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			handler, codec := recoveryFormServer(t)
			contentType, body, cookies := forge(t, codec)

			response := postRecoveryForm(handler, contentType, body, cookies...)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "did not start in this browser") {
				t.Errorf("recovery = %d %q, want 400 saying it did not start here", response.Code, response.Body.String())
			}
			if c, ok := setCookie(response, sessionCookie); ok && c.Value != "" {
				t.Error("a forged recovery post was handed a console session")
			}
		})
	}
}

// The real form still works: the sign-in page pins its state to the browser
// as the login cookie, the post from that browser signs in, and the cookie
// is spent with it. The programmatic channels need no page: a bearer, and a
// JSON body under a JSON content type.
func TestTheConsoleRecoveryFormWorksFromTheBrowserItWasServedTo(t *testing.T) {
	t.Parallel()
	handler, _ := recoveryFormServer(t)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/login", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("sign-in page = %d", page.Code)
	}
	match := formState.FindStringSubmatch(page.Body.String())
	if match == nil {
		t.Fatalf("the sign-in page rendered no recovery state: %s", page.Body.String())
	}
	state := html.UnescapeString(match[1])

	login, ok := setCookie(page, recoveryCookie)
	if !ok || login.Value != state {
		t.Fatalf("the page's login cookie = %v, want the form's state", login)
	}
	if !login.HttpOnly || login.SameSite != http.SameSiteLaxMode || login.MaxAge <= 0 || login.MaxAge > int(signInWindow.Seconds()) {
		t.Errorf("the login cookie is not HttpOnly, SameSite=Lax and short-lived: %v", login)
	}

	form := url.Values{"state": {state}, "proof": {"the-proof"}}
	response := postRecoveryForm(handler, "application/x-www-form-urlencoded", form.Encode(),
		&http.Cookie{Name: login.Name, Value: login.Value})
	if response.Code != http.StatusFound {
		t.Fatalf("recovery = %d %q, want a redirect into the console", response.Code, response.Body.String())
	}
	if c, ok := setCookie(response, sessionCookie); !ok || c.Value == "" {
		t.Error("the recovery from the page's own browser was handed no session")
	}
	if c, ok := setCookie(response, recoveryCookie); !ok || c.MaxAge >= 0 {
		t.Errorf("the login cookie was not spent: %v", c)
	}

	for name, request := range map[string]*http.Request{
		"bearer": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/login/recovery", nil)
			r.Header.Set("Authorization", "Bearer the-proof")
			return r
		}(),
		"JSON": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/login/recovery", strings.NewReader(`{"proof":"the-proof"}`))
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
			return r
		}(),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Errorf("%s recovery = %d %q, want 204", name, response.Code, response.Body.String())
		}
	}
}

// A recovery form's state is refused by the provider callback, even from
// the browser that holds it in every cookie: each door takes only its own.
func TestTheConsoleProviderCallbackRefusesARecoveryState(t *testing.T) {
	t.Parallel()
	handler, codec, signIn := recoveryFormServerWith(t, &identifyingNobody{})

	recovery, err := codec.IssueAs(access.Binding{Owner: access.RecoveryPurpose})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/login/google/callback?code=x&state="+url.QueryEscape(recovery), nil)
	request.AddCookie(&http.Cookie{Name: loginCookie, Value: recovery})
	request.AddCookie(&http.Cookie{Name: recoveryCookie, Value: recovery})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Errorf("callback with a recovery state = %d %q, want 400", response.Code, response.Body.String())
	}
	if n := signIn.count(); n != 0 {
		t.Errorf("a recovery state reached the directory %d times", n)
	}
}

// Following a provider button from the sign-in page leaves the recovery form
// on it working: the two keep their states in cookies of their own.
func TestAConsoleProviderStartDoesNotBreakAPendingRecoveryForm(t *testing.T) {
	t.Parallel()
	handler, _, _ := recoveryFormServerWith(t, &identifyingNobody{})
	jar := map[string]string{}
	keep := func(response *httptest.ResponseRecorder) {
		for _, c := range response.Result().Cookies() {
			jar[c.Name] = c.Value
		}
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/login", nil))
	keep(page)
	match := formState.FindStringSubmatch(page.Body.String())
	if match == nil {
		t.Fatalf("the sign-in page rendered no recovery state: %s", page.Body.String())
	}

	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/login/google/start", nil))
	keep(start)
	if start.Code != http.StatusFound || jar[loginCookie] == "" {
		t.Fatalf("provider start = %d, cookies %v", start.Code, jar)
	}

	cookies := make([]*http.Cookie, 0, len(jar))
	for name, value := range jar {
		cookies = append(cookies, &http.Cookie{Name: name, Value: value})
	}
	form := url.Values{"state": {html.UnescapeString(match[1])}, "proof": {"the-proof"}}
	response := postRecoveryForm(handler, "application/x-www-form-urlencoded", form.Encode(), cookies...)
	if response.Code != http.StatusFound {
		t.Errorf("recovery after a provider start = %d %q, want it to sign in", response.Code, response.Body.String())
	}
}

// A recovery post is bounded before it is read: a body of megabytes is
// refused unread rather than parsed, whatever its shape.
func TestAnOversizedConsoleRecoveryPostIsRefused(t *testing.T) {
	t.Parallel()
	handler, _ := recoveryFormServer(t)
	big := strings.Repeat("x", 1<<20)

	for name, post := range map[string]struct{ contentType, body string }{
		"urlencoded": {"application/x-www-form-urlencoded", url.Values{"proof": {big}}.Encode()},
		"multipart": {"multipart/form-data; boundary=b",
			"--b\r\nContent-Disposition: form-data; name=\"proof\"\r\n\r\n" + big + "\r\n--b--\r\n"},
	} {
		response := postRecoveryForm(handler, post.contentType, post.body)
		if response.Code != http.StatusRequestEntityTooLarge && response.Code != http.StatusBadRequest {
			t.Errorf("a 1 MiB %s recovery post = %d, want 413 or 400", name, response.Code)
		}
		if name == "urlencoded" && response.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("a 1 MiB urlencoded recovery post = %d, want 413", response.Code)
		}
		if c, ok := setCookie(response, sessionCookie); ok && c.Value != "" {
			t.Errorf("an oversized %s post was handed a session", name)
		}
	}
}
