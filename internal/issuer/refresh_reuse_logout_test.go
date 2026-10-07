package issuer_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/truvity/sluis/internal/issuer"
)

// refreshAt posts a refresh grant as local-dev and returns the status, the
// error name and the new refresh token.
func refreshAt(t *testing.T, base, token string) (status int, errName, next string) {
	t.Helper()
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {"local-dev"}}
	response, err := http.Post(base+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var body struct {
		Error   string `json:"error"`
		Refresh string `json:"refresh_token"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)
	return response.StatusCode, body.Error, body.Refresh
}

// A reuse ends the session of the client that holds the token, tells that
// client over its back-channel, and leaves the browser sign-in alone: the
// person is still signed in everywhere else.
func TestAReuseSendsTheBackChannelLogoutAndLeavesTheBrowserSignInAlive(t *testing.T) {
	told := make(chan string, 4)
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		told <- r.Form.Get("logout_token")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(listener.Close)

	server, iss := signInServerWith(t, "ada@north.example", backChannelPolicy(listener.URL))
	skew := make(chan time.Duration, 1)
	skew <- 0
	iss.Sessions().SetClock(func() time.Time {
		d := <-skew
		skew <- d
		return time.Now().Add(d)
	})
	b := newBrowser(t, server)
	b.signIn()

	tokens := redeem(t, b, b.authorizeWith(map[string]string{"scope": "openid offline_access"}, ""))
	first, _ := tokens["refresh_token"].(string)
	if first == "" {
		t.Fatal("no refresh token was issued")
	}
	status, _, second := refreshAt(t, server.URL, first)
	if status != http.StatusOK || second == "" {
		t.Fatalf("refresh: %d", status)
	}
	if got := len(told); got != 0 {
		t.Fatalf("a refresh sent %d logout tokens", got)
	}

	<-skew
	skew <- 33 * time.Second

	status, errName, _ := refreshAt(t, server.URL, first)
	if status != http.StatusBadRequest || errName != "invalid_grant" {
		t.Fatalf("reuse: %d %q, want 400 invalid_grant", status, errName)
	}
	raw := awaitLogoutToken(t, told)
	claims := jwtPart(t, raw, 1)
	if claims["aud"] != "local-dev" || claims["sub"] != "ada@north.example" {
		t.Errorf("logout token aud=%v sub=%v, want the client and the person", claims["aud"], claims["sub"])
	}
	if events, _ := claims["events"].(map[string]any); events["http://schemas.openid.net/event/backchannel-logout"] == nil {
		t.Errorf("events = %v, want the back-channel logout event", claims["events"])
	}
	if len(told) != 0 {
		t.Error("one reuse sent more than one logout token")
	}
	if status, _, _ = refreshAt(t, server.URL, second); status == http.StatusOK {
		t.Error("the successor refreshes after the reuse")
	}

	// The browser sign-in survives: no new login is asked for.
	if b.cookies[issuer.SSOCookieName] == "" {
		t.Fatal("the reuse took the browser's sign-in cookie away")
	}
	if where := b.authorize(""); strings.Contains(where, "/login") {
		t.Errorf("the browser was sent to %q: its sign-in did not survive the reuse", where)
	}
	// Repeating the reuse sends nothing more.
	_, _, _ = refreshAt(t, server.URL, first)
	select {
	case <-told:
		t.Error("a repeated reuse of an ended session sent another logout token")
	case <-time.After(300 * time.Millisecond):
	}
}

// A mark dated ahead of this replica's clock (another replica's clock runs
// fast) keeps the grace window -- the replay is answered, the session lives
// -- and is counted, since skew moves the window.
func TestAMarkDatedAheadIsCountedAndKeepsItsGrace(t *testing.T) {
	metricsReader()
	server, iss := signInServerWith(t, "ada@north.example", backChannelPolicy("http://127.0.0.1:1"))
	back := make(chan time.Duration, 1)
	back <- 0
	iss.Sessions().SetClock(func() time.Time {
		d := <-back
		back <- d
		return time.Now().Add(d)
	})
	b := newBrowser(t, server)
	b.signIn()
	tokens := redeem(t, b, b.authorizeWith(map[string]string{"scope": "openid offline_access"}, ""))
	first, _ := tokens["refresh_token"].(string)
	if first == "" {
		t.Fatal("no refresh token was issued")
	}
	status, _, second := refreshAt(t, server.URL, first)
	if status != http.StatusOK {
		t.Fatalf("refresh: %d", status)
	}

	ahead := counted(t, "access_issuer.spent_mark_ahead", []attribute.KeyValue{}...)
	<-back
	back <- -10 * time.Second // this replica now reads 10 s behind the one that wrote the mark

	status, _, replayed := refreshAt(t, server.URL, first)
	if status != http.StatusOK || replayed != second {
		t.Errorf("replay of a future-dated mark: %d %q, want 200 and %q", status, replayed, second)
	}
	if got := counted(t, "access_issuer.spent_mark_ahead"); got < ahead+1 {
		t.Errorf("spent_mark_ahead = %d, want at least %d", got, ahead+1)
	}
	// Within the 2 s tolerance nothing is counted.
	<-back
	back <- -time.Second
	quiet := counted(t, "access_issuer.spent_mark_ahead")
	if status, _, _ = refreshAt(t, server.URL, first); status != http.StatusOK {
		t.Errorf("replay 1 s ahead: %d", status)
	}
	if got := counted(t, "access_issuer.spent_mark_ahead"); got != quiet {
		t.Errorf("a skew of 1 s was counted (%d -> %d)", quiet, got)
	}
}

// Concurrent reuses of one spent token tell the client once.
func TestConcurrentReusesSendOneBackChannelLogout(t *testing.T) {
	told := make(chan string, 16)
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		told <- r.Form.Get("logout_token")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(listener.Close)

	server, iss := signInServerWith(t, "ada@north.example", backChannelPolicy(listener.URL))
	skew := make(chan time.Duration, 1)
	skew <- 0
	iss.Sessions().SetClock(func() time.Time {
		d := <-skew
		skew <- d
		return time.Now().Add(d)
	})
	b := newBrowser(t, server)
	b.signIn()
	tokens := redeem(t, b, b.authorizeWith(map[string]string{"scope": "openid offline_access"}, ""))
	first, _ := tokens["refresh_token"].(string)
	if status, _, _ := refreshAt(t, server.URL, first); status != http.StatusOK {
		t.Fatalf("refresh: %d", status)
	}
	<-skew
	skew <- 33 * time.Second

	const racers = 8
	statuses := make([]int, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			statuses[i], _, _ = refreshAt(t, server.URL, first)
		})
	}
	close(start)
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusBadRequest {
			t.Errorf("racer %d: %d, want 400", i, status)
		}
	}
	awaitLogoutToken(t, told)
	select {
	case <-told:
		t.Error("concurrent reuses sent more than one logout token")
	case <-time.After(500 * time.Millisecond):
	}
}
