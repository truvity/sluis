package issuer

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/internal/access"
)

// consentTestKey signs the acceptances [Storage.CompleteAcceptedForTest]
// makes, when a test gave the storage no sign-in state of its own.
const consentTestKey = "a-test-key-for-signing-state"

// CompleteAcceptedForTest completes a request as the consent page's accept
// does: with an acceptance minted for this request and this person, from
// the browser holding its cookie. For a test whose subject is what the
// completion opens rather than how it was accepted.
func (s *Storage) CompleteAcceptedForTest(ctx context.Context, id string, who Authenticated) error {
	if s.signInState == nil {
		s.signInState = access.NewStateCodec([]byte(consentTestKey), 0)
	}

	who.Consent = acceptedForTest(s.signInState, id, who)

	return s.Complete(ctx, id, who)
}

// acceptedForTest is an acceptance for one request and one person, as the
// browser the page was shown in presents it.
func acceptedForTest(codec *access.StateCodec, id string, who Authenticated) access.AgentConsent {
	token, err := codec.IssueAgentConsent(id, access.AgentConsentActor(who.Subject, who.SSO))
	if err != nil {
		panic(err)
	}

	r := httptest.NewRequest(http.MethodPost, "/login/agent-consent", nil)
	r.AddCookie(access.AgentConsentCookie(token, false, time.Minute))

	return access.AgentConsentPresented(r, token, false)
}

// NewSessionsServiceForTest builds the contract over a stubbed verifier,
// so the authorization rules can be tested without minting real tokens.
func NewSessionsServiceForTest(
	sessions *Sessions,
	verify func(ctx context.Context, bearer string) (string, []string, error),
) *SessionsService {
	return &SessionsService{sessions: sessions, verify: verify}
}

// NewSessionsServiceWithSSOForTest is the same with the sign-in store
// attached, which is what "sign this browser out" acts on.
func NewSessionsServiceWithSSOForTest(
	sessions *Sessions,
	sso *SSO,
	verify func(ctx context.Context, bearer string) (string, []string, error),
) *SessionsService {
	return &SessionsService{sessions: sessions, sso: sso, verify: verify}
}

// NewSessionsServiceForCookieTest is the service over a real issuer, as
// production builds it, with only the bearer's verifier stubbed: a
// browser's cookie is then judged by the issuer's own decision.
func NewSessionsServiceForCookieTest(
	iss *Issuer,
	verify func(ctx context.Context, bearer string) (string, []string, error),
) *SessionsService {
	s := NewSessionsService(iss, nil, false)
	s.verify = verify
	return s
}

// ErrUnverifiedForTest is what a stub verifier refuses with.
var ErrUnverifiedForTest = errors.New("unverified")

// CutForTest and SplitForTest keep the test file free of imports it only
// needs for string handling.
func CutForTest(s, sep string) (string, string, bool) { return strings.Cut(s, sep) }
func SplitForTest(s, sep string) []string             { return strings.Split(s, sep) }

// CreateAuthRequestForTest makes a completed authorization request, which
// is otherwise built by the library from a browser redirect. It returns
// the request's id.
func (s *Storage) CreateAuthRequestForTest(ctx context.Context, subject, clientID string) (string, error) {
	request := &authRequest{
		ID:  "req-" + subject + "-" + clientID,
		Req: &oidc.AuthRequest{ClientID: clientID, RedirectURI: "https://rp.example/cb"},
	}
	if err := setJSON(ctx, s.state, requestKey(request.ID), request, authRequestTTL); err != nil {
		return "", err
	}
	if err := s.Complete(ctx, request.ID, Authenticated{Subject: subject}); err != nil {
		return "", err
	}
	return request.ID, nil
}

// CreateAuthRequestForResourceTest is [CreateAuthRequestForTest] naming an
// RFC 8707 resource and asking for `offline_access`, so a test can drive
// a REAL code exchange through the actual library path — [op.CreateTokenResponse]
// and friends — and then a refresh from the token it mints, rather than
// only exercising this storage's own methods directly. See
// signing_audience_test.go.
func (s *Storage) CreateAuthRequestForResourceTest(ctx context.Context, subject, clientID, resource string) (string, error) {
	request := &authRequest{
		ID: "req-" + subject + "-" + clientID + "-" + resource,
		Req: &oidc.AuthRequest{
			ClientID: clientID, RedirectURI: "https://rp.example/cb",
			ResponseType: oidc.ResponseTypeCode,
			Scopes:       []string{oidc.ScopeOpenID, oidc.ScopeOfflineAccess},
		},
		Resource: resource,
	}
	if err := setJSON(ctx, s.state, requestKey(request.ID), request, authRequestTTL); err != nil {
		return "", err
	}
	if err := s.Complete(ctx, request.ID, Authenticated{Subject: subject}); err != nil {
		return "", err
	}
	return request.ID, nil
}

// CreatePendingAuthRequestForTest makes an authorization request nobody has
// completed yet, for a test to complete. It returns the request's id.
func (s *Storage) CreatePendingAuthRequestForTest(ctx context.Context, id, clientID string) (string, error) {
	request := &authRequest{
		ID:  id,
		Req: &oidc.AuthRequest{ClientID: clientID, RedirectURI: "https://rp.example/cb"},
	}
	if err := setJSON(ctx, s.state, requestKey(request.ID), request, authRequestTTL); err != nil {
		return "", err
	}
	return request.ID, nil
}

// NewForSessionsTest is an Issuer that holds nothing but a session store,
// which is all the sign-out route reaches for.
func NewForSessionsTest(sessions *Sessions) *Issuer {
	return &Issuer{sessions: sessions}
}

// WithAnnounceForTest sets the announcer sign-out everywhere tells clients
// through.
func (s *SessionsService) WithAnnounceForTest(f func(context.Context, []Session)) *SessionsService {
	s.announce = f

	return s
}

// AnnounceLogoutForTest delivers logout tokens as sign-out does.
func (s *Storage) AnnounceLogoutForTest(ctx context.Context, ended []Session) {
	s.announceLogout(ctx, slog.New(slog.DiscardHandler), ended)
}

// EndPreviousForTest runs what an interactive sign-in does to the sign-in
// the browser held before, for a test that controls the request's context.
func EndPreviousForTest(deps SignInDeps, r *http.Request, who Authenticated) {
	(&signIn{deps: deps}).endPrevious(r, who)
}

// CreateSignedInAuthRequestForTest is [CreateAuthRequestForTest] completed
// under a browser sign-in, with no scope at all: what a request whose every
// scope the client was not allowed comes to.
func (s *Storage) CreateSignedInAuthRequestForTest(ctx context.Context, subject, clientID, sso string) (string, error) {
	request := &authRequest{
		ID:  "req-" + subject + "-" + clientID + "-" + sso,
		Req: &oidc.AuthRequest{ClientID: clientID, RedirectURI: "https://rp.example/cb", ResponseType: oidc.ResponseTypeCode},
	}
	if err := setJSON(ctx, s.state, requestKey(request.ID), request, authRequestTTL); err != nil {
		return "", err
	}
	if err := s.Complete(ctx, request.ID, Authenticated{Subject: subject, SSO: sso}); err != nil {
		return "", err
	}
	return request.ID, nil
}
