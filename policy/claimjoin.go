package policy

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ClaimJoinList and ClaimJoinString are the two shapes [ClaimJoin.As]
// accepts: a one-element list (the default) or a bare string.
const (
	ClaimJoinList   = "list"
	ClaimJoinString = "string"
)

// ClaimJoin declares that the claim at a dotted path is not the union of
// the groups' lists but ONE string made from them: every string any merged
// group contributes, sorted and de-duplicated, joined with Join and wrapped
// in Prefix and Suffix. It exists for a filter a relying party applies
// verbatim, where two grants must OR and a list would AND.
type ClaimJoin struct {
	// Join goes between the strings. Required, so a declaration that joins
	// nothing cannot be written by omission.
	Join string `yaml:"join"`
	// Prefix and Suffix wrap the joined string.
	Prefix string `yaml:"prefix,omitempty"`
	Suffix string `yaml:"suffix,omitempty"`
	// As is "list" (default): a one-element list; or "string".
	As string `yaml:"as,omitempty"`
}

// checkClaimMerge refuses a malformed declaration, and a fragment that sets
// a declared path to anything but a list of strings.
func (p Policy) checkClaimMerge() error {
	for _, path := range slices.Sorted(maps.Keys(p.ClaimMerge)) {
		rule := p.ClaimMerge[path]
		if path == "" || slices.Contains(strings.Split(path, "."), "") {
			return fmt.Errorf("claim_merge: %q is not a dotted claim path", path)
		}
		if rule.Join == "" {
			return fmt.Errorf("claim_merge: %s: join must not be empty", path)
		}
		switch rule.As {
		case "", ClaimJoinList, ClaimJoinString:
		default:
			return fmt.Errorf("claim_merge: %s: as is %q, want %q or %q", path, rule.As, ClaimJoinList, ClaimJoinString)
		}
		for _, name := range slices.Sorted(maps.Keys(p.Claims)) {
			if _, err := fragmentStrings(p.Claims[name], strings.Split(path, ".")); err != nil {
				return fmt.Errorf("claim_merge: claims.%s at %s: %w", name, path, err)
			}
		}
	}
	return nil
}

// fragmentStrings reads the strings a fragment holds at path. A path the
// fragment does not set yields none; one that is set must be a list of
// strings, and a path that runs through a non-map is refused.
func fragmentStrings(node any, path []string) ([]string, error) {
	for i, seg := range path {
		m, ok := asMap(node)
		if !ok {
			return nil, fmt.Errorf("%s is a %T, not a map", strings.Join(path[:i], "."), node)
		}
		next, set := m[seg]
		if !set {
			return nil, nil
		}
		node = next
	}
	list, ok := node.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a list of strings, found %T", node)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be a list of strings, found a %T member", v)
		}
		out = append(out, str)
	}
	return out, nil
}

// joinClaims replaces, in the merged claims, each declared path's unioned
// list with its joined form. A path no group contributed to is left out, and
// so is a parent map the removal empties. It runs on whatever set of
// fragments was merged, so a token scoped to fewer groups joins fewer.
func (p Policy) joinClaims(claims map[string]any) {
	for _, path := range slices.Sorted(maps.Keys(p.ClaimMerge)) {
		rule := p.ClaimMerge[path]
		segs := strings.Split(path, ".")
		replaceAt(claims, segs, func(cur any) (any, bool) {
			list, ok := cur.([]any)
			if !ok {
				return nil, false
			}
			parts := make([]string, 0, len(list))
			for _, v := range list {
				if str, ok := v.(string); ok {
					parts = append(parts, str)
				}
			}
			if len(parts) == 0 {
				return nil, false
			}
			slices.Sort(parts)
			parts = slices.Compact(parts)
			joined := rule.Prefix + strings.Join(parts, rule.Join) + rule.Suffix
			if rule.As == ClaimJoinString {
				return joined, true
			}
			return []any{joined}, true
		})
	}
}

// replaceAt applies fn to the value at segs inside m; when fn reports false
// the key is deleted, and a map left empty is deleted from its parent.
func replaceAt(m map[string]any, segs []string, fn func(any) (any, bool)) {
	cur, ok := m[segs[0]]
	if !ok {
		return
	}
	if len(segs) == 1 {
		if v, keep := fn(cur); keep {
			m[segs[0]] = v
		} else {
			delete(m, segs[0])
		}
		return
	}
	inner, ok := asMap(cur)
	if !ok {
		return
	}
	replaceAt(inner, segs[1:], fn)
	if len(inner) == 0 {
		delete(m, segs[0])
	}
}
