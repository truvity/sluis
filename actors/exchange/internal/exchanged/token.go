package exchanged

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"time"

	configv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/config/v2alpha1"
	"github.com/truvity/sluis/v2/kits/go/groupsclaim"
	lifetimekit "github.com/truvity/sluis/v2/kits/go/lifetime"
)

// groupsOf is the groups claim of the token for the audience client (ADR 0020, "The claims of a token"): the held grant
// names whose `<scope>:<thing>` pair appears among the pairs of the client's `requires`, in any role, and those of the
// things its `groups` override names, written with its `groups_delimiter`. Admission was decided on the full set;
// narrowing only keeps a relying party from reading a name meant for another system. The rule is the one session applies
// (kits/go/groupsclaim). The result is sorted, has no repeats and is never nil.
func groupsOf(held []string, c *configv2alpha1.InstallationClient) []string {
	return groupsclaim.For(held, groupsclaim.Audience{Requires: c.GetRequires(), Things: c.GetGroups(), Delimiter: c.GetGroupsDelimiter()})
}

// lifetime is the lifetime of the token: the earliest of the issuer's, the audience client's `ttl_cap`, the built-in
// maximum and the end of the subject token (ADR 0020, "Lifetimes"; REQ-EXCH-004: an exchange never yields a token
// that outlives the token it was made from). It returns the `iat` and `exp` in whole seconds, and false when the
// subject token has less than a second left. The rule is the one session applies (kits/go/lifetime).
func lifetime(now time.Time, issuer, clientCap time.Duration, subjectExp time.Time) (iat, exp int64, ok bool) {
	return lifetimekit.Expiry(now, lifetimekit.Limits{Issuer: issuer, Caps: []time.Duration{clientCap}, Ends: []time.Time{subjectExp}})
}

// accessClaims are the claims of the access token that exchange has signed (ADR 0020, "The claims of a token"). A
// claim that is not here is not issued; signer refuses one that is outside the set.
type accessClaims struct {
	Iss      string    `json:"iss"`
	Sub      string    `json:"sub"`
	Aud      string    `json:"aud"`
	Exp      int64     `json:"exp"`
	Iat      int64     `json:"iat"`
	JTI      string    `json:"jti"`
	ClientID string    `json:"client_id"`
	AuthTime *int64    `json:"auth_time,omitempty"`
	ACR      *string   `json:"acr,omitempty"`
	AMR      *[]string `json:"amr,omitempty"`
	SID      string    `json:"sid,omitempty"`
	Groups   []string  `json:"groups"`
}

// claimsOf builds the claims of the new token. `client_id` is the client the token is issued to (RFC 9068 section 2.2):
// the client that asked, authenticated or the public client's own. It is not the client of the subject token when a
// confidential client exchanges a public client's token; exchange adds no `act` claim, since a standard's core comes
// before its extensions (principle 5) and the audit record names both clients. `acr`, `amr`, `auth_time` and `sid` pass from the subject token as
// they are, and an absent one stays absent (ADR 0020): exchange fills in nothing the upstream did not send.
func claimsOf(issuer, sub, audience, clientID string, iat, exp int64, jti string, s *Subject, groups []string) ([]byte, error) {
	c := accessClaims{
		Iss: issuer, Sub: sub, Aud: audience, Exp: exp, Iat: iat, JTI: jti, ClientID: clientID,
		AuthTime: s.AuthTime, ACR: s.ACR, SID: s.SID, Groups: groups,
	}
	if s.AMRPresent {
		amr := append([]string{}, s.AMR...)
		c.AMR = &amr
	}
	return json.Marshal(c)
}

// newJTI is a random id for the token: 128 bits from the operating system's generator.
func newJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// authAge is the seconds since the person last authenticated, counted from the subject token's `auth_time`. It is
// absent when the token has none: policy has no clock, and an absent name is unknown (policy.md, "Context").
func authAge(s *Subject, now time.Time) *int64 {
	if s.AuthTime == nil {
		return nil
	}
	age := max(now.Unix()-*s.AuthTime, 0)
	return &age
}
