package issuer_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/policy"
)

// Once an operator has turned recovery off from the console, nothing a
// recovery sign-in opened is usable: not the form, not the browser sign-in, not
// a refresh token (docs/decisions/0075).

// flipGate is the switch, thrown by a test.
type flipGate struct {
	off atomic.Bool
	err error
}

func (g *flipGate) Off(context.Context) (bool, error) { return g.off.Load(), g.err }

func newRecoveryIssuer(t *testing.T) (*httptest.Server, *issuer.Issuer, *flipGate) {
	t.Helper()
	server, iss := signInServerWith(t, "ada@north.example", lookAlikePolicy)
	gate := &flipGate{}
	iss.UseRecoveryGate(gate)
	return server, iss, gate
}

func openSession(t *testing.T, iss *issuer.Issuer, identity, token, method string) issuer.Session {
	t.Helper()
	session, err := iss.Sessions().Record(t.Context(), issuer.Opened{
		Identity: identity, ClientID: "local-dev", How: issuer.HowCode, Method: method,
		Token: token, Scopes: []string{"openid"}, AuthTime: time.Now(),
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	return session
}

// A refresh token a recovery sign-in opened stops refreshing the moment the
// switch is thrown, and its session is revoked by the refresh that finds it. A
// person's refresh goes on.
func TestARecoveryRefreshTokenIsRefusedOnceRecoveryIsOff(t *testing.T) {
	t.Parallel()
	server, iss, gate := newRecoveryIssuer(t)
	before := openSession(t, iss, lookAlike, "refresh-before", issuer.RecoveryHow)
	recovered := openSession(t, iss, lookAlike, "refresh-recovery", issuer.RecoveryHow)
	openSession(t, iss, "ada@north.example", "refresh-person", "google")

	if status, body := refreshLocalDev(t, server.URL, "refresh-before"); status != http.StatusOK {
		t.Fatalf("a recovery refresh with recovery on = %d %v", status, body)
	}
	if _, live, _ := iss.Sessions().ByID(t.Context(), before.ID); !live {
		t.Fatal("the control session ended with recovery on")
	}

	gate.off.Store(true)

	status, body := refreshLocalDev(t, server.URL, "refresh-recovery")
	if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("a recovery refresh with recovery off = %d %v, want 400 invalid_grant", status, body)
	}
	if _, live, err := iss.Sessions().ByID(t.Context(), recovered.ID); err != nil || live {
		t.Errorf("the recovery session is live=%v err=%v after the switch, want revoked", live, err)
	}
	if status, body = refreshLocalDev(t, server.URL, "refresh-person"); status != http.StatusOK {
		t.Errorf("a person's refresh with recovery off = %d %v, want it untouched", status, body)
	}
}

// EndRecovery removes exactly what recovery opened and counts it.
func TestEndRecoveryEndsOnlyWhatRecoveryOpened(t *testing.T) {
	t.Parallel()
	_, iss, _ := newRecoveryIssuer(t)
	ctx := t.Context()

	recoverySignIn, _, err := iss.SSO().Begin(ctx, lookAlike, issuer.RecoveryHow)
	if err != nil {
		t.Fatal(err)
	}
	personSignIn, _, err := iss.SSO().Begin(ctx, "ada@north.example", "google")
	if err != nil {
		t.Fatal(err)
	}
	r1 := openSession(t, iss, lookAlike, "t-recovery-1", issuer.RecoveryHow)
	r2 := openSession(t, iss, lookAlike, "t-recovery-2", issuer.RecoveryHow)
	person := openSession(t, iss, "ada@north.example", "t-person", "google")

	ended, err := iss.EndRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ended.SignIns != 1 || ended.Sessions != 2 {
		t.Errorf("ended = %+v, want 1 sign-in and 2 sessions", ended)
	}
	for name, id := range map[string]string{"first": r1.ID, "second": r2.ID} {
		if _, live, _ := iss.Sessions().ByID(ctx, id); live {
			t.Errorf("the %s recovery session is still live", name)
		}
	}
	if _, live, _ := iss.Sessions().ByID(ctx, person.ID); !live {
		t.Error("a person's session was ended with recovery's")
	}
	if _, live, _ := iss.SSO().Get(ctx, recoverySignIn.ID); live {
		t.Error("the recovery sign-in is still live")
	}
	if _, live, _ := iss.SSO().Get(ctx, personSignIn.ID); !live {
		t.Error("a person's sign-in was ended with recovery's")
	}
	// Idempotent: nothing left to end.
	if again, err := iss.EndRecovery(ctx); err != nil || again.SignIns+again.Sessions != 0 {
		t.Errorf("second sweep = %+v, %v", again, err)
	}
}

// The browser's recovery sign-in does not stand once recovery is off: the
// console on the issuer's origin reads it as nobody, and the record is ended.
func TestARecoverySignInDoesNotStandOnceRecoveryIsOff(t *testing.T) {
	t.Parallel()
	_, iss, gate := newRecoveryIssuer(t)
	ctx := t.Context()
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	read := issuer.SignedInReader(iss, storage, issuer.SignInDeps{Log: slog.New(slog.DiscardHandler)})

	recovered, secret, err := iss.SSO().Begin(ctx, lookAlike, issuer.RecoveryHow)
	if err != nil {
		t.Fatal(err)
	}
	person, personSecret, err := iss.SSO().Begin(ctx, "ada@north.example", "google")
	if err != nil {
		t.Fatal(err)
	}
	request := func(secret string) (*http.Request, *httptest.ResponseRecorder) {
		r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		r.AddCookie(iss.SSO().Cookie(secret, false))
		return r, httptest.NewRecorder()
	}

	r, w := request(secret)
	if got, ok := read(w, r); !ok || got.ID != recovered.ID {
		t.Fatalf("a recovery sign-in with recovery on = %v, %v", got, ok)
	}

	gate.off.Store(true)

	r, w = request(secret)
	if got, ok := read(w, r); ok {
		t.Fatalf("a recovery sign-in with recovery off stood: %v", got)
	}
	if _, live, _ := iss.SSO().Get(ctx, recovered.ID); live {
		t.Error("the recovery sign-in was not ended by being found")
	}
	cleared := false
	for _, c := range w.Result().Cookies() {
		cleared = cleared || (c.Name == issuer.SSOCookieName && c.Value == "")
	}
	if !cleared {
		t.Error("the browser's sign-in cookie was not cleared")
	}

	r, w = request(personSecret)
	if got, ok := read(w, r); !ok || got.ID != person.ID {
		t.Errorf("a person's sign-in with recovery off = %v, %v; want it standing", got, ok)
	}
}

// A switch that cannot be read denies recovery and destroys nothing: a fault in
// the State must not end a session an operator never meant to end. Once it can
// be read again the session works, and only a positive record ends it.
func TestAnUnreadableSwitchRefusesRecoveryAndEndsNothing(t *testing.T) {
	t.Parallel()
	server, iss, gate := newRecoveryIssuer(t)
	ctx := t.Context()
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	read := issuer.SignedInReader(iss, storage, issuer.SignInDeps{Log: slog.New(slog.DiscardHandler)})
	recovered := openSession(t, iss, lookAlike, "refresh-unreadable", issuer.RecoveryHow)
	signIn, secret, err := iss.SSO().Begin(ctx, lookAlike, issuer.RecoveryHow)
	if err != nil {
		t.Fatal(err)
	}

	gate.off.Store(true)
	gate.err = errors.New("dynamodb: throttled")

	if status, _ := refreshLocalDev(t, server.URL, "refresh-unreadable"); status == http.StatusOK {
		t.Error("a recovery refresh was served while the switch could not be read")
	} else if status < http.StatusInternalServerError {
		t.Errorf("the refusal is %d: a fault is a server error the client retries, not an invalid grant that ends its chain", status)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	r.AddCookie(iss.SSO().Cookie(secret, false))
	w := httptest.NewRecorder()
	if got, ok := read(w, r); ok {
		t.Errorf("a recovery sign-in stood while the switch could not be read: %v", got)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == issuer.SSOCookieName && c.Value == "" {
			t.Error("the browser's cookie was cleared on a read error")
		}
	}
	if _, live, _ := iss.Sessions().ByID(ctx, recovered.ID); !live {
		t.Error("the recovery session was revoked on a read error")
	}
	if _, live, _ := iss.SSO().Get(ctx, signIn.ID); !live {
		t.Error("the recovery sign-in was ended on a read error")
	}

	// The State is back and says nothing was disabled: it all works again.
	gate.off.Store(false)
	gate.err = nil
	if status, body := refreshLocalDev(t, server.URL, "refresh-unreadable"); status != http.StatusOK {
		t.Errorf("after the read recovered the refresh = %d %v", status, body)
	}
	r = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	r.AddCookie(iss.SSO().Cookie(secret, false))
	if _, ok := read(httptest.NewRecorder(), r); !ok {
		t.Error("the recovery sign-in did not stand once the read recovered")
	}
}

// The form post is refused before it is read, and the chooser offers no form.
func TestTheRecoveryFormIsRefusedAndNotOfferedOnceRecoveryIsOff(t *testing.T) {
	t.Parallel()
	codec := access.NewStateCodec(make([]byte, 32), time.Minute)
	storage := &countingCompleter{}
	sso := issuer.NewSSO(issuer.NewMemoryState(), time.Hour)
	_, iss, gate := newRecoveryIssuer(t)
	mux := http.NewServeMux()
	issuer.SignInRoutes(mux, issuer.SignInDeps{
		Issuer: iss, Recovery: acceptingRecovery{}, Storage: storage, State: codec, SSO: sso,
		Return: func(context.Context, string) string { return "/done" },
		Log:    slog.New(slog.DiscardHandler),
	})
	handler := refusal.Middleware(mux)

	post := func() *httptest.ResponseRecorder {
		state, err := codec.IssueAs(access.Binding{Bind: "req-1", Owner: access.RecoveryPurpose})
		if err != nil {
			t.Fatal(err)
		}
		form := url.Values{"state": {state}, "proof": {"a-good-token"}}
		request := httptest.NewRequest(http.MethodPost, "/login/recovery", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(&http.Cookie{Name: access.RecoveryCookieName, Value: state})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	chooser := func() string {
		request := httptest.NewRequest(http.MethodGet, "/login?auth=req-1", nil)
		request.AddCookie(startedIn(t, codec, "req-1"))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Body.String()
	}
	if !strings.Contains(chooser(), "/login/recovery") {
		t.Fatalf("the sign-in page offers no recovery form with recovery on: %s", chooser())
	}
	if response := post(); response.Code >= http.StatusBadRequest {
		t.Fatalf("recovery with it on = %d %q", response.Code, response.Body.String())
	}
	if storage.count() != 1 {
		t.Fatalf("%d requests completed with recovery on, want 1", storage.count())
	}

	gate.off.Store(true)
	if strings.Contains(chooser(), "/login/recovery") {
		t.Error("the sign-in page still offers the recovery form with recovery off")
	}
	response := post()
	if response.Code != http.StatusForbidden {
		t.Errorf("recovery with it off = %d, want 403", response.Code)
	}
	if storage.count() != 1 {
		t.Errorf("%d requests completed, want the same one", storage.count())
	}
	for _, c := range response.Result().Cookies() {
		if c.Name == issuer.SSOCookieName && c.Value != "" {
			t.Errorf("a refused recovery handed the browser a sign-in: %v", c)
		}
	}
}

// A request recovery completed before the switch is thrown cannot be redeemed
// after it, and a new one cannot be completed: the code is the last thing a
// recovery sign-in holds that is not a session.
func TestACodeRecoveryIssuedIsNotRedeemableOnceRecoveryIsOff(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w := newAgentWorld(t, agentPolicyText(true))
	w.now = time.Now()
	gate := &flipGate{}
	w.iss.UseRecoveryGate(gate)
	storage, err := issuer.NewTestStorage(w.iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	who := issuer.Authenticated{Subject: "k8s:identity-system:authorization-webhook", AuthTime: time.Now(), How: issuer.RecoveryHow}
	start := func() op.AuthRequest {
		pending, err := storage.CreateAuthRequest(ctx, &oidc.AuthRequest{
			ClientID: agentClient, RedirectURI: "http://127.0.0.1/callback",
			Scopes: oidc.SpaceDelimitedArray{"openid"}, ResponseType: oidc.ResponseTypeCode,
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		return pending
	}

	completed := start()
	if err = storage.Complete(ctx, completed.GetID(), who); err != nil {
		t.Fatalf("complete with recovery on: %v", err)
	}
	gate.off.Store(true)

	redeemed, err := storage.AuthRequestByID(ctx, completed.GetID())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = storage.CreateAccessAndRefreshTokens(ctx, redeemed.(op.TokenRequest), ""); err == nil {
		t.Error("a code recovery issued was redeemed after recovery was turned off")
	}
	if sessions, listErr := w.iss.Sessions().List(ctx, issuer.Query{Identity: who.Subject}); listErr != nil || len(sessions) != 0 {
		t.Errorf("sessions after the refused redemption = %v, %v; want none", sessions, listErr)
	}
	if err = storage.Complete(ctx, start().GetID(), who); !errors.Is(err, issuer.ErrRecoveryOff) {
		t.Errorf("Complete with recovery off = %v, want ErrRecoveryOff", err)
	}
}

// Back-channel logout tells the clients of what the sweep ended.
func TestEndRecoveryTellsTheClientsItEnded(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var told atomic.Int32
	var paths sync.Map
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		told.Add(1)
		paths.Store(r.URL.Path, true)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(listener.Close)

	declared, err := policy.Parse([]byte("version: 1\nlifetimes:\n  default: 1h\nclients:\n" +
		"  c0:\n    kind: public\n    redirects: [\"http://localhost:8000/callback\"]\n    requires: [\"all:everyone\"]\n" +
		"    backchannel_logout_uri: " + listener.URL + "/c0\n" +
		"  c1:\n    kind: public\n    redirects: [\"http://localhost:8000/callback\"]\n    requires: [\"all:everyone\"]\n" +
		"    backchannel_logout_uri: " + listener.URL + "/c1\n" +
		"groups:\n  all:everyone:\n    matchers:\n      - email: ada@north.example\n"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, &fakeDirectory{}, issuer.NewMemoryState())
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	signIn, _, err := iss.SSO().Begin(ctx, lookAlike, issuer.RecoveryHow)
	if err != nil {
		t.Fatal(err)
	}
	// c1 only ever got an ID token under the sign-in; c0 holds a refresh token.
	if err = iss.SSO().Involve(ctx, signIn.ID, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err = iss.Sessions().Record(ctx, issuer.Opened{
		Identity: lookAlike, ClientID: "c0", How: issuer.HowCode, Method: issuer.RecoveryHow, SSO: signIn.ID,
		Token: "t-c0", Scopes: []string{"openid"}, AuthTime: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	ended, err := iss.EndRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ended.SignIns != 1 || ended.Sessions != 1 || len(ended.Ended) != 2 {
		t.Fatalf("ended = %+v, want 1 sign-in, 1 session and 2 clients to tell", ended)
	}
	storage.AnnounceEnded(ctx, nil, ended.Ended)
	if got := told.Load(); got != 2 {
		t.Errorf("%d clients were told, want 2", got)
	}
	for _, path := range []string{"/c0", "/c1"} {
		if _, ok := paths.Load(path); !ok {
			t.Errorf("%s was not told", path)
		}
	}
}

// An access token minted under a recovery sign-in carries the claim a verifier
// that sees only the token reads, and one minted under a person's does not.
func TestARecoveryAccessTokenCarriesTheRecoveryClaim(t *testing.T) {
	t.Parallel()
	server, iss, _ := newRecoveryIssuer(t)
	openSession(t, iss, lookAlike, "refresh-claim-recovery", issuer.RecoveryHow)
	openSession(t, iss, "ada@north.example", "refresh-claim-person", "google")

	status, body := refreshLocalDev(t, server.URL, "refresh-claim-recovery")
	if status != http.StatusOK {
		t.Fatalf("recovery refresh = %d %v", status, body)
	}
	if got := payloadOf(t, body["access_token"].(string))[issuer.RecoveryClaim]; got != true {
		t.Errorf("a recovery access token carries %s = %v, want true", issuer.RecoveryClaim, got)
	}
	status, body = refreshLocalDev(t, server.URL, "refresh-claim-person")
	if status != http.StatusOK {
		t.Fatalf("person refresh = %d %v", status, body)
	}
	if _, has := payloadOf(t, body["access_token"].(string))[issuer.RecoveryClaim]; has {
		t.Error("a person's access token carries the recovery claim")
	}
}
