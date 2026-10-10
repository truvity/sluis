package migrate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/truvity/sluis/internal/githubroster/appid"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secretstore"
	slconn "github.com/truvity/sluis/internal/slackroster/connection"
)

// The mapping from layout v4 to layout v5 (ADR 0072). It is one table in two
// halves, and both halves are the source of truth for `sluis migrate v5`:
//
//   - State: every logical key of a v4 record is located in a module, a kind and
//     an id by the layout's own tables ([port.Locate5]); the logical keys do not
//     change, so a record keeps its key and gains its module's table.
//   - Secrets: every v4 parameter name (below the installation's root) is
//     mapped here to the v5 name. Nothing else computes a v5 secret name for the
//     migration.
//
// The tests hold the table to the readers and writers of both layouts.

var (
	// ErrNotMigrated is a v4 item that has no v5 address on purpose: the reason
	// is in the error.
	ErrNotMigrated = errors.New("migrate: not carried to layout v5")
	// ErrNoTarget is a v4 item layout v5 has no address for.
	ErrNoTarget = errors.New("migrate: no address on layout v5")
)

// Target is the module, kind and id of an item on layout v5.
type Target struct {
	Module string
	Kind   string
	ID     string
}

// String is `<module>/<kind>/<id>`.
func (t Target) String() string {
	if t.ID == "" {
		return t.Module + "/" + t.Kind
	}
	return t.Module + "/" + t.Kind + "/" + t.ID
}

// sharedModule names the families no one module owns in a report.
const sharedModule = "shared"

// StateTarget is where the State record with the logical key lands on layout v5.
func StateTarget(key string) (Target, error) {
	a, err := port.Locate5(key)
	if err != nil {
		return Target{}, fmt.Errorf("%w: %w", ErrNoTarget, err)
	}
	if a.Kind == port.KindOther {
		return Target{}, fmt.Errorf("%w: no family of layout v5 names the key", ErrNoTarget)
	}
	m := string(a.Module)
	if m == "" {
		m = sharedModule
	}
	return Target{Module: m, Kind: a.Kind, ID: a.ID}, nil
}

// IndexTarget is where the Index set lands on layout v5.
func IndexTarget(set string) (Target, error) {
	a, err := port.LocateSet5(set)
	if err != nil {
		return Target{}, fmt.Errorf("%w: %w", ErrNoTarget, err)
	}
	if a.Kind == port.KindIndexOther {
		return Target{}, fmt.Errorf("%w: no set of layout v5 is named like this", ErrNoTarget)
	}
	return Target{Module: string(a.Module), Kind: a.Kind, ID: a.ID}, nil
}

// seg writes one segment of a logical key the way the domain stores do.
func seg(s string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out.WriteByte(c)
		default:
			out.WriteByte('~')
			out.WriteByte(hex[c>>4])
			out.WriteByte(hex[c&15])
		}
	}
	return out.String()
}

// stateRow is one collection of a domain: the logical key of an item, and the
// name of the v4 credential family it carries, if any (with `<ref>` where the
// store puts the random ref).
type stateRow struct {
	domain, kind string
	key          func(it item) (string, error)
	credential   func(it item) string
}

func idKey(prefix string) func(it item) (string, error) {
	return func(it item) (string, error) { return prefix + seg(it.id), nil }
}

func cred(family string) func(it item) string {
	return func(it item) string { return "internal/credentials/" + family + "/" + seg(it.id) + "/<ref>" }
}

