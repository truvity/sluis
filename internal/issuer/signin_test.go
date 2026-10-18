package issuer_test

import (
	"context"
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
	"github.com/truvity/sluis/internal/issuer"
)

// stubPending is an authorization request waiting to be completed, with
// no storage behind it: these routes decide what to do about a request
// before anyone is established, so what they need is what it ASKS.
type stubPending struct {
	asks issuer.Pending
}

func (s stubPending) Pending(string) (issuer.Pending, error)                       { return s.asks, nil }
func (s stubPending) Complete(context.Context, string, issuer.Authenticated) error { return nil }

// signInHandlerWith is signInHandler with a pending request to answer.
func signInHandlerWith(t *testing.T, sso *issuer.SSO, pending stubPending) http.Handler {
	t.Helper()

	mux := http.NewServeMux()
	issuer.SignInRoutes(mux, issuer.SignInDeps{
		SSO:     sso,
		Storage: pending,
		Log:     slog.New(slog.DiscardHandler),
	})

	return mux
}

// signInHandler is the issuer's own pages, with nothing behind them but
// the sign-in store: these routes run BEFORE there is anyone to
// authorize, so none of them needs a provider or a policy.
func signInHandler(t *testing.T, sso *issuer.SSO) http.Handler {
	t.Helper()

	mux := http.NewServeMux()
	issuer.SignInRoutes(mux, issuer.SignInDeps{
		SSO:   sso,
		State: access.NewStateCodec(make([]byte, 32), time.Minute),
		Log:   slog.New(slog.DiscardHandler),
	})

	return mux
}

// `/logout` is the sign-out a person follows, and it must actually end
// the sign-in -- once they have confirmed it on the page that asks.
//
// The console's button pointed here and nothing served it: 404, and the
// session survived. Reported as "even sign-out does not work", which it
// did not.
func TestLogoutEndsTheSignIn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state := issuer.NewMemoryState()
	sso := issuer.NewSSO(state, time.Hour)

	session, cookie, err := sso.Begin(ctx, "ada@north.example", "google")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	handler := signInHandler(t, sso)

	recorder := pressSignOut(t, handler, cookie)
	checkSignedOut(t, recorder, sso, session.ID)
}

// A link, a frame or a redirect from another site is a GET, and a GET must
// not sign anybody out: that is logout CSRF, and with it a way to end the
// sessions the browser opened. It shows a page that asks.
func TestLogoutGetOnlyAsks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sso := issuer.NewSSO(issuer.NewMemoryState(), time.Hour)

	session, cookie, err := sso.Begin(ctx, "ada@north.example", "google")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/logout", nil)
	request.AddCookie(&http.Cookie{Name: issuer.SSOCookieName, Value: cookie})
	recorder := httptest.NewRecorder()
	signInHandler(t, sso).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `method="post" action="/logout"`) {
		t.Fatalf("GET /logout = %d %q, want a page with a sign-out button", recorder.Code, recorder.Body.String())
	}

	if _, found, err := sso.Get(ctx, session.ID); err != nil || !found {
		t.Errorf("a GET ended the sign-in: found=%v err=%v", found, err)
	}

	for _, c := range recorder.Result().Cookies() {
		if c.Name == issuer.SSOCookieName {
			t.Errorf("a GET touched the sign-in cookie: %v", c)
		}
	}
}

// A browser with no sign-in has nothing to be asked about.
func TestLogoutGetWithNoSignInGoesStraightOn(t *testing.T) {
	t.Parallel()

	sso := issuer.NewSSO(issuer.NewMemoryState(), time.Hour)
	recorder := httptest.NewRecorder()
	signInHandler(t, sso).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/logout", nil))

	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/signed-out" {
		t.Errorf("GET /logout with no sign-in = %d to %q, want a redirect to /signed-out",
			recorder.Code, recorder.Header().Get("Location"))
	}
}

// The POST carries the token the page put in its form, bound to the cookie
// that proves the sign-in: without it, or with one made for another
// browser, nothing ends.
func TestLogoutPostWithoutItsTokenEndsNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sso := issuer.NewSSO(issuer.NewMemoryState(), time.Hour)

	session, cookie, err := sso.Begin(ctx, "ada@north.example", "google")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, other, err := sso.Begin(ctx, "grace@north.example", "google")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	handler := signInHandler(t, sso)
	othersToken := csrfOf(t, handler, other)

	for name, form := range map[string]url.Values{
		"no token":          nil,
		"an empty token":    {"csrf": {""}},
		"a made-up token":   {"csrf": {"forged"}},
		"another's token":   {"csrf": {othersToken}},
		"the cookie itself": {"csrf": {cookie}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: issuer.SSOCookieName, Value: cookie})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Errorf("POST /logout = %d, want 400", recorder.Code)
			}

			if _, found, err := sso.Get(ctx, session.ID); err != nil || !found {
				t.Errorf("a POST without the token ended the sign-in: found=%v err=%v", found, err)
			}
		})
	}
}

