package main

import (
	"encoding/base64"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAnHTTPIssuerIsRefusedExceptOnLoopback(t *testing.T) {
	for _, bad := range []string{
		"http://access.north.example", "http://10.0.0.5", "http://169.254.169.254", "ftp://access.north.example",
		"access.north.example", "https://user:pw@access.north.example", "http://localhost.evil.example",
	} {
		choiceEnv(t)
		if _, err := loadConfig(bad, ""); err == nil {
			t.Errorf("%q was accepted as an issuer", bad)
		} else if strings.Contains(err.Error(), "pw") {
			t.Errorf("%q: the error repeats a credential: %v", bad, err)
		}
	}
	for _, good := range []string{
		"https://access.north.example", "https://access.north.example/", "http://localhost:8080", "http://127.0.0.1:9", "http://[::1]:9",
	} {
		choiceEnv(t)
		if _, err := loadConfig(good, ""); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
}

// The same holds for an issuer that comes from the environment or the config
// file rather than the flag.
func TestAnHTTPIssuerFromTheEnvironmentOrTheFileIsRefused(t *testing.T) {
	choiceEnv(t)
	t.Setenv(envIssuer, "http://access.north.example")
	if _, err := loadConfig("", ""); err == nil {
		t.Error("an http issuer in the environment was accepted")
	}
	choiceEnv(t)
	choiceConfig(t, "issuer: http://access.north.example\n")
	if _, err := loadConfig("", ""); err == nil {
		t.Error("an http issuer in the config file was accepted")
	}
}

func callback(t *testing.T, addr, query string) int {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/callback?" + query) //nolint:noctx // a test against its own listener
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// A request to the loopback listener that does not carry the state is not the
// issuer's answer: an `?error=` or a code from anything else on the machine
// neither aborts the sign-in nor completes it, and the real callback still can.
func TestTheLoginCallbackIgnoresRequestsWithoutTheState(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		code string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		code, err := waitForCode(listener, "the-state")
		done <- outcome{code, err}
	}()
	addr := listener.Addr().String()

	for _, q := range []string{
		"error=access_denied&error_description=gotcha",
		"error=access_denied&state=wrong",
		"code=stolen",
		"code=stolen&state=the-stat",
		"code=stolen&state=the-state-and-more",
		"",
	} {
		if status := callback(t, addr, q); status != http.StatusBadRequest {
			t.Errorf("%q answered %d, want 400", q, status)
		}
	}
	select {
	case got := <-done:
		t.Fatalf("a request without the state ended the sign-in: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}

	if status := callback(t, addr, "code=the-code&state=the-state"); status != http.StatusOK {
		t.Errorf("the real callback answered %d", status)
	}
	got := <-done
	if got.err != nil || got.code != "the-code" {
		t.Fatalf("the real callback: %+v", got)
	}
}

// The issuer's own refusal carries the state, and does end the sign-in.
func TestTheLoginCallbackStopsOnTheIssuersRefusalWithTheState(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := waitForCode(listener, "the-state")
		done <- err
	}()
	addr := listener.Addr().String()
	callback(t, addr, "error=access_denied&error_description=not+in+the+group&state=the-state")
	err = <-done
	if err == nil || !strings.Contains(err.Error(), "not in the group") {
		t.Fatalf("err = %v", err)
	}
}

// A sign-in asks for `email`, which the issuer answers in the ID token only
// when it is granted. A session saved without one, by an older issuer or an
// older sluisctl, still names the person by `sub`, which for a person is the
// same address.
func TestASignInAsksForTheAddressAndASessionWithoutOneUsesTheSubject(t *testing.T) {
	for _, scope := range []string{"openid", "email", "offline_access"} {
		if !slices.Contains(strings.Fields(loginScope), scope) {
			t.Errorf("the sign-in asks for %q, want %s among them", loginScope, scope)
		}
	}

	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"ada@north.example"}`))
	subject, email, ok := readIDToken("e30." + payload + ".sig")
	if !ok || subject != "ada@north.example" || email != "" {
		t.Errorf("an ID token without email read as %q %q %v", subject, email, ok)
	}

	choiceEnv(t)
	t.Setenv(envGitHubTokenURL, "")
	const issuer = "https://access.north.example"
	if err := saveSession(issuer, Session{RefreshToken: "a-refresh", Subject: subject}); err != nil {
		t.Fatal(err)
	}
	if got := baoIdentity(Config{Issuer: issuer}); got != "ada@north.example" {
		t.Errorf("a session without email names %q, want its subject", got)
	}
}
