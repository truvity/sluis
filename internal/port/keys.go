package port

import (
	"fmt"
	"strings"
)

// What the layouts of State keys share (layout5.go is the layout): an address
// is a KIND and an ID, and a rule maps the logical key of a family to it.
//
// The logical keys of State (`ws.dir.<id>`, `issuer:token:<jti>`, ...) are the
// service's own vocabulary and do not change. What an adapter writes into its
// engine is derived from a key in one place, for every adapter:
//
//   - a key is a record KIND (a readable noun: `workspace`, `org`, `token`) and
//     an ID within it, slash-separated when it is compound (`stable/acme`);
//   - DynamoDB stores the kind as `pk` and the id as `sk`, in the table of the
//     key's module.
//
// A key no rule names is the kind [KindOther] with the whole key as its id: it
// is correct (and listable by a scan) and a new family is named in layout5.go.
//
// The mapping of layout 4 (one table, kinds such as `github-org`) was removed
// in v1.77.1.

// KindOther is the kind of a key the layout names no family for.
const KindOther = "other"

// KindIndexOther is the kind of an Index set no rule names.
const KindIndexOther = "index"

// Address is where a key lives: its kind and its id within it.
type Address struct {
	Kind string
	ID   string
}

// String is `<kind>/<id>`.
func (a Address) String() string { return a.Kind + "/" + a.ID }

type conv int

const (
	// convID keeps the rest of the key as the id.
	convID conv = iota // the zero value: the rest of the key is the id
	// convDots makes every dot of the rest a slash (the rest holds no slash).
	convDots
	// convColons makes every colon of the rest a slash (the rest holds no slash).
	convColons
	// convDashes makes every dot of the rest a dash (the rest holds no slash):
	// a runner App's `<tier>.<org>` is the App id `<tier>-<org>`.
	convDashes
	// convLease makes the first colon a slash: `<kind>:<target>`.
	convLease
)

type keyRule struct {
	prefix string
	kind   string
	conv   conv
	// exact is a key that is the whole family: its id is fixed.
	exact string
	// refuse is a rest the rule cannot hold without clashing with an exact.
	refuse string
	// refusePrefix is a start of the rest the rule cannot hold without
	// clashing with another family of the same kind.
	refusePrefix string
	// strip is a start the rest must have; it is not part of the id.
	strip string
	// idPrefix is put in front of the id.
	idPrefix string
}

func (r keyRule) matches(key string) bool {
	if r.exact != "" {
		return key == r.prefix
	}
	return strings.HasPrefix(key, r.prefix)
}

func (r keyRule) id(key string) (string, error) {
	if r.exact != "" {
		return r.exact, nil
	}
	rest := key[len(r.prefix):]
	if r.strip != "" {
		if !strings.HasPrefix(rest, r.strip) {
			return "", fmt.Errorf("%w: %q is not a %q key", ErrUnsupported, key, r.strip)
		}
		rest = rest[len(r.strip):]
	}
	if rest == "" {
		return "", fmt.Errorf("%w: %q names no id", ErrUnsupported, key)
	}
	if rest == r.refuse {
		return "", fmt.Errorf("%w: %q: the id %q is the link App's", ErrUnsupported, key, rest)
	}
	if r.refusePrefix != "" && strings.HasPrefix(rest, r.refusePrefix) {
		return "", fmt.Errorf("%w: %q: the id starts with %q, which is the runner Apps'", ErrUnsupported, key, r.refusePrefix)
	}
	out, ok := r.convert(rest)
	if !ok {
		return "", fmt.Errorf("%w: %q holds a slash", ErrUnsupported, key)
	}
	return r.idPrefix + out, nil
}

func (r keyRule) convert(rest string) (string, bool) {
	switch r.conv {
	case convID:
		return rest, true
	case convDots:
		return strings.ReplaceAll(rest, ".", "/"), !strings.Contains(rest, "/")
	case convColons:
		return strings.ReplaceAll(rest, ":", "/"), !strings.Contains(rest, "/")
	case convDashes:
		return strings.ReplaceAll(rest, ".", "-"), !strings.Contains(rest, "/")
	case convLease:
		return strings.Replace(rest, ":", "/", 1), !strings.Contains(rest, "/")
	}
	return rest, true
}

// EscapeSegment makes a string one path segment: `~` and `/` become `~7E` and
// `~2F`.
func EscapeSegment(s string) string {
	if !strings.ContainsAny(s, "~/") {
		return s
	}
	return strings.NewReplacer("~", "~7E", "/", "~2F").Replace(s)
}

// CredentialsPrefix is the Secrets path under which credentials live when the
// secrets are a Secrets port and not the layout v5 stores: a record's at
// `credentials/<module>/<kind>/<id>/<ref>` (internal/portstore), a generated
// client's at `credentials/oidc-client/<id>/secret`.
const CredentialsPrefix = "credentials/"
