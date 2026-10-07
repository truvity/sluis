package issuer_test

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
	"github.com/truvity/sluis/internal/issuer"
)

// countingCompleter completes every request and counts how many it did.
type countingCompleter struct {
	stubPending

	mu        sync.Mutex
	completed int
}

func (c *countingCompleter) Complete(context.Context, string, issuer.Authenticated) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.completed++

	return nil
}

func (c *countingCompleter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.completed
}

// recoveryDoor is the sign-in routes with a recovery that accepts any proof,
// over a store that counts the requests it completed.
func recoveryDoor(t *testing.T) (http.Handler, *access.StateCodec, *countingCompleter, *issuer.SSO) {
	t.Helper()

	codec := access.NewStateCodec(make([]byte, 32), time.Minute)
	storage := &countingCompleter{}
	sso := issuer.NewSSO(issuer.NewMemoryState(), time.Hour)
	mux := http.NewServeMux()
	issuer.SignInRoutes(mux, issuer.SignInDeps{
		Recovery: acceptingRecovery{}, Storage: storage, State: codec, SSO: sso,
		Return: func(context.Context, string) string { return "/done" },
		Log:    slog.New(slog.DiscardHandler),
	})

	return mux, codec, storage, sso
}

func postRecovery(handler http.Handler, state string, cookie *http.Cookie) *httptest.ResponseRecorder {
	form := url.Values{"state": {state}, "proof": {"a-good-token"}}
	request := httptest.NewRequest(http.MethodPost, "/login/recovery", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if cookie != nil {
		request.AddCookie(cookie)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	return response
}

// A recovery POST is accepted only from the browser the form was served to.
// The signed state alone is no proof of that -- anybody can load the sign-in
// page for a request of their own and read it off the form -- so the holder
// of a valid proof could otherwise post it from a page of theirs and sign a
// victim's browser in as the ServiceAccount.
func TestARecoveryPostFromAnotherBrowserIsRefused(t *testing.T) {
	t.Parallel()

	for name, cookie := range map[string]func(codec *access.StateCodec) *http.Cookie{
		"no login cookie": func(*access.StateCodec) *http.Cookie { return nil },
		"an empty login cookie": func(*access.StateCodec) *http.Cookie {
			return &http.Cookie{Name: access.RecoveryCookieName, Value: ""}
		},
		"another flow's login cookie": func(codec *access.StateCodec) *http.Cookie {
			other, err := codec.IssueAs(access.Binding{Bind: "req-attacker", Owner: access.RecoveryPurpose})
			if err != nil {
				panic(err)
			}

			return &http.Cookie{Name: access.RecoveryCookieName, Value: other}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler, codec, storage, sso := recoveryDoor(t)

			state, err := codec.IssueAs(access.Binding{Bind: "req-victim", Owner: access.RecoveryPurpose})
			if err != nil {
				t.Fatal(err)
			}

			response := postRecovery(handler, state, cookie(codec))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "did not start in this browser") {
				t.Errorf("recovery = %d %q, want 400 saying it did not start here", response.Code, response.Body.String())
			}

			if n := storage.count(); n != 0 {
				t.Errorf("%d requests were completed by a forged recovery", n)
			}

			for _, c := range response.Result().Cookies() {
				if c.Name == issuer.SSOCookieName && c.Value != "" {
					t.Errorf("the forged recovery handed the browser a sign-in: %v", c)
				}
			}

			if open, err := sso.List(context.Background(), ""); err != nil || len(open) != 0 {
				t.Errorf("sign-ins after a forged recovery = %v, %v; want none", open, err)
			}
		})
	}
}

var recoveryStateField = regexp.MustCompile(`name="state" value="([^"]+)"`)

// The real recovery page still works: the chooser that serves the form pins
// its state to the browser as the login cookie, the POST from that browser
// completes, and the cookie is spent with it.
func TestTheRecoveryFormWorksFromTheBrowserItWasServedTo(t *testing.T) {
	t.Parallel()

	handler, _, storage, _ := recoveryDoor(t)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/login?auth=req-1", nil))

	if page.Code != http.StatusOK {
		t.Fatalf("chooser = %d", page.Code)
	}

	match := recoveryStateField.FindStringSubmatch(page.Body.String())
	if match == nil {
		t.Fatalf("the chooser rendered no recovery form: %s", page.Body.String())
	}

	state := html.UnescapeString(match[1])

	var login *http.Cookie

	for _, c := range page.Result().Cookies() {
		if c.Name == access.RecoveryCookieName {
			login = c
		}
	}

	if login == nil || login.Value != state {
		t.Fatalf("the chooser's login cookie = %v, want the recovery form's state", login)
	}

	if !login.HttpOnly || login.SameSite != http.SameSiteLaxMode {
		t.Errorf("the login cookie is not HttpOnly and SameSite=Lax: %v", login)
	}

	response := postRecovery(handler, state, &http.Cookie{Name: login.Name, Value: login.Value})
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/done" {
		t.Fatalf("recovery = %d %q, want a redirect to /done", response.Code, response.Body.String())
	}

	if n := storage.count(); n != 1 {
		t.Errorf("completed %d requests, want 1", n)
	}

	spent := false

	for _, c := range response.Result().Cookies() {
		if c.Name == access.RecoveryCookieName && c.MaxAge < 0 {
			spent = true
		}
	}

	if !spent {
		t.Error("the login cookie was not spent by the recovery it pinned")
	}
}

