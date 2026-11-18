// Package groupsclaim holds the rule for the `groups` claim of a token, written once for the two actors that issue
// tokens. session issues the ID and access tokens of a sign-in and a refresh, and exchange issues the access token of an
// exchange; both give an audience the same groups from the same held set (ADR 0020, "The claims of a token").
// kits/conformance/token/groups-vectors.json fixes the rule, and the store's loader is held to the same file for the
// delimiter it accepts.
//
// The rule has two steps and the order matters. Narrowing keeps the held grant names the audience may read, and works on
// the names as they are written, `<scope>:<thing>:<role>`. The delimiter then respells each kept name for a verifier
// that splits a group on `:`. Neither step touches what `requires` admits: admission is decided on the full held set.
package groupsclaim

import (
	"slices"
	"strings"
)

// Separator is what a grant name is written with between its scope, its thing and its role.
const Separator = ":"

// Audience is what a client declares about the groups its tokens carry.
type Audience struct {
	// Requires are the grant names the audience's sign-in needs; their `<scope>:<thing>` pairs are what a token keeps.
	Requires []string
	// Things are the things whose grant names the audience reads in every scope and role, beyond the pairs of Requires.
	Things []string
	// Delimiter replaces each Separator in a name; empty keeps it.
	Delimiter string
}

// For is the `groups` claim of a token for the audience: the held grant names narrowed, then respelled with the
// delimiter. The result is sorted by the names as written, has no repeats and is never nil, so a caller decides whether an
// empty claim is an empty array or no claim.
func For(held []string, a Audience) []string {
	return Respell(Narrow(held, a.Requires, a.Things), a.Delimiter)
}

// Narrow keeps each held grant name whose `<scope>:<thing>` pair is among the pairs of requires, in any role, and each
// whose thing is among things, in any scope and role. A held name that is not a grant name of three parts, or that holds
// a wildcard, is never kept, and a requires entry of that shape names no pair. The result is sorted and has no repeats.
func Narrow(held, requires, things []string) []string {
	pairs := map[string]bool{}
	for _, r := range requires {
		if scope, thing, _, ok := split(r); ok {
			pairs[scope+Separator+thing] = true
		}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, h := range held {
		scope, thing, _, ok := split(h)
		if !ok || seen[h] {
			continue
		}
		if pairs[scope+Separator+thing] || slices.Contains(things, thing) {
			seen[h] = true
			out = append(out, h)
		}
	}
	slices.Sort(out)
	return out
}

// Respell writes each name with every Separator replaced by delimiter. An empty delimiter returns the names unchanged.
// It is sound only because a grant name holds no character of a delimiter the loader accepts (ADR 0020 gives a name
// letters, digits, `-` and `_`), so two different names never become one.
func Respell(names []string, delimiter string) []string {
	if delimiter == "" {
		return names
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strings.ReplaceAll(n, Separator, delimiter)
	}
	return out
}

// split reads a grant name `<scope>:<thing>:<role>`. A name of another shape, with an empty part or a wildcard, has none.
func split(name string) (scope, thing, role string, ok bool) {
	if strings.Contains(name, "*") {
		return "", "", "", false
	}
	parts := strings.Split(name, Separator)
	if len(parts) != 3 || slices.Contains(parts, "") {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
