// Package lifetime holds the rule for how long a token lives, written once for the two actors that issue tokens. session
// issues the ID and access tokens of a sign-in and a refresh, and exchange issues the access token of an exchange; both
// give a token the same exp from the same limits (ADR 0020, "Lifetimes"). kits/conformance/token/lifetime-vectors.json
// fixes the rule, and signer's cap on exp - iat is held to the same file.
//
// A token lives for the shortest of the built-in maximum, the issuer's lifetime for its type and every cap the audience
// sets (the client's `ttl_cap`, and the resource's `ttl_cap` when a request names a resource, v1's "the shorter of the
// two wins"), and never past the end of what it came from: the session chain that backs it or the proof that was
// exchanged. Each limit only shortens; a longer token is a new record.
package lifetime

import "time"

// Maximum is the built-in maximum of the lifetime of any token (ADR 0020, "Lifetimes": one hour).
const Maximum = time.Hour

// Limits is what bounds one token. A limit that is zero (or, for a duration, negative) is not set.
type Limits struct {
	// Issuer is the lifetime the issuer gives this type of token (TokenLifetime.id_token or .access_token).
	Issuer time.Duration
	// Caps are the audience's caps, such as the client's `ttl_cap`; the shortest wins.
	Caps []time.Duration
	// Ends are the instants the token must not outlive: the end of the session chain, the end of the exchanged token.
	Ends []time.Time
}

// Expiry returns the `iat` and the `exp` of a token issued at now, in whole seconds as a token writes them. ok is false
// when the token would not live a second: the chain or the proof has less than a second left, and no token is issued.
func Expiry(now time.Time, l Limits) (iat, exp int64, ok bool) {
	d := Maximum
	if l.Issuer > 0 {
		d = min(d, l.Issuer)
	}
	for _, c := range l.Caps {
		if c > 0 {
			d = min(d, c)
		}
	}
	iat = now.Unix()
	exp = iat + int64(d/time.Second)
	for _, e := range l.Ends {
		if !e.IsZero() {
			exp = min(exp, e.Unix())
		}
	}
	return iat, exp, exp > iat
}

// Cap is the longest `exp - iat` a token may have when it carries these limits and no end: what a signer holds a
// request to.
func Cap(l Limits) time.Duration {
	iat, exp, _ := Expiry(time.Unix(0, 0), Limits{Issuer: l.Issuer, Caps: l.Caps})
	return time.Duration(exp-iat) * time.Second
}