// csrfOf asks the sign-out page for the token it would give this cookie.
func csrfOf(t *testing.T, handler http.Handler, cookie string) string {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/logout", nil)
	request.AddCookie(&http.Cookie{Name: issuer.SSOCookieName, Value: cookie})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	found := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(recorder.Body.String())
	if found == nil {
		t.Fatalf("the sign-out page has no token: %d %s", recorder.Code, recorder.Body.String())
	}

	return html.UnescapeString(found[1])
}

// pressSignOut is a person asking for the page and pressing its button.
func pressSignOut(t *testing.T, handler http.Handler, cookie string) *httptest.ResponseRecorder {
	t.Helper()

	form := url.Values{"csrf": {csrfOf(t, handler, cookie)}}
	request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: issuer.SSOCookieName, Value: cookie})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

// checkSignedOut checks the whole of what sign-out must do: redirect, end
// the record, clear the cookie.
func checkSignedOut(t *testing.T, recorder *httptest.ResponseRecorder, sso *issuer.SSO, id string) {
	t.Helper()

	if recorder.Code != http.StatusFound {
		t.Fatalf("POST /logout = %d, want a redirect", recorder.Code)
	}
	if to := recorder.Header().Get("Location"); to != "/signed-out" {
		t.Errorf("landed on %q, want /signed-out", to)
	}

	// The half that was missing: the record itself.
	if _, found, err := sso.Get(context.Background(), id); err != nil || found {
		t.Errorf("the sign-in survived sign-out: found=%v err=%v", found, err)
	}

	// And the cookie is cleared, so a request carrying the old value
	// does not look signed in until something checks the store.
	cleared := false
	for _, c := range recorder.Result().Cookies() {
		if c.Name == issuer.SSOCookieName && c.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the session cookie was not cleared")
	}
}

// `prompt=none` with nobody signed in answers the CLIENT, not the person.
//
// OpenID Connect Core 3.1.2.6 requires `login_required` at the redirect
// URI. This rendered a page saying so, which is worse than it sounds:
// the caller of `prompt=none` is usually a hidden iframe doing a silent
// renewal, and it cannot read an HTML page, has nobody to show it to,
// and waits until it times out. Conformance called it "expected an
// error but did not get one" — the same fact from the other side.
func TestPromptNoneWithNoSessionRefusesToTheClient(t *testing.T) {
	t.Parallel()

	handler := signInHandlerWith(t, nil, stubPending{
		asks: issuer.Pending{
			ForbidsUI:   true,
			RedirectURI: "https://rp.example/callback",
			State:       "the-client-state",
		},
	})

	request := httptest.NewRequest(http.MethodGet, "/login?auth=abc", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("GET /login = %d, want a redirect to the client", recorder.Code)
	}

	to, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}

	if got, want := to.Scheme+"://"+to.Host+to.Path, "https://rp.example/callback"; got != want {
		t.Errorf("redirected to %q, want the client's own %q", got, want)
	}

	if got := to.Query().Get("error"); got != "login_required" {
		t.Errorf("error = %q, want login_required", got)
	}

	// The state is the client's and must come back, or the client cannot
	// match the answer to the request it sent.
	if got := to.Query().Get("state"); got != "the-client-state" {
		t.Errorf("state = %q, want it echoed", got)
	}
}

// Signing out ends what the browser opened, not only the sign-in.
//
// The design leaned on those sessions dying "at their next refresh".
// They do — a revoked session's refresh is refused — but nothing was
// revoking them, so every console the person had opened kept its own
// session until it happened to refresh, and a sign-out that reported
// success left access in place.
func TestSignOutEndsWhatTheBrowserOpened(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state := issuer.NewMemoryState()
	sso := issuer.NewSSO(state, time.Hour)
	sessions := issuer.NewSessions(state, time.Hour, 0)

	session, cookie, err := sso.Begin(ctx, "ada@north.example", "google")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	for _, client := range []string{"hubble", "kargo"} {
		if _, err = sessions.Record(ctx, issuer.Opened{
			Identity: "ada@north.example", ClientID: client,
			How: issuer.HowCode, Token: "t-" + client, SSO: session.ID,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	mux := http.NewServeMux()
	issuer.SignInRoutes(mux, issuer.SignInDeps{
		SSO:    sso,
		Issuer: issuer.NewForSessionsTest(sessions),
		State:  access.NewStateCodec(make([]byte, 32), time.Minute),
		Log:    slog.New(slog.DiscardHandler),
	})

	pressSignOut(t, mux, cookie)

	left, err := sessions.List(ctx, issuer.Query{Identity: "ada@north.example"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(left) != 0 {
		t.Errorf("after sign-out %d session(s) remain: %v", len(left), left)
	}
}
