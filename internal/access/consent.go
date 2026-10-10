package access

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AgentConsentCookieName is the cookie an agent-consent page's acceptance
// token travels in: a fifth flow cookie, for the reason the others are
// separate. The login state a provider round trip sets and a recovery
// form's state carry the same authorization request, and neither may be
// presented where an acceptance is asked for (docs/decisions/0040-agent-class-sessions.md,
// decision 6).
const AgentConsentCookieName = "sluis_agent_consent"

// AgentConsentPurpose is what an acceptance token carries as its
// [Binding.Owner]. A provider round trip's state carries none, a legacy
// four-part state reads back with none, and a recovery form's carries
// [RecoveryPurpose], so none of them is an acceptance.
const AgentConsentPurpose = "agent-consent"

// AgentConsentFormLimit is the most an acceptance post may carry: one
// state, a few hundred bytes.
const AgentConsentFormLimit = 8 << 10

// ErrNoAgentConsent is an acceptance that does not verify: absent, forged,
// stale, for another request or another person, or not from the browser
// it was shown in.
var ErrNoAgentConsent = errors.New("access: the agent connection was not accepted in this browser")

// AgentConsentCookie builds the acceptance cookie, and with an empty value
// the one that clears it: `__Host-` prefixed when cookies are secure,
// HttpOnly, SameSite=Lax, Path=/, for the flow's lifetime.
func AgentConsentCookie(value string, secure bool, ttl time.Duration) *http.Cookie {
	return flowCookie(AgentConsentCookieName, value, secure, ttl)
}

// AgentConsentActor is the actor an acceptance token is bound to: the
// person being completed and the browser sign-in they were shown the page
// under. Both, so that a token minted for one person, or for one sign-in of
// theirs, accepts nothing for another.
func AgentConsentActor(subject, signIn string) string {
	return strings.ToLower(strings.TrimSpace(subject)) + "\x00" + signIn
}

// IssueAgentConsent mints an acceptance token for one authorization
// request and one actor ([AgentConsentActor]). It is what the consent page
// puts in its form and in [AgentConsentCookie], and the only place one is
// made.
func (c *StateCodec) IssueAgentConsent(request, actor string) (string, error) {
	if request == "" || actor == "" {
		return "", fmt.Errorf("%w: an acceptance names a request and a person", ErrBadState)
	}

	return c.IssueAs(Binding{Bind: request, Actor: actor, Owner: AgentConsentPurpose})
}

// AgentConsent is an acceptance as a browser presented it: the token from
// the form and the value of the acceptance cookie. Only this package makes
// one ([AgentConsentPresented]); its zero value is no acceptance at all, so
// there is no boolean a caller could set instead. It is verified by whoever
// completes the request ([StateCodec.VerifyAgentConsent]), not by the page
// that received it.
type AgentConsent struct {
	state  string
	cookie string
}

// AgentConsentPresented reads an acceptance off a request: the token the
// form posted, and the acceptance cookie the browser sent with it.
func AgentConsentPresented(r *http.Request, state string, secure bool) AgentConsent {
	consent := AgentConsent{state: state}
	if cookie, err := ReadCookie(r, AgentConsentCookieName, secure); err == nil {
		consent.cookie = cookie.Value
	}

	return consent
}

// Request is the authorization request the presented token names, read
// without deciding anything: the page that received the post needs it to
// find the request, and [StateCodec.VerifyAgentConsent] is what decides.
// Empty for a token that does not verify or is not an acceptance.
func (c *StateCodec) Request(consent AgentConsent) string {
	bound, err := c.VerifyBinding(consent.state)
	if err != nil || bound.Owner != AgentConsentPurpose {
		return ""
	}

	return bound.Bind
}

// VerifyAgentConsent checks an acceptance for the request being completed
// and the actor completing it ([AgentConsentActor]):
//
//   - the cookie carries the very token the form posted, compared in
//     constant time, so the post comes from the browser the page was shown
//     in (another site can neither read the HttpOnly cookie nor make a
//     cross-site POST carry the SameSite=Lax one);
//   - the token is this codec's, unexpired, and an acceptance
//     ([AgentConsentPurpose]): never the login state a provider round trip
//     sets, a recovery form's, or a legacy four-part state with no owner;
//   - it is bound to this request and to this actor.
func (c *StateCodec) VerifyAgentConsent(consent AgentConsent, request, actor string) error {
	if consent.state == "" || consent.cookie == "" ||
		subtle.ConstantTimeCompare([]byte(consent.cookie), []byte(consent.state)) != 1 {
		return fmt.Errorf("%w: not from the browser the page was shown in", ErrNoAgentConsent)
	}

	bound, err := c.VerifyBinding(consent.state)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoAgentConsent, err)
	}

	switch {
	case bound.Owner != AgentConsentPurpose:
		return fmt.Errorf("%w: not an acceptance", ErrNoAgentConsent)
	case request == "" || bound.Bind != request:
		return fmt.Errorf("%w: for another request", ErrNoAgentConsent)
	case actor == "" || subtle.ConstantTimeCompare([]byte(bound.Actor), []byte(actor)) != 1:
		return fmt.Errorf("%w: for another person or sign-in", ErrNoAgentConsent)
	}

	return nil
}
