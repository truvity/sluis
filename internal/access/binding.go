package access

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// AuthRequestCookieName is the cookie that ties an authorization request to
// the browser that started it.
//
// An authorization request is made by `/authorize` and finished by whoever
// opens `/login?auth=<id>` with a sign-in in hand: the id is a bearer of
// the request, and it travels in a URL. Without something the browser holds
// beside it, an attacker who starts a request of their own can send the
// link to a victim whose live sign-in then completes the attacker's
// request as the victim, and the code goes to the attacker's redirect. The
// cookie is that something: `/authorize` sets it, and every step that would
// advance the request asks for it.
const AuthRequestCookieName = "sluis_authreq"

// authRequestsKept is how many requests one browser may have open at once,
// so that a second tab does not strand the first. The oldest is dropped.
const authRequestsKept = 8

// authRequestTagLength is how much of the MAC is kept: 128 bits.
const authRequestTagLength = 22

// AuthRequestCookie builds the cookie, and with an empty value the one that
// clears it. It is `__Host-` prefixed when cookies are secure, HttpOnly and
// SameSite=Lax like the flow's other cookies: the redirects it rides on are
// top-level navigations.
func AuthRequestCookie(value string, secure bool, ttl time.Duration) *http.Cookie {
	return flowCookie(AuthRequestCookieName, value, secure, ttl)
}

// RequestTag is the proof, for one authorization request, that its browser
// was the one `/authorize` answered: a MAC of the request id under a key
// derived for this purpose alone, so that no state, session or CSRF value
// this codec signs can stand in for it.
func (c *StateCodec) RequestTag(id string) (string, error) {
	tag, err := sign(c.key, "sluis/authorization-request/v1\x00"+id)
	if err != nil {
		return "", err
	}

	return tag[:authRequestTagLength], nil
}

// AddRequest returns the cookie value that adds id to those the browser
// already holds (the value of its current cookie, empty for none), newest
// last, forgetting the oldest beyond the few kept.
func (c *StateCodec) AddRequest(held, id string) (string, error) {
	tag, err := c.RequestTag(id)
	if err != nil {
		return "", err
	}

	var tags []string

	for _, previous := range strings.Split(held, ".") {
		if len(previous) == authRequestTagLength && previous != tag {
			tags = append(tags, previous)
		}
	}

	tags = append(tags, tag)
	if len(tags) > authRequestsKept {
		tags = tags[len(tags)-authRequestsKept:]
	}

	return strings.Join(tags, "."), nil
}

// RequestStartedHere reports whether the request carries the cookie for the
// authorization request id: whether this is the browser `/authorize` was
// answered to, rather than one a link was sent to. Compared in constant
// time, because the cookie is the half of the pair an attacker does not
// have.
func (c *StateCodec) RequestStartedHere(r *http.Request, id string, secure bool) bool {
	if c == nil || id == "" {
		return false
	}

	cookie, err := ReadCookie(r, AuthRequestCookieName, secure)
	if err != nil || cookie.Value == "" {
		return false
	}

	want, err := c.RequestTag(id)
	if err != nil {
		return false
	}

	found := 0

	for _, held := range strings.Split(cookie.Value, ".") {
		// Every tag is compared, so that the time taken does not say which
		// position matched.
		found |= subtle.ConstantTimeCompare([]byte(held), []byte(want))
	}

	return found == 1
}

// CSRFToken is a token for one purpose, bound to a value the browser holds
// (a cookie's), that a form carries back. It is a MAC under a key derived
// for this purpose, so a page an attacker can get hold of for themselves
// carries a token that is useless against anybody else.
func (c *StateCodec) CSRFToken(purpose, bound string) (string, error) {
	return sign(c.key, "sluis/csrf/v1\x00"+purpose+"\x00"+bound)
}

// CSRFValid reports whether token is [StateCodec.CSRFToken] for the same
// purpose and bound value.
func (c *StateCodec) CSRFValid(purpose, bound, token string) bool {
	if c == nil || token == "" || bound == "" {
		return false
	}

	want, err := c.CSRFToken(purpose, bound)

	return err == nil && subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}
