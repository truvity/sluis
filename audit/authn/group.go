package authn

import (
	"slices"
	"strings"
)

// The grammar of a group name in the estate's vocabulary, which sluis (the issuer,
// formerly access-roster) defines and every relying party reads: a grant is
// named `<scope>:<thing>:<role>`, three segments, or five when the thing is
// `<vendor>:<project>:<customer>`, none empty.
//
// It is written out here, and not imported from sluis, so that this module does
// not require sluis: sluis is to import this module's SQS sink, and a module
// cannot require one that requires it. The two must agree, which is what
// TestSplitGroupAgreesWithSluis holds this to, on the table of cases sluis's own
// test (policy.TestSplitGroupReadsAnyGrant, sluis v1.63.0) holds its reader to.

const (
	// GroupSeparator separates the segments of a group name.
	GroupSeparator = ":"
	// ScopeAll is the scope of a role over the whole installation rather than one
	// of its tenants. What it means is the reader's to decide: this preset reads
	// it as every tenant.
	ScopeAll = "all"
)

// SplitGroup reads a group name as a grant: the scope the role is held in, the
// thing it is held on, and the role. The scope is the first segment, the role the
// last, and the thing is everything between them, joined by GroupSeparator. It
// reports false for a name that is not a grant: two segments, four, six or more,
// or an empty segment. The scope is returned as written, so ScopeAll comes back
// as "all".
func SplitGroup(name string) (scope, thing, role string, ok bool) {
	parts := strings.Split(name, GroupSeparator)
	if len(parts) != 3 && len(parts) != 5 {
		return "", "", "", false
	}
	if slices.Contains(parts, "") {
		return "", "", "", false
	}
	last := len(parts) - 1
	return parts[0], strings.Join(parts[1:last], GroupSeparator), parts[last], true
}