// refusingProvider is a directory that counts the callbacks that reached
// it and never says who anybody is.
type refusingProvider struct {
	mu         sync.Mutex
	identified int
}

func (*refusingProvider) Kind() string { return "google" }

func (*refusingProvider) URL(state string) (string, error) {
	return "https://idp.example/authorize?state=" + url.QueryEscape(state), nil
}

func (p *refusingProvider) Identify(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.identified++

	return "", errors.New("the stand-in identifies nobody")
}

func (p *refusingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.identified
}

// recoveryAndProviderDoor is [recoveryDoor] with a provider beside it.
func recoveryAndProviderDoor(t *testing.T) (http.Handler, *access.StateCodec, *countingCompleter, *refusingProvider) {
	t.Helper()

	codec := access.NewStateCodec(make([]byte, 32), time.Minute)
	storage := &countingCompleter{}
	provider := &refusingProvider{}
	mux := http.NewServeMux()
	issuer.SignInRoutes(mux, issuer.SignInDeps{
		Recovery: acceptingRecovery{}, Storage: storage, State: codec,
		Providers: []issuer.SignIn{provider},
		SSO:       issuer.NewSSO(issuer.NewMemoryState(), time.Hour),
		Return:    func(context.Context, string) string { return "/done" },
		Log:       slog.New(slog.DiscardHandler),
	})

	return mux, codec, storage, provider
}

// A provider round trip's state is refused by the recovery door, and a
// recovery form's by the provider callback, even from the browser that
// holds it in every cookie: the two carry the same request, and each door
// takes only its own.
func TestEachSignInDoorRefusesTheOtherDoorsState(t *testing.T) {
	t.Parallel()

	t.Run("a provider state at the recovery door", func(t *testing.T) {
		t.Parallel()

		handler, codec, storage, _ := recoveryAndProviderDoor(t)

		provider, err := codec.Issue("req-1")
		if err != nil {
			t.Fatal(err)
		}

		form := url.Values{"state": {provider}, "proof": {"a-good-token"}}
		request := httptest.NewRequest(http.MethodPost, "/login/recovery", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(&http.Cookie{Name: access.LoginCookieName, Value: provider})
		request.AddCookie(&http.Cookie{Name: access.RecoveryCookieName, Value: provider})

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest {
			t.Errorf("recovery with a provider state = %d %q, want 400", response.Code, response.Body.String())
		}

		if n := storage.count(); n != 0 {
			t.Errorf("%d requests completed with a provider state", n)
		}
	})

	t.Run("a recovery state at the provider callback", func(t *testing.T) {
		t.Parallel()

		handler, codec, _, provider := recoveryAndProviderDoor(t)

		recovery, err := codec.IssueAs(access.Binding{Bind: "req-1", Owner: access.RecoveryPurpose})
		if err != nil {
			t.Fatal(err)
		}

		request := httptest.NewRequest(http.MethodGet,
			"/login/google/callback?code=x&state="+url.QueryEscape(recovery), nil)
		request.AddCookie(&http.Cookie{Name: access.LoginCookieName, Value: recovery})
		request.AddCookie(&http.Cookie{Name: access.RecoveryCookieName, Value: recovery})

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest {
			t.Errorf("callback with a recovery state = %d %q, want 400", response.Code, response.Body.String())
		}

		if n := provider.count(); n != 0 {
			t.Errorf("a recovery state reached the provider %d times", n)
		}
	})
}

// Opening another sign-in -- a provider button on the same page, or any
// sign-in page on this host -- leaves a recovery form already open working:
// the two keep their states in cookies of their own.
func TestAProviderSignInDoesNotBreakAPendingRecoveryForm(t *testing.T) {
	t.Parallel()

	handler, _, storage, _ := recoveryAndProviderDoor(t)
	jar := map[string]string{}

	keep := func(response *httptest.ResponseRecorder) {
		for _, c := range response.Result().Cookies() {
			jar[c.Name] = c.Value
		}
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/login?auth=req-1", nil))
	keep(page)

	match := recoveryStateField.FindStringSubmatch(page.Body.String())
	if match == nil {
		t.Fatalf("the chooser rendered no recovery form: %s", page.Body.String())
	}

	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/login/google/start?auth=req-1", nil))
	keep(start)

	if start.Code != http.StatusFound || jar[access.LoginCookieName] == "" {
		t.Fatalf("provider start = %d, cookies %v", start.Code, jar)
	}

	form := url.Values{"state": {html.UnescapeString(match[1])}, "proof": {"a-good-token"}}
	request := httptest.NewRequest(http.MethodPost, "/login/recovery", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	for name, value := range jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusFound || storage.count() != 1 {
		t.Errorf("recovery after a provider start = %d %q, completed %d; want it to complete",
			response.Code, response.Body.String(), storage.count())
	}
}

// A recovery post is bounded before it is read: a proof is a few KiB, and a
// body of megabytes is refused unread rather than parsed.
func TestAnOversizedRecoveryPostIsRefused(t *testing.T) {
	t.Parallel()

	handler, codec, storage, _ := recoveryDoor(t)

	state, err := codec.IssueAs(access.Binding{Bind: "req-1", Owner: access.RecoveryPurpose})
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"state": {state}, "proof": {strings.Repeat("x", 1<<20)}}
	request := httptest.NewRequest(http.MethodPost, "/login/recovery", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: access.RecoveryCookieName, Value: state})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a 1 MiB recovery post = %d, want 413", response.Code)
	}

	if n := storage.count(); n != 0 {
		t.Errorf("%d requests completed by an oversized post", n)
	}
}
