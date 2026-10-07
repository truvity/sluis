package access_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
)

const consentKey = "a-test-key-for-signing-state"

// presented is an acceptance post: the form's token and, when cookie is
// not empty, the acceptance cookie beside it, set under the name a flow
// with secure cookies sets it under, and read as that flow reads it.
func presented(state, cookie string, secure bool) access.AgentConsent {
	r := httptest.NewRequest(http.MethodPost, "/login/agent-consent", nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: access.CookieNameFor(access.AgentConsentCookieName, secure), Value: cookie})
	}

	return access.AgentConsentPresented(r, state, true)
}

// legacyState is a four-part state as issued before owners were carried,
// which verifies with an empty owner.
func legacyState(bind, actor string) string {
	body := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte("nonce-0123456789")),
		base64.RawURLEncoding.EncodeToString([]byte(bind)),
		base64.RawURLEncoding.EncodeToString([]byte(actor)),
		strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10),
	}, ":")
	mac := hmac.New(sha256.New, []byte(consentKey))
	mac.Write([]byte(body))

	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// An acceptance verifies for its own request and actor, from the browser
// holding its cookie, and for nothing else: not without the cookie, not with
// another token's cookie, not for another request or person, not once
// expired, and never as the login state, a recovery form's state or a
// legacy four-part state.
func TestAnAgentConsentVerifiesOnlyForItsRequestActorAndBrowser(t *testing.T) {
	t.Parallel()

	codec := access.NewStateCodec([]byte(consentKey), time.Minute)
	actor := access.AgentConsentActor("Ada@North.example", "sign-in-1")

	token, err := codec.IssueAgentConsent("req-1", actor)
	if err != nil {
		t.Fatal(err)
	}
	other, err := codec.IssueAgentConsent("req-1", actor)
	if err != nil {
		t.Fatal(err)
	}
	login, _ := codec.Issue("req-1")
	recovery, _ := codec.IssueAs(access.Binding{Bind: "req-1", Owner: access.RecoveryPurpose})
	legacy := legacyState("req-1", actor)

	if err = codec.VerifyAgentConsent(presented(token, token, true), "req-1", actor); err != nil {
		t.Fatalf("the acceptance from its own browser: %v", err)
	}
	if got := codec.Request(presented(token, "", true)); got != "req-1" {
		t.Errorf("Request = %q, want req-1", got)
	}
	if got := codec.Request(presented(login, "", true)); got != "" {
		t.Errorf("Request of the login state = %q, want none", got)
	}

	for _, tc := range []struct {
		name    string
		consent access.AgentConsent
		request string
		actor   string
	}{
		{"no cookie", presented(token, "", true), "req-1", actor},
		{"the cookie of another acceptance", presented(token, other, true), "req-1", actor},
		{"a cookie under the plain-HTTP name", presented(token, token, false), "req-1", actor},
		{"no token", presented("", token, true), "req-1", actor},
		{"another request", presented(token, token, true), "req-2", actor},
		{"another person", presented(token, token, true), "req-1", access.AgentConsentActor("eve@north.example", "sign-in-1")},
		{"another sign-in of the same person", presented(token, token, true), "req-1", access.AgentConsentActor("ada@north.example", "sign-in-2")},
		{"the login state start sets", presented(login, login, true), "req-1", actor},
		{"a recovery form's state", presented(recovery, recovery, true), "req-1", actor},
		{"a legacy four-part state", presented(legacy, legacy, true), "req-1", actor},
		{"the zero value", access.AgentConsent{}, "req-1", actor},
	} {
		if err := codec.VerifyAgentConsent(tc.consent, tc.request, tc.actor); !errors.Is(err, access.ErrNoAgentConsent) {
			t.Errorf("%s: %v, want it refused", tc.name, err)
		}
	}

	// Stale: past the flow's lifetime.
	stale := access.NewStateCodec([]byte(consentKey), time.Minute)
	stale.SetClock(func() time.Time { return time.Now().Add(-2 * time.Minute) })
	old, _ := stale.IssueAgentConsent("req-1", actor)
	if err = codec.VerifyAgentConsent(presented(old, old, true), "req-1", actor); !errors.Is(err, access.ErrNoAgentConsent) {
		t.Errorf("a stale acceptance: %v, want it refused", err)
	}
}

// The cookie is a flow cookie: host-locked when secure, HttpOnly,
// SameSite=Lax, Path=/, and cleared with an empty value.
func TestTheAgentConsentCookieIsAHostLockedFlowCookie(t *testing.T) {
	t.Parallel()

	cookie := access.AgentConsentCookie("v", true, time.Minute)
	if cookie.Name != "__Host-"+access.AgentConsentCookieName || !cookie.Secure || !cookie.HttpOnly ||
		cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge != 60 {
		t.Errorf("cookie = %+v", cookie)
	}
	if cleared := access.AgentConsentCookie("", true, time.Minute); cleared.MaxAge >= 0 {
		t.Errorf("clearing cookie MaxAge = %d", cleared.MaxAge)
	}
}
