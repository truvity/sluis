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
// groups kept and dropped by [Policy.ScopeGroups] and the claim fragments of
// the groups kept ([Policy.ClaimsFor]). It holds no
// secret and no registered claim (`iss`, `sub`, `aud`, `exp`...): only what the
// policy decides.
type Explanation struct {
	Audience string         `json:"audience" yaml:"audience"`
	Held     []string       `json:"held" yaml:"held"`
	Kept     []string       `json:"groups_kept" yaml:"groups_kept"`
	Dropped  []DroppedGroup `json:"groups_dropped" yaml:"groups_dropped"`
	Claims   map[string]any `json:"claims" yaml:"claims"`
}

// registeredClaims are never part of an explanation: the issuer sets them.
var registeredClaims = []string{"iss", "sub", "aud", "exp", "nbf", "iat", "jti", "azp", "nonce", "at_hash", "c_hash", "auth_time"}

// Explain answers "what would a token for audience carry for this caller",
// with the same steps the issuer takes with token scoping enforced: Evaluate,
// ScopeGroups(audience), ClaimsFor(kept). An audience that is not a declared
// client is an error, not an empty answer.
func (p Policy) Explain(audience string, in Input) (Explanation, error) {
	if _, ok := p.Clients[audience]; !ok {
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
	for _, name := range dropped {
		out.Dropped = append(out.Dropped, DroppedGroup{
			Group:  name,
			Reason: "outside the audience's scope: not named by the client's requires, and not kept by a groups override",
		})
	}
	claims, _ := Plain(p.ClaimsFor(kept)).(map[string]any)
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
