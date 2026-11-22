// Package groupsclaim holds the rule for the `groups` claim of a token, written once for the two actors that issue
// tokens. session issues the ID and access tokens of a sign-in and a refresh, and exchange issues the access token of an
// exchange; both give an audience the same groups from the same held set (ADR 0020, "The claims of a token").
// kits/conformance/token/groups-vectors.json fixes the rule, and the store's loader is held to the same file for the
// delimiter it accepts.
//
// The rule has two steps and the order matters. Narrowing keeps the held grant names the audience may read, and works on
// the names as they are written, `<scope>:<thing>:<role>` or `<scope>:<vendor>:<project>:<customer>:<role>` (ADR 0055),
// whose thing is the three words between the scope and the role. The delimiter then respells each kept name for a verifier
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
// whose thing is among things, in any scope and role. A held name that is not a grant name of three parts or five (see
// Split), such as one that holds a wildcard, is never kept, and a requires entry of another shape names no pair. A thing of
// three words is matched whole, like a thing of one. The result is sorted and has no repeats.
func Narrow(held, requires, things []string) []string {
	pairs := map[string]bool{}
	for _, r := range requires {
		if scope, thing, _, ok := Split(r); ok {
			pairs[scope+Separator+thing] = true
		}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, h := range held {
		scope, thing, _, ok := Split(h)
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

// The segment counts of a grant name: `<scope>:<thing>:<role>`, and the longer form whose thing is
// `<vendor>:<project>:<customer>` (ADR 0055).
const (
	grantSegments     = 3
	grantSegmentsLong = 5
	segmentMax        = 64
)

// Split reads a grant name into its scope, thing and role, by its shape alone (ADR 0020, ADR 0055): three segments or five,
// each 1 to 64 ASCII letters, digits, `-` and `_`. The scope is the first segment, the role the last, and the thing is
// everything between them, joined by Separator: one word, or `<vendor>:<project>:<customer>`. Two segments, four, six or
// more, an empty segment, a wildcard and any other character are not a grant name, and ok is false.
// kits/conformance/token/grant-name-vectors.json fixes the rule, which sluis v1's policy.SplitGroup follows too.
func Split(name string) (scope, thing, role string, ok bool) {
	parts := strings.Split(name, Separator)
	if len(parts) != grantSegments && len(parts) != grantSegmentsLong {
		return "", "", "", false
	}
	if !slices.ContainsFunc(parts, badSegment) {
		last := len(parts) - 1
		return parts[0], strings.Join(parts[1:last], Separator), parts[last], true
	}
	return "", "", "", false
}

// badSegment reports whether s cannot be a segment of a grant name.
func badSegment(s string) bool {
	if s == "" || len(s) > segmentMax {
		return true
	}
	for _, b := range []byte(s) {
		ok := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
		if !ok {
			return true
		}
	}
	return false
}
