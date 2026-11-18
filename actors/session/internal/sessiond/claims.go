package sessiond

import (
	"encoding/json"
	"time"

	configv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/config/v2alpha1"
	"github.com/truvity/sluis/v2/kits/go/groupsclaim"
	lifetimekit "github.com/truvity/sluis/v2/kits/go/lifetime"
)

// claims is the input of a token's claim set (0020 "The claims of a token"). The set is fixed: signer refuses a claim
// outside it, and the table below is where session says which ones it fills.
type claims struct {
	issuer, audience, subject, sid string
	issuedAt, expires              time.Time
	assure                         assurance
	groups                         []string
	nonce                          string // ID token only
	jti, clientID                  string // access token only
}

// idToken is the claim set of an ID token: iss, sub, aud, exp, iat, the nonce when the client sent one,
// auth_time, acr and amr as the upstream said them (absent stays absent), sid and groups.
func (c claims) idToken() ([]byte, error) {
	m := c.common()
	if c.nonce != "" {
		m["nonce"] = c.nonce
	}
	return json.Marshal(m)
}

// accessToken is the claim set of an access token (RFC 9068 section 2.2): the common ones plus jti and
// client_id.
func (c claims) accessToken() ([]byte, error) {
	m := c.common()
	m["jti"] = c.jti
	m["client_id"] = c.clientID
	return json.Marshal(m)
}

func (c claims) common() map[string]any {
	m := map[string]any{
		"iss": c.issuer, "sub": c.subject, "aud": c.audience,
		"iat": c.issuedAt.Unix(), "exp": c.expires.Unix(),
		"sid": c.sid,
	}
	if c.assure.hasAuthTime {
		m["auth_time"] = c.assure.authTime.Unix()
	}
	if c.assure.acr != nil {
		m["acr"] = *c.assure.acr
	}
	if len(c.assure.amr) > 0 {
		m["amr"] = c.assure.amr
	}
	if len(c.groups) > 0 {
		m["groups"] = c.groups
	}
	return m
}

// groupsOf is the groups claim of a token for the client (0020): the held grant names whose <scope>:<thing> pair appears
// among the pairs of the client's requires, and those of the things its groups override names, written with the client's
// delimiter. Narrowing never changes what requires admits: admission was decided on the full set. The rule is the one
// exchange applies (kits/go/groupsclaim). The result is sorted and has no repeats; it is empty, never nil.
func groupsOf(held []string, c *configv2alpha1.InstallationClient) []string {
	return groupsclaim.For(held, groupsclaim.Audience{Requires: c.GetRequires(), Things: c.GetGroups(), Delimiter: c.GetGroupsDelimiter()})
}

// expiry is the exp of a token: the earliest of the issuer's lifetime for the type, the client's ttl_cap and the end
// of the session chain that backs it (0020 "Lifetimes"). zero ttlCap means the client sets none. ok is false when the
// token would not live a second. The rule is the one exchange applies (kits/go/lifetime).
func expiry(now time.Time, lifetime, ttlCap time.Duration, chainEnd time.Time) (exp time.Time, ok bool) {
	_, e, ok := lifetimekit.Expiry(now, lifetimekit.Limits{Issuer: lifetime, Caps: []time.Duration{ttlCap}, Ends: []time.Time{chainEnd}})
	return time.Unix(e, 0), ok
}
