package server

import (
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
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
	console := githubConsole(t, nil)
	console.deps.Audit = audittest.New(t)
	sessions, err := access.NewSessions(make([]byte, access.SessionKeyBytes), time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	codec := access.NewStateCodec(make([]byte, 32), time.Minute)
	return NewConsoleServer(ConsoleServerDeps{
		Console: console, Authorizer: console.deps.Authorizer, Sessions: sessions, State: codec,
		Recovery: oneRecovery{}, Log: slog.New(slog.DiscardHandler),
	}).Handler(), codec
}

var (
	loginCookie   = access.CookieNameFor(access.LoginCookieName, true)
	sessionCookie = access.CookieNameFor(access.CookieName, true)
	formState     = regexp.MustCompile(`name="state" value="([^"]+)"`)
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

	issue := func(t *testing.T, codec *access.StateCodec, bind string) string {
		t.Helper()
		state, err := codec.Issue(bind)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}

	for name, forge := range map[string]func(t *testing.T, codec *access.StateCodec) (contentType, body string, cookies []*http.Cookie){
		"no login cookie": func(t *testing.T, codec *access.StateCodec) (string, string, []*http.Cookie) {
			form := url.Values{"state": {issue(t, codec, recoveryStateBinding)}, "proof": {"the-proof"}}
			return urlencoded, form.Encode(), nil
		},
		"a wrong login cookie": func(t *testing.T, codec *access.StateCodec) (string, string, []*http.Cookie) {
			form := url.Values{"state": {issue(t, codec, recoveryStateBinding)}, "proof": {"the-proof"}}
			return urlencoded, form.Encode(), []*http.Cookie{{Name: loginCookie, Value: "not-the-state"}}
		},
		"another flow's login cookie": func(t *testing.T, codec *access.StateCodec) (string, string, []*http.Cookie) {
			form := url.Values{"state": {issue(t, codec, recoveryStateBinding)}, "proof": {"the-proof"}}
			return urlencoded, form.Encode(), []*http.Cookie{{Name: loginCookie, Value: issue(t, codec, "")}}
		},
		"a provider round trip's state as both": func(t *testing.T, codec *access.StateCodec) (string, string, []*http.Cookie) {
			provider := issue(t, codec, "")
			form := url.Values{"state": {provider}, "proof": {"the-proof"}}
			return urlencoded, form.Encode(), []*http.Cookie{{Name: loginCookie, Value: provider}}
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

	login, ok := setCookie(page, loginCookie)
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
	if c, ok := setCookie(response, loginCookie); !ok || c.MaxAge >= 0 {
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