// stateRows are the collections of [kinds], in the same order.
var stateRows = []stateRow{
	{domain: DomainGoogle, kind: "workspaces",
		key: func(it item) (string, error) {
			doc, ok := it.val.(workspaceDoc)
			if !ok {
				return "", errors.New("not a workspace")
			}
			backend := doc.Workspace.Backend
			if backend == "" {
				backend = "google"
			}
			return "ws.dir." + seg(backend) + "." + seg(it.id), nil
		},
		credential: func(it item) string {
			backend := "google"
			if doc, ok := it.val.(workspaceDoc); ok && doc.Workspace.Backend != "" {
				backend = doc.Workspace.Backend
			}
			return "internal/credentials/directory/" + seg(backend) + "/" + seg(it.id) + "/<ref>"
		}},

	{domain: "github", kind: "organisations", key: idKey("gh.org."), credential: cred("github-org")},
	{domain: "github", kind: "link-app",
		key:        func(item) (string, error) { return "app.gh.link", nil },
		credential: func(item) string { return "internal/credentials/github-app/link/<ref>" }},
	{domain: "github", kind: "runner-apps",
		key: func(it item) (string, error) {
			tier, org, ok := strings.Cut(it.id, "/")
			if !ok {
				return "", errors.New("a runner App is <tier>/<org>")
			}
			return "app.gh.runner." + seg(tier) + "." + seg(org), nil
		},
		credential: func(it item) string {
			tier, org, _ := strings.Cut(it.id, "/")
			return "internal/credentials/github-runner-app/" + seg(tier) + "/" + seg(org) + "/<ref>"
		}},
	{domain: "github", kind: "catalogue-apps", key: idKey("app.gh.cat."), credential: cred("github-app")},
	{domain: "github", kind: "links", key: idKey("gh.link."), credential: cred("github-link")},
	{domain: "github", kind: "confirmations", key: func(it item) (string, error) { return "gate.github." + seg(it.id) + ".confirm", nil }},
	{domain: "github", kind: "pass-requests", key: func(it item) (string, error) { return "gate.github." + seg(it.id) + ".pass", nil }},

	{domain: "slack", kind: "workspaces", key: idKey("ws.slack."), credential: cred("slack-workspace")},
	{domain: "slack", kind: "catalogue-apps", key: idKey("app.slack.cat."), credential: cred("slack-app")},
	{domain: "slack", kind: "shared-channels", key: idKey("rec.slack.shared.")},
	{domain: "slack", kind: "console-channels",
		key: func(it item) (string, error) {
			ws, name, ok := strings.Cut(it.id, "/")
			if !ok {
				return "", errors.New("a console channel is <workspace>/<name>")
			}
			return "rec.slack.channel." + seg(ws) + "." + seg(name), nil
		}},
	{domain: "slack", kind: "confirmations",
		key: func(it item) (string, error) {
			c, ok := it.val.(slconn.Confirmation)
			if !ok {
				return "", errors.New("not a confirmation")
			}
			key := "gate.slack." + seg(c.Workspace) + ".confirm"
			if c.Channel != "" {
				key += "." + seg(c.Channel)
			}
			return key, nil
		}},
	{domain: "slack", kind: "pass-requests", key: func(it item) (string, error) { return "gate.slack." + seg(it.id) + ".pass", nil }},

	{domain: "console", kind: "session-key",
		key:        func(item) (string, error) { return "rec.console.session-key", nil },
		credential: func(item) string { return "internal/credentials/console/session-key" }},
}

func rowOf(domain, kind string) (stateRow, bool) {
	for _, r := range stateRows {
		if r.domain == domain && r.kind == kind {
			return r, true
		}
	}
	return stateRow{}, false
}

// SecretTarget is where a v4 secret lands on layout v5.
type SecretTarget struct {
	Module string
	Kind   string
	ID     string
	// V5 is the name below the installation's root, or the family when it
	// holds a `<ref>` the store chooses at each write.
	V5 string
	// Config is true for a name an operator or Pulumi seeds as plain text
	// (below internal/config), which the v4 source delivers by name.
	Config bool
}

// MapSecret maps a v4 secret name, below the installation's root (`internal/...`
// or `external/...`), to its v5 address. A name with a `<ref>` last segment is a
// family: its refs are random per write and are not preserved.
func MapSecret(name string) (SecretTarget, error) {
	kindOf, rest, ok := strings.Cut(name, "/")
	if !ok {
		return SecretTarget{}, fmt.Errorf("%w: %q is not below internal/ or external/", ErrNoTarget, name)
	}
	segs := strings.Split(rest, "/")
	switch kindOf {
	case "external":
		return mapExternal(name, segs)
	case "internal":
		return mapInternal(name, segs)
	}
	return SecretTarget{}, fmt.Errorf("%w: %q is not below internal/ or external/", ErrNoTarget, name)
}

// mapExternal: the documents a consumer reads are at the same address on both
// layouts.
func mapExternal(name string, segs []string) (SecretTarget, error) {
	if len(segs) != 2 {
		return SecretTarget{}, fmt.Errorf("%w: %q is not external/<module>/<id>", ErrNoTarget, name)
	}
	m, err := secretstore.ParseModule(segs[0])
	if err != nil || !secretstore.ModuleExternal(m) {
		return SecretTarget{}, fmt.Errorf("%w: %q has no external namespace", ErrNoTarget, segs[0])
	}
	return SecretTarget{Module: segs[0], Kind: "document", ID: segs[1], V5: name}, nil
}

func target(module, kind, id, v5 string) SecretTarget {
	return SecretTarget{Module: module, Kind: kind, ID: id, V5: "internal/" + v5}
}

func notMigrated(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrNotMigrated}, args...)...)
}

func mapInternal(name string, segs []string) (SecretTarget, error) {
	bad := func() (SecretTarget, error) {
		return SecretTarget{}, fmt.Errorf("%w: %q is a name layout v4 never wrote", ErrNoTarget, name)
	}
	n := len(segs)
	switch segs[0] {
	case "config":
		t, err := mapConfig(name, segs[1:])
		t.Config = true
		return t, err
	case "credentials":
		if n < 3 {
			return bad()
		}
		return mapCredential(name, segs[1:])
	case "cloudflare":
		if n == 3 && segs[2] == "minter" {
			return target("cloudflare", "minter", segs[1], "cloudflare/"+segs[1]+"/minter"), nil
		}
	case "cloudflare-minted":
		if n == 2 {
			return target("cloudflare", "minted", segs[1], "cloudflare/minted/"+segs[1]), nil
		}
	}
	return bad()
}

