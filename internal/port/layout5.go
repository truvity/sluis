package port

import (
	"fmt"
	"sort"
	"strings"
)

// The storage layout, version 5: module first.
//
// A module owns its records. Every State key of the service (its logical key,
// which does not change) is located in one module, as a KIND and an ID within
// the module's table or partition space:
//
//	(module, kind, id)
//
// The kinds lose the module prefix they carry in layout 4 (`github-org` is the
// kind `org` of the module `github`; `slack-workspace` is `workspace` of
// `slack`; `directory/google/<id>` is `workspace/<id>` of `google`;
// `issuer-token` is `token` of `oidc`). The layout 4 mapping is frozen in
// keys_v4.go ([Locate], [LocatePrefix], [LocateSet], [Kinds]) and is what
// `migrate` reads the source with and what the compatibility layout writes.
//
// A few families belong to no one module: the leases and notifications of the
// controllers, and the generic gate, cache and dedupe families. Every table
// holds them (`lease` and `notify` are in every module's table), so their
// [Address5.Module] is empty and the adapter uses the module of the table it
// is serving, except for a lease whose target names its module (a
// `github-tick` lease is the github module's).

// Module is a unit of ownership: its records, parameters and function.
type Module string

// The modules of layout 5.
const (
	ModuleOIDC       Module = "oidc"
	ModuleGitHub     Module = "github"
	ModuleSlack      Module = "slack"
	ModuleCloudflare Module = "cloudflare"
	ModuleGoogle     Module = "google"
	ModuleBackup     Module = "backup"
)

// Modules lists every module, in the order above.
func Modules() []Module {
	return []Module{ModuleOIDC, ModuleGitHub, ModuleSlack, ModuleCloudflare, ModuleGoogle, ModuleBackup}
}

// Valid reports whether m is one of [Modules].
func (m Module) Valid() bool {
	for _, k := range Modules() {
		if m == k {
			return true
		}
	}
	return false
}

// ParseModule is the module of that name.
func ParseModule(s string) (Module, error) {
	if m := Module(s); m.Valid() {
		return m, nil
	}
	return "", fmt.Errorf("%w: %q is not a module", ErrUnsupported, s)
}

// SharedKinds are the kinds every module's table holds.
var SharedKinds = []string{"lease", "notify", "maintenance"}

// Address5 is where a key lives in layout 5. Module is empty for a key of no
// one module (see the file comment).
type Address5 struct {
	Module Module
	Kind   string
	ID     string
}

// String is `<module>/<kind>/<id>`, or `<kind>/<id>` when no module owns it.
func (a Address5) String() string {
	if a.Module == "" {
		return a.Kind + "/" + a.ID
	}
	return string(a.Module) + "/" + a.Kind + "/" + a.ID
}

// Shared reports whether no one module owns the key.
func (a Address5) Shared() bool { return a.Module == "" }

type rule5 struct {
	keyRule
	module Module
	// moduleOf picks the module from the rest of the key when no single one
	// owns the family; it returns "" for none.
	moduleOf func(rest string) Module
}

func (r rule5) moduleFor(key string) Module {
	if r.moduleOf != nil {
		return r.moduleOf(key[len(r.prefix):])
	}
	return r.module
}

// leaseModule is the module a lease target belongs to: `github-tick:acme` and
// `export:github-app.x` are the github module's.
func leaseModule(rest string) Module {
	kind, target, _ := strings.Cut(rest, ":")
	if kind == "export" {
		kind, _, _ = strings.Cut(target, ".")
	}
	switch {
	case strings.HasPrefix(kind, "github"):
		return ModuleGitHub
	case strings.HasPrefix(kind, "slack"):
		return ModuleSlack
	case strings.HasPrefix(kind, "cloudflare"):
		return ModuleCloudflare
	case kind == "refresh":
		return ModuleGoogle
	}
	return ""
}

func r5(m Module, kind, prefix string, c conv) rule5 {
	return rule5{keyRule: keyRule{prefix: prefix, kind: kind, conv: c}, module: m}
}

func r5exact(m Module, kind, prefix, id string) rule5 {
	return rule5{keyRule: keyRule{prefix: prefix, kind: kind, exact: id}, module: m}
}

