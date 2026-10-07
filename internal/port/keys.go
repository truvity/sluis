package port

import (
	"fmt"
	"sort"
	"strings"
)

// The storage layout, version 2, of the adapters that are not the legacy one
// (docs/reference/storage-layout.md).
//
// The logical keys of State (`ws.dir.<id>`, `issuer:token:<jti>`, ...) are the
// service's own vocabulary and do not change: the legacy adapter and
// `sluis migrate` read the source with them. What an adapter writes into its
// engine is derived from a key here, in one place and for every adapter:
//
//   - a key is a record KIND (a readable noun: `workspace`, `github-org`,
//     `issuer-token`) and an ID within it, slash-separated when it is
//     compound (`stable/acme`);
//   - DynamoDB stores the kind as `pk` and the id as `sk`;
//   - the Secrets of a credential live under `credentials/<kind>/<id>/<ref>`,
//     which the ssm adapter puts at `/sluis/private/credentials/...`.
//
// A key no rule names is the kind [KindOther] with the whole key as its id: it
// is correct (and listable by a scan) and a new family is named here.

// KindOther is the kind of a key the layout names no family for.
const KindOther = "other"

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
}

// keyRules are the State key families, one line per kind a record or a
// credential has. Every kind written anywhere in the service is here; the
// tests pin each one.
var keyRules = []keyRule{
	// Domain records (internal/portstore); the ones with a credential keep it
	// in Secrets under the same kind.
	// A directory workspace is `directory/<provider>/<id>`: the provider (the
	// record's backend: google, later entra) is a segment, not a family.
	{prefix: "ws.dir.", kind: "directory", conv: convDots},
	{prefix: "ws.slack.", kind: "slack-workspace"},
	{prefix: "gh.org.", kind: "github-org"},
	{prefix: "gh.link.", kind: "github-link"},
	{prefix: "app.gh.link", kind: "github-app", exact: "link"},
	{prefix: "app.gh.cat.", kind: "github-app", refuse: "link"},
	{prefix: "app.gh.runner.", kind: "github-runner-app", conv: convDots},
	{prefix: "app.slack.cat.", kind: "slack-app"},
	{prefix: "rec.slack.shared.", kind: "slack-shared"},
	{prefix: "rec.slack.channel.", kind: "slack-channel", conv: convDots},
	{prefix: "rec.console.session-key", kind: "console", exact: "session-key"},
	{prefix: "gate.github-claim.", kind: "github-claim"},
	{prefix: "gate.github.", kind: "github-gate", conv: convDots},
	{prefix: "gate.slack.", kind: "slack-gate", conv: convDots},
	{prefix: "gate.", kind: "gate", conv: convDots},
	{prefix: "share.", kind: "slack-share", conv: convDots},
	{prefix: "cache.slack.user.", kind: "slack-user-cache", conv: convDots},
	{prefix: "cache.", kind: "cache", conv: convDots},
	{prefix: "dedupe.", kind: "dedupe"},
	// Leases and notifications of the controllers: `lease.<kind>:<target>`,
	// kinds `github-tick`, `github-links`, `slack-tick`, and the hub's `refresh`.
	{prefix: "lease.", kind: "lease", conv: convLease},
	{prefix: "notify.", kind: "notify"},

	// The issuer's records, in the layout's dotted form ...
	{prefix: "ses.", kind: "session", conv: convDots},
	{prefix: "sid.", kind: "session-pointer"},
	{prefix: "req.", kind: "issuer-request"},
	{prefix: "code.", kind: "issuer-code"},
	{prefix: "codesess.", kind: "issuer-code-session"},
	{prefix: "tok.", kind: "issuer-token"},
	{prefix: "sso.", kind: "issuer-sso"},
	{prefix: "rt.", kind: "issuer-session-token"},
	{prefix: "rtrot.", kind: "issuer-session-rotated"},
	{prefix: "keyring.", kind: "keyring", conv: convColons},
	// ... and in the form the issuer writes today, one kind each.
	{prefix: "issuer:request:", kind: "issuer-request"},
	{prefix: "issuer:code:", kind: "issuer-code"},
	{prefix: "issuer:code-session:", kind: "issuer-code-session"},
	{prefix: "issuer:token:", kind: "issuer-token"},
	{prefix: "issuer:sso:", kind: "issuer-sso"},
	{prefix: "issuer:sso-of:", kind: "issuer-sso-of"},
	{prefix: "issuer:sso-cookie:", kind: "issuer-sso-cookie"},
	{prefix: "issuer:session:", kind: "issuer-session"},
	{prefix: "issuer:session-token:", kind: "issuer-session-token"},
	{prefix: "issuer:session-rotated:", kind: "issuer-session-rotated"},
	{prefix: "issuer:keyring:entry:", kind: "keyring", conv: convColons},
	{prefix: "issuer:keyring:retired:", kind: "keyring-retired", conv: convColons},
	{prefix: "issuer:held:", kind: "issuer-held"},
	{prefix: "issuer:kms:", kind: "issuer-guard"},
}