func mapConfig(name string, segs []string) (SecretTarget, error) {
	n := len(segs)
	switch {
	case n == 2 && segs[0] == "issuer" && segs[1] == "state-secret":
		return target("oidc", "state-secret", "", "oidc/state-secret"), nil
	case n == 2 && segs[0] == "recovery" && segs[1] == "password":
		return target("oidc", "recovery-password", "", "oidc/recovery-password"), nil
	case n == 2 && segs[0] == "valkey" && segs[1] == "password":
		return SecretTarget{}, notMigrated("%s is the shared store's password, delivered to the cluster only", name)
	case n == 4 && segs[0] == "providers" && segs[1] == "google" && (segs[3] == "client-id" || segs[3] == "client-secret"):
		return target("oidc", "signin", segs[2]+"/"+segs[3], "oidc/signin/"+segs[2]+"/"+segs[3]), nil
	case n == 3 && segs[0] == "clients" && segs[2] == "secret":
		return target("oidc", "client", segs[1], "oidc/clients/"+segs[1]), nil
	case n == 3 && segs[0] == "directory" && segs[2] == "key":
		return target("google", "workspace-key", segs[1], "google/workspaces/"+segs[1]+"/key"), nil
	}
	return SecretTarget{}, fmt.Errorf("%w: %q is a name the document gives, and layout v5 reads none of its own", ErrNoTarget, name)
}

// mapCredential maps internal/credentials/<kind>/<id...>/<ref>.
func mapCredential(name string, segs []string) (SecretTarget, error) {
	kind, rest := segs[0], segs[1:]
	switch kind {
	case "console":
		if len(rest) == 1 && rest[0] == "session-key" {
			return target("oidc", "console-session-key", "", "oidc/console-session-key"), nil
		}
	case "directory":
		// directory/<provider>/<workspace-id>/<ref>
		if len(rest) == 3 {
			if rest[0] != "google" {
				return SecretTarget{}, fmt.Errorf("%w: the directory backend %q has no module on layout v5", ErrNoTarget, rest[0])
			}
			return target("google", "workspace-key", rest[1], "google/workspaces/"+rest[1]+"/key"), nil
		}
	case "github-org":
		if len(rest) == 2 {
			return SecretTarget{}, notMigrated("%s: on layout v5 an organisation has no key, it names the App whose key it uses (app_ref)", name)
		}
	case "github-app":
		if len(rest) == 2 {
			return target("github", "app", rest[0], "github/apps/"+rest[0]+"/"+rest[1]), nil
		}
	case "github-runner-app":
		if len(rest) == 3 {
			id := appid.RunnerPrefix + rest[0] + "-" + rest[1]
			return target("github", "app", id, "github/apps/"+id+"/"+rest[2]), nil
		}
	case "github-link":
		if len(rest) == 2 {
			return target("github", "link", rest[0], "github/links/"+rest[0]+"/"+rest[1]), nil
		}
	case "slack-workspace":
		if len(rest) == 2 {
			return target("slack", "workspace", rest[0], "slack/workspaces/"+rest[0]+"/"+rest[1]), nil
		}
	case "slack-app":
		if len(rest) == 2 {
			return target("slack", "app", rest[0], "slack/apps/"+rest[0]+"/"+rest[1]), nil
		}
	}
	return SecretTarget{}, fmt.Errorf("%w: %q is a credential layout v4 never wrote", ErrNoTarget, name)
}

// S3Module is the module that owns the blob prefix a static S3 credential
// opens: the Google module's cache, `google/`.
const S3Module = "google"

// MapS3Ref maps the `credentialsRef` of a layout v4 installation,
// `internal/<kind>/<id>`, to the v5 reference `internal/<module>/<name>`: the
// credential moves under the module that owns the blob prefix, and the two
// segments join (`internal/blobs/r2` becomes `internal/google/blobs-r2`). A ref
// that already begins with a module is unchanged.
func MapS3Ref(ref string) (string, error) {
	rest, err := secretstore.CheckInternalRef(ref)
	if err != nil {
		return "", err
	}
	first, second, _ := strings.Cut(rest, "/")
	if _, err = secretstore.ParseModule(first); err == nil {
		return ref, nil
	}
	v5 := "internal/" + S3Module + "/" + first + "-" + second
	if _, err = secretstore.CheckModuleRef(secretstore.Module(S3Module), v5); err != nil {
		return "", err
	}
	return v5, nil
}