// rules5 are the State key families of layout 5, in the order of keyRules.
// TestLayout5NamesEveryLayout4Family holds the two tables to the same prefixes.
var rules5 = []rule5{
	// google: a directory workspace is `ws.dir.google.<id>`; another backend
	// has no module yet and is refused.
	{keyRule: keyRule{prefix: "ws.dir.", kind: "workspace", conv: convDots, strip: "google."}, module: ModuleGoogle},

	r5(ModuleSlack, "workspace", "ws.slack.", convID),
	r5(ModuleSlack, "app", "app.slack.cat.", convID),
	r5(ModuleSlack, "shared", "rec.slack.shared.", convID),
	r5(ModuleSlack, "channel", "rec.slack.channel.", convDots),
	r5(ModuleSlack, "gate", "gate.slack.", convDots),
	r5(ModuleSlack, "share", "share.", convDots),
	r5(ModuleSlack, "user-cache", "cache.slack.user.", convDots),

	// github: one kind `app` for every App, keyed by its id; the link App is
	// `link`, a catalogue App is its id, a runner App is `runner-<tier>-<org>`
	// (the id its exported key has had since ADR 0041). A catalogue id may be
	// neither `link` nor begin `runner-`.
	r5(ModuleGitHub, "org", "gh.org.", convID),
	r5(ModuleGitHub, "link", "gh.link.", convID),
	r5exact(ModuleGitHub, "app", "app.gh.link", "link"),
	{keyRule: keyRule{prefix: "app.gh.cat.", kind: "app", refuse: "link", refusePrefix: "runner-"}, module: ModuleGitHub},
	{keyRule: keyRule{prefix: "app.gh.runner.", kind: "app", conv: convDashes, idPrefix: "runner-"}, module: ModuleGitHub},
	r5(ModuleGitHub, "claim", "gate.github-claim.", convID),
	r5(ModuleGitHub, "gate", "gate.github.", convDots),

	// oidc: the issuer's records and the console's session key.
	r5exact(ModuleOIDC, "console", "rec.console.session-key", "session-key"),
	r5(ModuleOIDC, "session", "ses.", convDots),
	r5(ModuleOIDC, "session-pointer", "sid.", convID),
	r5(ModuleOIDC, "request", "req.", convID),
	r5(ModuleOIDC, "code", "code.", convID),
	r5(ModuleOIDC, "code-session", "codesess.", convID),
	r5(ModuleOIDC, "token", "tok.", convID),
	r5(ModuleOIDC, "sso", "sso.", convID),
	r5(ModuleOIDC, "session-token", "rt.", convID),
	r5(ModuleOIDC, "session-rotated", "rtrot.", convID),
	r5(ModuleOIDC, "keyring", "keyring.", convColons),
	r5(ModuleOIDC, "request", "issuer:request:", convID),
	r5(ModuleOIDC, "code", "issuer:code:", convID),
	r5(ModuleOIDC, "code-session", "issuer:code-session:", convID),
	r5(ModuleOIDC, "token", "issuer:token:", convID),
	r5(ModuleOIDC, "sso", "issuer:sso:", convID),
	r5(ModuleOIDC, "sso-of", "issuer:sso-of:", convID),
	r5(ModuleOIDC, "sso-cookie", "issuer:sso-cookie:", convID),
	r5(ModuleOIDC, "session", "issuer:session:", convID),
	r5(ModuleOIDC, "session-token", "issuer:session-token:", convID),
	r5(ModuleOIDC, "session-rotated", "issuer:session-rotated:", convID),
	r5(ModuleOIDC, "keyring", "issuer:keyring:entry:", convColons),
	r5(ModuleOIDC, "keyring-retired", "issuer:keyring:retired:", convColons),
	r5(ModuleOIDC, "held", "issuer:held:", convID),
	r5(ModuleOIDC, "guard", "issuer:kms:", convID),

	// No one module: the generic families, and the controllers' leases and
	// notifications, which every table holds.
	r5("", "gate", "gate.", convDots),
	r5("", "cache", "cache.", convDots),
	r5("", "dedupe", "dedupe.", convID),
	{keyRule: keyRule{prefix: "lease.", kind: "lease", conv: convLease}, moduleOf: leaseModule},
	r5("", "notify", "notify.", convID),
	r5exact("", "maintenance", "rec.maintenance", "flag"),
}

// setRules5 are the Index sets of layout 5; all are the oidc module's.
var setRules5 = []rule5{
	r5(ModuleOIDC, "keyring-index", "issuer:keyring:index:", convID),
	r5(ModuleOIDC, "sso-clients", "issuer:sso-clients:", convID),
	r5(ModuleOIDC, "sessions-of", "issuer:sessions-of:", convID),
	r5(ModuleOIDC, "sessions-for", "issuer:sessions-for:", convID),
	r5exact(ModuleOIDC, "sso-index", "issuer:sso", "all"),
	r5exact(ModuleOIDC, "sessions-index", "issuer:sessions", "all"),
}