// setRules are the Index sets: a set is a kind of its own, and a member is
// stored under `<set id>/<member>`.
var setRules = []keyRule{
	{prefix: "issuer:keyring:index:", kind: "keyring-index"},
	{prefix: "issuer:sso-clients:", kind: "sso-clients"},
	{prefix: "issuer:sessions-of:", kind: "sessions-of"},
	{prefix: "issuer:sessions-for:", kind: "sessions-for"},
	{prefix: "issuer:sso", kind: "sso-index", exact: "all"},
	{prefix: "issuer:sessions", kind: "sessions-index", exact: "all"},
}

// KindIndexOther is the kind of an Index set no rule names.
const KindIndexOther = "index"

func init() {
	for _, rules := range [][]keyRule{keyRules, setRules} {
		sort.SliceStable(rules, func(i, j int) bool { return len(rules[i].prefix) > len(rules[j].prefix) })
	}
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
	if rest == "" {
		return "", fmt.Errorf("%w: %q names no id", ErrUnsupported, key)
	}
	if rest == r.refuse {
		return "", fmt.Errorf("%w: %q: the id %q is the link App's", ErrUnsupported, key, rest)
	}
	out, ok := r.convert(rest)
	if !ok {
		return "", fmt.Errorf("%w: %q holds a slash", ErrUnsupported, key)
	}
	return out, nil
}

func (r keyRule) convert(rest string) (string, bool) {
	switch r.conv {
	case convID:
		return rest, true
	case convDots:
		return strings.ReplaceAll(rest, ".", "/"), !strings.Contains(rest, "/")
	case convColons:
		return strings.ReplaceAll(rest, ":", "/"), !strings.Contains(rest, "/")
	case convLease:
		return strings.Replace(rest, ":", "/", 1), !strings.Contains(rest, "/")
	}
	return rest, true
}

// Locate is the address of a State key. A key no rule names is [KindOther].
func Locate(key string) (Address, error) {
	if key == "" {
		return Address{}, fmt.Errorf("%w: an empty key", ErrUnsupported)
	}
	for _, r := range keyRules {
		if r.matches(key) {
			id, err := r.id(key)
			if err != nil {
				return Address{}, err
			}
			return Address{Kind: r.kind, ID: id}, nil
		}
	}
	return Address{Kind: KindOther, ID: key}, nil
}

// LocatePrefix is the one kind a listing prefix lies in, and the prefix of the
// ids in it. ok is false when the prefix does not name one family (`ws.`, an
// empty prefix, `gate.github`): the listing is then a scan.
func LocatePrefix(prefix string) (kind, idPrefix string, ok bool) {
	for _, r := range keyRules {
		if r.exact != "" || !strings.HasPrefix(prefix, r.prefix) {
			continue
		}
		for _, o := range keyRules {
			if o.prefix != r.prefix && strings.HasPrefix(o.prefix, prefix) {
				return "", "", false
			}
		}
		id, fits := r.convert(prefix[len(r.prefix):])
		if !fits {
			return "", "", false
		}
		return r.kind, id, true
	}
	return "", "", false
}

// LocateSet is the address of an Index set: the kind of its family and the
// set's id, which holds no slash.
func LocateSet(set string) (Address, error) {
	if set == "" {
		return Address{}, fmt.Errorf("%w: an empty index set", ErrUnsupported)
	}
	for _, r := range setRules {
		if !r.matches(set) {
			continue
		}
		if r.exact != "" {
			return Address{Kind: r.kind, ID: r.exact}, nil
		}
		if rest := set[len(r.prefix):]; rest != "" {
			return Address{Kind: r.kind, ID: EscapeSegment(rest)}, nil
		}
	}
	return Address{Kind: KindIndexOther, ID: EscapeSegment(set)}, nil
}

// EscapeSegment makes a string one path segment: `~` and `/` become `~7E` and
// `~2F`.
func EscapeSegment(s string) string {
	if !strings.ContainsAny(s, "~/") {
		return s
	}
	return strings.NewReplacer("~", "~7E", "/", "~2F").Replace(s)
}

// CredentialsPrefix is the Secrets path under which the credentials of the
// records live: `credentials/<kind>/<id>/<ref>`.
const CredentialsPrefix = "credentials/"

// Kinds lists every kind the layout names, the State ones first (sorted),
// then the Index sets'. The documentation and the tests are held to it.
func Kinds() (state, sets []string) {
	seen := map[string]bool{}
	for _, r := range keyRules {
		if !seen[r.kind] {
			seen[r.kind] = true
			state = append(state, r.kind)
		}
	}
	for _, r := range setRules {
		sets = append(sets, r.kind)
	}
	sort.Strings(state)
	sort.Strings(sets)
	return state, sets
}
