package issuer

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

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/internal/access"
)

// Storage.Complete is the one place every sign-in converges, and it
// verifies an agent-class request's acceptance ITSELF
// (docs/decisions/0040-agent-class-sessions.md, decision 6): whatever a
// door passes it, nothing but an acceptance minted for this request, this
// person and this sign-in, beside a cookie that carries it, completes one.
func TestCompleteVerifiesAnAgentAcceptanceItself(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	key := []byte(consentTestKey)
	codec := access.NewStateCodec(key, time.Minute)
	who := Authenticated{Subject: "ada@north.example", AuthTime: time.Now(), SSO: "sign-in-1", How: "google"}
	actor := access.AgentConsentActor(who.Subject, who.SSO)

	presented := func(token, cookie string) access.AgentConsent {
		r := httptest.NewRequest(http.MethodPost, "/login/agent-consent", nil)
		if cookie != "" {
			r.AddCookie(access.AgentConsentCookie(cookie, false, time.Minute))
		}

		return access.AgentConsentPresented(r, token, false)
	}
	legacy := func(bind string) string {
		body := strings.Join([]string{
			base64.RawURLEncoding.EncodeToString([]byte("nonce-0123456789")),
			base64.RawURLEncoding.EncodeToString([]byte(bind)),
			base64.RawURLEncoding.EncodeToString([]byte(actor)),
			strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10),
		}, ":")
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(body))

		return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}

	for _, tc := range []struct {
		name    string
		codec   *access.StateCodec // the storage's; nil is none
		consent func(request string) access.AgentConsent
		who     Authenticated
		ok      bool
	}{
		{"no acceptance at all", codec, func(string) access.AgentConsent { return access.AgentConsent{} }, who, false},
		{"the login state start sets", codec, func(request string) access.AgentConsent {
			login, _ := codec.Issue(request)
			return presented(login, login)
		}, who, false},
		{"a recovery form's state", codec, func(request string) access.AgentConsent {
			recovery, _ := codec.IssueAs(access.Binding{Bind: request, Actor: actor, Owner: access.RecoveryPurpose})
			return presented(recovery, recovery)
		}, who, false},
		{"a legacy four-part state", codec, func(request string) access.AgentConsent {
			return presented(legacy(request), legacy(request))
		}, who, false},
		{"an acceptance for another request", codec, func(string) access.AgentConsent {
			token, _ := codec.IssueAgentConsent("another-request", actor)
			return presented(token, token)
		}, who, false},
		{"an acceptance shown to another person", codec, func(request string) access.AgentConsent {
			token, _ := codec.IssueAgentConsent(request, access.AgentConsentActor("eve@north.example", who.SSO))
			return presented(token, token)
		}, who, false},
		{"an acceptance shown under another sign-in", codec, func(request string) access.AgentConsent {
			token, _ := codec.IssueAgentConsent(request, access.AgentConsentActor(who.Subject, "sign-in-2"))
			return presented(token, token)
		}, who, false},
		{"the acceptance without its cookie", codec, func(request string) access.AgentConsent {
			token, _ := codec.IssueAgentConsent(request, actor)
			return presented(token, "")
		}, who, false},
		{"the acceptance beside another acceptance's cookie", codec, func(request string) access.AgentConsent {
			token, _ := codec.IssueAgentConsent(request, actor)
			other, _ := codec.IssueAgentConsent(request, actor)
			return presented(token, other)
		}, who, false},
		{"a storage with no sign-in state to verify against", nil, func(request string) access.AgentConsent {
			token, _ := codec.IssueAgentConsent(request, actor)
			return presented(token, token)
		}, who, false},
		{"the acceptance, from its own browser", codec, func(request string) access.AgentConsent {
			token, _ := codec.IssueAgentConsent(request, actor)
			return presented(token, token)
		}, who, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, storage := agentInternalIssuer(t)
			if tc.codec != nil {
				storage.UseSignInState(tc.codec)
			}

			pending, err := storage.CreateAuthRequest(ctx, &oidc.AuthRequest{
				ClientID: "mcp-host", RedirectURI: "http://127.0.0.1/callback",
				Scopes: oidc.SpaceDelimitedArray{"openid"}, ResponseType: oidc.ResponseTypeCode,
			}, "")
			if err != nil {
				t.Fatal(err)
			}

			completing := tc.who
			completing.Consent = tc.consent(pending.GetID())
			err = storage.Complete(ctx, pending.GetID(), completing)

			request, readErr := storage.request(ctx, pending.GetID())
			if readErr != nil {
				t.Fatal(readErr)
			}

			switch {
			case tc.ok && err != nil:
				t.Fatalf("complete: %v", err)
			case tc.ok && (!request.IsDone || request.Class != ClassAgent):
				t.Errorf("done %v class %q, want done and agent", request.IsDone, request.Class)
			case !tc.ok && !errors.Is(err, ErrAgentConsentRequired):
				t.Errorf("complete: %v, want ErrAgentConsentRequired", err)
			case !tc.ok && request.IsDone:
				t.Error("the request was marked done without an acceptance")
			}
		})
	}
}