func init() {
	for _, rules := range [][]rule5{rules5, setRules5} {
		sort.SliceStable(rules, func(i, j int) bool { return len(rules[i].prefix) > len(rules[j].prefix) })
	}
}

// Locate5 is the layout 5 address of a State key. A key no rule names is the
// kind [KindOther] of no module, with the whole key as its id. A key of a
// backend with no module (`ws.dir.entra.x`) is refused.
func Locate5(key string) (Address5, error) {
	if key == "" {
		return Address5{}, fmt.Errorf("%w: an empty key", ErrUnsupported)
	}
	for i := range rules5 {
		r := &rules5[i]
		if !r.matches(key) {
			continue
		}
		id, err := r.id(key)
		if err != nil {
			return Address5{}, err
		}
		return Address5{Module: r.moduleFor(key), Kind: r.kind, ID: id}, nil
	}
	return Address5{Kind: KindOther, ID: key}, nil
}

// LocatePrefix5 is the one module and kind a listing prefix lies in, and the
// prefix of the ids in it. ok is false when the prefix does not name one
// family, or the family's module depends on the rest of the key (a `lease.`
// listing is a scan of the table it is made in).
func LocatePrefix5(prefix string) (module Module, kind, idPrefix string, ok bool) {
	for i := range rules5 {
		r := &rules5[i]
		if r.exact != "" || !strings.HasPrefix(prefix, r.prefix) {
			continue
		}
		for i := range rules5 {
			o := &rules5[i]
			if o.prefix != r.prefix && strings.HasPrefix(o.prefix, prefix) {
				return "", "", "", false
			}
		}
		rest := prefix[len(r.prefix):]
		if r.strip != "" {
			if rest != "" && strings.HasPrefix(r.strip, rest) && rest != r.strip {
				return "", "", "", false // part of the backend's name
			}
			if rest != "" {
				if !strings.HasPrefix(rest, r.strip) {
					return "", "", "", false
				}
				rest = rest[len(r.strip):]
			}
		}
		id, fits := r.convert(rest)
		if !fits {
			return "", "", "", false
		}
		m := r.module
		if r.moduleOf != nil {
			// The module is known only once the lease's kind is complete.
			kind, target, colon := strings.Cut(rest, ":")
			if !colon || (kind == "export" && !strings.Contains(target, ".")) {
				return "", "", "", false
			}
			m = r.moduleOf(rest)
		}
		return m, r.kind, r.idPrefix + id, true
	}
	return "", "", "", false
}

// LocateSet5 is the layout 5 address of an Index set.
func LocateSet5(set string) (Address5, error) {
	if set == "" {
		return Address5{}, fmt.Errorf("%w: an empty index set", ErrUnsupported)
	}
	for i := range setRules5 {
		r := &setRules5[i]
		if !r.matches(set) {
			continue
		}
		if r.exact != "" {
			return Address5{Module: r.module, Kind: r.kind, ID: r.exact}, nil
		}
		if rest := set[len(r.prefix):]; rest != "" {
			return Address5{Module: r.module, Kind: r.kind, ID: EscapeSegment(rest)}, nil
		}
	}
	return Address5{Kind: KindIndexOther, ID: EscapeSegment(set)}, nil
}

// Kinds5 lists every `<module>/<kind>` the layout names (`*/<kind>` for a kind
// of no one module), State first, then the Index sets', each sorted.
func Kinds5() (state, sets []string) {
	seen := map[string]bool{}
	name := func(r *rule5) string {
		if r.module == "" {
			return "*/" + r.kind
		}
		return string(r.module) + "/" + r.kind
	}
	for i := range rules5 {
		r := &rules5[i]
		if n := name(r); !seen[n] {
			seen[n] = true
			state = append(state, n)
		}
	}
	for i := range setRules5 {
		r := &setRules5[i]
		sets = append(sets, name(r))
	}
	sort.Strings(state)
	sort.Strings(sets)
	return state, sets
}

// LocateModule5 is the one module every State key that begins with prefix
// belongs to, even when the prefix spans several kinds (`issuer:` is all
// oidc's). ok is false when two modules share the prefix, when a key of no
// module (a lease, a gate) can begin with it, or when the prefix is empty.
func LocateModule5(prefix string) (Module, bool) {
	if prefix == "" {
		return "", false
	}
	var found Module
	for i := range rules5 {
		r := &rules5[i]
		if !strings.HasPrefix(r.prefix, prefix) && !strings.HasPrefix(prefix, r.prefix) {
			continue
		}
		if r.moduleOf != nil || r.module == "" || (found != "" && found != r.module) {
			return "", false
		}
		found = r.module
	}
	return found, found != ""
}
