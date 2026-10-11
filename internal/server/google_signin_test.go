package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	"github.com/truvity/sluis/internal/modcall"
)

// moduleSignIn is the Google connector in the module: it signs somebody in
// with the address the test wants, and counts how often it is asked.
type moduleSignIn struct {
	signInConnector
	identified atomic.Int32
}

func (*moduleSignIn) Kind() string { return "google" }
func (m *moduleSignIn) Identify(ctx context.Context, code string) (string, error) {
	m.identified.Add(1)
	return m.signInConnector.Identify(ctx, code)
}

func signInThrough(t *testing.T, server *ConsoleServer, backend string) *httptest.ResponseRecorder {
	t.Helper()
	start := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/login/"+backend+"/start", nil)
	request.SetPathValue("backend", backend)
	server.signInStart(start, request)
	if start.Code != http.StatusFound {
		t.Fatalf("start = %d (%s), want a redirect", start.Code, start.Body.String())
	}
	cookie := start.Result().Cookies()[0] //nolint:bodyclose // a recorder's result has no body to close
	done := httptest.NewRecorder()
	callback := httptest.NewRequest(http.MethodGet, "/login/"+backend+"/callback?code=x&state="+cookie.Value, nil)
	callback.SetPathValue("backend", backend)
	callback.Header.Set("Accept", "text/html")
	callback.AddCookie(cookie)
	server.signInCallback(done, callback)
	return done
}

func hasSession(r *httptest.ResponseRecorder) bool {
	for _, c := range r.Result().Cookies() { //nolint:bodyclose // a recorder's result has no body to close
		if c.Name == access.CookieName {
			return true
		}
	}
	return false
}

// With `google.remote` set the sign-in is the module's: the address to send the
// browser to and the exchange of the code both go to `google.identify` and its
// sibling, called as the issuer, and the session is for the address it named.
func TestSigningInWithGoogleCallsTheModuleWhenItIsRemote(t *testing.T) {
	t.Parallel()
	module := &moduleSignIn{signInConnector: signInConnector{email: "ada@north.example"}}
	svc := googleconsole.NewService(nil, []googleconsole.Connector{module}, nil, slog.New(slog.DiscardHandler))
	server := signInHarness(t, "unused")
	server.connectors = map[string]Connector{
		"google": googleconsole.NewRemoteConnector(modcall.Local{googleconsole.Module: svc.Server()}.As(googleconsole.CallerIssuer)),
	}

	done := signInThrough(t, server, "google")
	if done.Code != http.StatusFound || !hasSession(done) {
		t.Fatalf("callback = %d (%s), want a session", done.Code, done.Body.String())
	}
	if got := module.identified.Load(); got != 1 {
		t.Errorf("google.identify was called %d times, want once for the sign-in", got)
	}
}

// Without it the sign-in is in process, and no module is called.
func TestSigningInWithGoogleStaysInProcessWhenItIsNot(t *testing.T) {
	t.Parallel()
	module := &moduleSignIn{signInConnector: signInConnector{email: "ada@north.example"}}
	// A module exists in the process, but the issuer's connector is the
	// in-process one and does not reach it.
	server := signInHarness(t, "unused")
	server.connectors = map[string]Connector{"google": module}

	done := signInThrough(t, server, "google")
	if done.Code != http.StatusFound || !hasSession(done) {
		t.Fatalf("callback = %d (%s), want a session", done.Code, done.Body.String())
	}
	if module.identified.Load() != 1 {
		t.Errorf("the in-process connector was asked %d times, want once", module.identified.Load())
	}
}

// down is a module that does not answer.
type down struct{}

func (down) Call(context.Context, string, string, []byte) ([]byte, error) {
	return nil, errors.Join(modcall.ErrTransport, errors.New("invoke sluis-x-google: cold"))
}

// A sign-in the module cannot finish is a clear page and no session. It names
// the module's availability, not the provider's words and not the transport's.
func TestASignInWhenTheModuleIsDownShowsAClearPageAndNoSession(t *testing.T) {
	t.Parallel()
	server := signInHarness(t, "unused")
	remote := googleconsole.NewRemoteConnector(down{})
	server.connectors = map[string]Connector{"google": remote}

	// The start already needs the module for the address.
	start := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/login/google/start", nil)
	request.SetPathValue("backend", "google")
	server.signInStart(start, request)
	if start.Code == http.StatusFound {
		t.Fatal("a sign-in was started with no address to send the browser to")
	}

	// A callback whose code cannot be exchanged.
	state, err := server.state.Issue("")
	if err != nil {
		t.Fatal(err)
	}
	done := httptest.NewRecorder()
	callback := httptest.NewRequest(http.MethodGet, "/login/google/callback?code=x&state="+state, nil)
	callback.SetPathValue("backend", "google")
	callback.Header.Set("Accept", "text/html")
	callback.AddCookie(&http.Cookie{Name: access.LoginCookieName, Value: state})
	server.signInCallback(done, callback)
	if hasSession(done) || done.Code == http.StatusFound {
		t.Fatalf("a session was issued with the module down: %d", done.Code)
	}
	body := done.Body.String()
	if !strings.Contains(strings.ToLower(body), "sign-in service") {
		t.Errorf("the page does not say the sign-in service did not answer:\n%s", body)
	}
	if strings.Contains(body, "invoke sluis-x-google") {
		t.Errorf("the page carries the transport's words:\n%s", body)
	}
}

// The consent's callback forwards the code to the module; a module that cannot
// be reached is a page the operator can read, never a 5xx, and never a retry.
func TestAConsentTheModuleCannotFinishIsAPageAndNotARetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	module := callerFunc(func(ctx context.Context, _, _ string, _ []byte) ([]byte, error) {
		calls.Add(1)
		if !modcall.NoRetry(ctx) {
			t.Error("the forward of a single-use code was not marked no-retry")
		}
		return nil, modcall.ErrTransport
	})
	codec := access.NewStateCodec([]byte("the hub's session key"), 60e9)
	server := &ConsoleServer{
		state:      codec,
		sessions:   &access.Sessions{},
		connectors: map[string]Connector{"google": googleconsole.NewRemoteConnector(nil)},
		console:    &Console{deps: ConsoleDeps{Modules: consolecall.Callers{Google: module}}},
		log:        slog.New(slog.DiscardHandler),
	}
	state, err := codec.IssueAs(access.Binding{Actor: "ada@north.example"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/connect/google/callback?state="+state+"&code=x", nil)
	request.SetPathValue("backend", "google")
	request.AddCookie(&http.Cookie{Name: access.ConnectCookieName, Value: state})
	recorder := httptest.NewRecorder()
	server.connectCallback(recorder, request)

	if recorder.Code >= 500 || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/html") {
		t.Errorf("a consent the module could not finish answered %d %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	if !strings.Contains(recorder.Body.String(), "single-use") || !strings.Contains(recorder.Body.String(), "Back to the console") {
		t.Errorf("the operator is left with no way on:\n%s", recorder.Body.String())
	}
	if calls.Load() != 1 {
		t.Errorf("the code was forwarded %d times, want once", calls.Load())
	}
}

type callerFunc func(ctx context.Context, module, method string, payload []byte) ([]byte, error)

func (f callerFunc) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	return f(ctx, module, method, payload)
}
