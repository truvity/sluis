package policy

import (
	"fmt"
	"slices"
	"strings"
)

// DroppedGroup is a held group a token for an audience does not carry.
type DroppedGroup struct {
	Group  string `json:"group" yaml:"group"`
	Reason string `json:"reason" yaml:"reason"`
}

// Explanation is what a token for one client would carry for one caller: the
// groups kept and dropped by [Policy.ScopeGroups], the claim fragments of
// every group held ([Policy.Evaluate]: scoping narrows only `groups`), how long the token would live and
// which `requires` group of the client admits the caller. It holds no
// secret and no registered claim (`iss`, `sub`, `aud`, `exp`...): only what the
// policy decides.
//
// The offline command and the issuer's live action print this one value, so
// the two cannot disagree about its shape.
type Explanation struct {
	Audience string         `json:"audience" yaml:"audience"`
	Held     []string       `json:"held" yaml:"held"`
	Kept     []string       `json:"groups_kept" yaml:"groups_kept"`
	Dropped  []DroppedGroup `json:"groups_dropped" yaml:"groups_dropped"`
	Claims   map[string]any `json:"claims" yaml:"claims"`
	// Lifetime is how long the token would live: the shortest lifetime across
	// the held groups, capped by the client's `ttl_cap`. Empty when neither
	// declares one.
	Lifetime string `json:"lifetime" yaml:"lifetime"`
	// Admitted says whether the caller holds a group the client requires; a
	// caller who does not would be refused a token.
	Admitted bool `json:"admitted" yaml:"admitted"`
	// AdmittedBy is the first group of the client's `requires`, in the order
	// the policy declares them, that the caller holds. Empty when not admitted.
	AdmittedBy string `json:"admitted_by" yaml:"admitted_by"`
}

// registeredClaims are never part of an explanation: the issuer sets them.
var registeredClaims = []string{"iss", "sub", "aud", "exp", "nbf", "iat", "jti", "azp", "nonce", "at_hash", "c_hash", "auth_time"}

// Explain answers "what would a token for audience carry for this caller",
// with the same steps the issuer takes with token scoping enforced: Evaluate,
// ScopeGroups(audience); the claims are
// the evaluated ones, every held group's fragments, as a token carries them. An audience that is not a declared
// client is an error, not an empty answer.
func (p Policy) Explain(audience string, in Input) (Explanation, error) {
	client, ok := p.Clients[audience]
	if !ok {
		return Explanation{}, fmt.Errorf("%q is not a declared client", audience)
	}
	result := p.Evaluate(in)
	kept, dropped := p.ScopeGroups(audience, result.Groups)
	out := Explanation{
		Audience: audience,
		Held:     result.Groups,
		Kept:     kept,
		Dropped:  []DroppedGroup{},
	}
	if lifetime := client.Cap(result.Lifetime); lifetime > 0 {
		out.Lifetime = lifetime.String()
	}
	for _, name := range client.Requires {
		if result.Has(name) {
			out.Admitted, out.AdmittedBy = true, name
			break
		}
	}
	for _, name := range dropped {
		out.Dropped = append(out.Dropped, DroppedGroup{
			Group:  name,
			Reason: "outside the audience's scope: not named by the client's requires, and not kept by a groups override",
		})
	}
	claims, _ := Plain(result.Claims).(map[string]any)
	if claims == nil {
		claims = map[string]any{}
	}
	delete(claims, "groups")
	for _, name := range registeredClaims {
		delete(claims, name)
	}
	out.Claims = claims
	return out, nil
}

// WorkloadInput reads a workload's token subject into the policy input it
// proves: a Kubernetes ServiceAccount in any spelling
// ([ParseServiceAccountSubject]) or an AWS role
// (`aws:<account>:role/<path><name>`).
func WorkloadInput(subject string) (Input, bool) {
	if ref, ok := ParseServiceAccountSubject(subject); ok {
		return Input{ServiceAccount: &ref}, true
	}
	if rest, ok := strings.CutPrefix(subject, "aws:"); ok {
		account, role, ok := strings.Cut(rest, ":role/")
		if !ok || account == "" || role == "" {
			return Input{}, false
		}
		path := "/"
		name := role
		if i := strings.LastIndex(role, "/"); i >= 0 {
			path, name = "/"+role[:i+1], role[i+1:]
		}
		if name == "" {
			return Input{}, false
		}
		return Input{AWS: &AWSRole{Account: account, Name: name, Path: path}}, true
	}
	return Input{}, false
}

// ExplainInput is the policy input for a person: the address and the directory
// groups the hub reports for it, taken as authoritative.
func ExplainInput(email string, directoryGroups []string) Input {
	return Input{Email: email, DirectoryGroups: slices.Clone(directoryGroups), Authoritative: true}
}
