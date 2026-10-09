package port_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/truvity/sluis/internal/port"
)

// Every State key of the service, located in layout 5. The first four are
// the module, kind and id; the key is the logical key, which does not change.
// A rule added to layout5.go without a row here fails
// TestEveryLayout5KindIsPinned.
var rows5 = []struct{ key, module, kind, id string }{
	{"ws.dir.google.C01ipl6j0", "google", "workspace", "C01ipl6j0"},
	{"ws.slack.T01", "slack", "workspace", "T01"},
	{"gh.org.acme", "github", "org", "acme"},
	{"gh.link.299386", "github", "link", "299386"},
	{"app.gh.link", "github", "app", "link"},
	{"app.gh.cat.renovate", "github", "app", "renovate"},
	{"app.gh.runner.stable.acme", "github", "app", "runner/stable/acme"},
	{"app.slack.cat.alerts", "slack", "app", "alerts"},
	{"rec.slack.shared.partners", "slack", "shared", "partners"},
	{"rec.slack.channel.acme.ops", "slack", "channel", "acme/ops"},
	{"rec.console.session-key", "oidc", "console", "session-key"},
	{"gate.github-claim.42", "github", "claim", "42"},
	{"gate.github.acme.confirm", "github", "gate", "acme/confirm"},
	{"gate.slack.acme.confirm.ops", "slack", "gate", "acme/confirm/ops"},
	{"gate.other.thing", "", "gate", "other/thing"},
	{"share.acme.partners", "slack", "share", "acme/partners"},
	{"cache.slack.user.acme.U1", "slack", "user-cache", "acme/U1"},
	{"cache.deadbeef.groups", "", "cache", "deadbeef/groups"},
	{"dedupe.abc", "", "dedupe", "abc"},
	{"lease.github-tick:acme", "github", "lease", "github-tick/acme"},
	{"lease.github-links:acme", "github", "lease", "github-links/acme"},
	{"lease.slack-tick:acme", "slack", "lease", "slack-tick/acme"},
	{"lease.cloudflare-tick:main", "cloudflare", "lease", "cloudflare-tick/main"},
	{"lease.refresh:C01ipl6j0", "google", "lease", "refresh/C01ipl6j0"},
	{"lease.export:slack-app.alerts", "slack", "lease", "export/slack-app.alerts"},
	{"lease.export:github-app.x", "github", "lease", "export/github-app.x"},
	{"lease.unknown:thing", "", "lease", "unknown/thing"},
	{"notify.acme", "", "notify", "acme"},
	{"ses.ada.s1", "oidc", "session", "ada/s1"},
	{"sid.s1", "oidc", "session-pointer", "s1"},
	{"req.r1", "oidc", "request", "r1"},
	{"code.c1", "oidc", "code", "c1"},
	{"codesess.r1", "oidc", "code-session", "r1"},
	{"tok.j1", "oidc", "token", "j1"},
	{"sso.s1", "oidc", "sso", "s1"},
	{"rt.h1", "oidc", "session-token", "h1"},
	{"rtrot.h1", "oidc", "session-rotated", "h1"},
	{"keyring.ES384:kid1", "oidc", "keyring", "ES384/kid1"},
	{"issuer:request:5b0f1f0e", "oidc", "request", "5b0f1f0e"},
	{"issuer:code:c1", "oidc", "code", "c1"},
	{"issuer:code-session:r1", "oidc", "code-session", "r1"},
	{"issuer:token:5b0f1f0e", "oidc", "token", "5b0f1f0e"},
	{"issuer:sso:s1", "oidc", "sso", "s1"},
	{"issuer:sso-of:ada@acme.example", "oidc", "sso-of", "ada@acme.example"},
	{"issuer:sso-cookie:h1", "oidc", "sso-cookie", "h1"},
	{"issuer:session:s1", "oidc", "session", "s1"},
	{"issuer:session-token:h1", "oidc", "session-token", "h1"},
	{"issuer:session-rotated:h1", "oidc", "session-rotated", "h1"},
	{"issuer:keyring:entry:ES384:kid1", "oidc", "keyring", "ES384/kid1"},
	{"issuer:keyring:retired:ES384:kid1", "oidc", "keyring-retired", "ES384/kid1"},
	{"issuer:held:ada@acme.example", "oidc", "held", "ada@acme.example"},
	{"issuer:kms:state-secret-fingerprint", "oidc", "guard", "state-secret-fingerprint"},
	{"anything.else", "", port.KindOther, "anything.else"},
}

var setRows5 = []struct{ set, module, kind, id string }{
	{"issuer:keyring:index:ES384", "oidc", "keyring-index", "ES384"},
	{"issuer:sso-clients:s1", "oidc", "sso-clients", "s1"},
	{"issuer:sessions-of:ada@acme.example", "oidc", "sessions-of", "ada@acme.example"},
	{"issuer:sessions-for:console", "oidc", "sessions-for", "console"},
	{"issuer:sessions-for:https://a/b", "oidc", "sessions-for", "https:~2F~2Fa~2Fb"},
	{"issuer:sso", "oidc", "sso-index", "all"},
	{"issuer:sessions", "oidc", "sessions-index", "all"},
	{"something", "", port.KindIndexOther, "something"},
}

// pinnedRules5 is how many State rules layout 5 has. A sweep that finds fewer
// has lost a family (or never ran), so the count is pinned here and moves with
// a deliberate change.
const pinnedRules5, pinnedSetRules5 = 48, 6

func TestEveryKeyHasItsLayout5Address(t *testing.T) {
	if len(rows5) < pinnedRules5 {
		t.Fatalf("the table of this test has %d rows, want at least %d", len(rows5), pinnedRules5)
	}
	for _, r := range rows5 {
		got, err := port.Locate5(r.key)
		if err != nil || string(got.Module) != r.module || got.Kind != r.kind || got.ID != r.id {
			t.Errorf("Locate5(%q) = %v, %v; want %q/%s/%s", r.key, got, err, r.module, r.kind, r.id)
		}
		if got.Shared() != (r.module == "") {
			t.Errorf("Locate5(%q).Shared() = %v", r.key, got.Shared())
		}
	}
	for _, r := range setRows5 {
		got, err := port.LocateSet5(r.set)
		if err != nil || string(got.Module) != r.module || got.Kind != r.kind || got.ID != r.id {
			t.Errorf("LocateSet5(%q) = %v, %v; want %q/%s/%s", r.set, got, err, r.module, r.kind, r.id)
		}
	}
}

// Every family of layout 4 is a family of layout 5, and the other way round:
// the key of each row of the layout 4 table lands in a module or is a
// documented shared family.
func TestEveryLayout4KeyHasAModuleOrIsShared(t *testing.T) {
	if len(stateRows) < pinnedRules5 {
		t.Fatalf("the layout 4 table has %d rows, want at least %d (an empty sweep proves nothing)", len(stateRows), pinnedRules5)
	}
	shared := []string{"gate", "cache", "dedupe", "lease", "notify", port.KindOther}
	for _, r := range stateRows {
		if r.kind == "directory" && r.id[:len("google/")] != "google/" {
			if _, err := port.Locate5(r.key); !errors.Is(err, port.ErrUnsupported) {
				t.Errorf("Locate5(%q) = %v, want ErrUnsupported (no module for the backend)", r.key, err)
			}
			continue
		}
		got, err := port.Locate5(r.key)
		if err != nil {
			t.Errorf("Locate5(%q): %v", r.key, err)
			continue
		}
		if got.Module == "" && !slices.Contains(shared, got.Kind) {
			t.Errorf("Locate5(%q) = %v: a key family with no module", r.key, got)
		}
		if got.Module != "" && !got.Module.Valid() {
			t.Errorf("Locate5(%q) names the module %q, which is not one", r.key, got.Module)
		}
	}
}

func TestEveryLayout5KindIsPinned(t *testing.T) {
	state, sets := port.Kinds5()
	if len(state) == 0 || len(sets) != pinnedSetRules5 {
		t.Fatalf("Kinds5 found %d state kinds and %d sets, want some and %d", len(state), len(sets), pinnedSetRules5)
	}
	var pinned, pinnedSets []string
	for _, r := range rows5 {
		m := r.module
		if m == "" {
			m = "*"
		}
		pinned = append(pinned, m+"/"+r.kind)
	}
	for _, r := range setRows5 {
		pinnedSets = append(pinnedSets, r.module+"/"+r.kind)
	}
	for _, k := range state {
		if !slices.Contains(pinned, k) {
			t.Errorf("the kind %q has a rule and no row in the table of this test", k)
		}
	}
	for _, k := range sets {
		if !slices.Contains(pinnedSets, k) {
			t.Errorf("the index kind %q has a rule and no row in the table of this test", k)
		}
	}
}

// Within a module, two families never share a (kind, id) space unless they
// are the same record in two spellings: the layout 4 kinds that mapped to the
// same layout 4 kind still do.
func TestLayout5DoesNotMergeLayout4Kinds(t *testing.T) {
	merged := map[string]string{} // layout 5 module/kind -> layout 4 kind
	allowed := map[string][]string{
		"github/app":           {"github-app", "github-runner-app"},
		"oidc/session":         {"session", "issuer-session"},
		"oidc/request":         {"issuer-request"},
		"oidc/code":            {"issuer-code"},
		"oidc/code-session":    {"issuer-code-session"},
		"oidc/token":           {"issuer-token"},
		"oidc/sso":             {"issuer-sso"},
		"oidc/session-token":   {"issuer-session-token"},
		"oidc/session-rotated": {"issuer-session-rotated"},
		"oidc/keyring":         {"keyring"},
		"slack/gate":           {"slack-gate"},
		"github/gate":          {"github-gate"},
		"*/gate":               {"gate"},
	}
	for _, r := range stateRows {
		if r.kind == "directory" && r.id[:len("google/")] != "google/" {
			continue
		}
		got, err := port.Locate5(r.key)
		if err != nil {
			continue
		}
		m := string(got.Module)
		if m == "" {
			m = "*"
		}
		k := m + "/" + got.Kind
		if prev, ok := merged[k]; ok && prev != r.kind && !slices.Contains(allowed[k], r.kind) {
			t.Errorf("layout 5 %s merges the layout 4 kinds %s and %s", k, prev, r.kind)
		}
		merged[k] = r.kind
	}
}

func TestALayout5KeyNoModuleCanHoldIsRefused(t *testing.T) {
	for _, key := range []string{"", "ws.dir.", "ws.dir.entra.4b1f", "ws.dir.google.", "ws.dir.google.a/b", "app.gh.cat.link", "app.gh.cat.runner/x", "lease.a:b/c"} {
		if _, err := port.Locate5(key); !errors.Is(err, port.ErrUnsupported) {
			t.Errorf("Locate5(%q) = %v, want ErrUnsupported", key, err)
		}
	}
	if _, err := port.LocateSet5(""); !errors.Is(err, port.ErrUnsupported) {
		t.Errorf("LocateSet5(\"\") = %v, want ErrUnsupported", err)
	}
}

func TestALayout5PrefixIsAQueryOnlyWhenItNamesOneKind(t *testing.T) {
	for _, c := range []struct {
		prefix, module, kind, id string
		ok                       bool
	}{
		{"ws.dir.", "google", "workspace", "", true},
		{"ws.dir.google.", "google", "workspace", "", true},
		{"ws.dir.google.C01", "google", "workspace", "C01", true},
		{"ws.dir.goo", "", "", "", false},
		{"ws.dir.entra.", "", "", "", false},
		{"app.gh.runner.", "github", "app", "runner/", true},
		{"app.gh.runner.stable.", "github", "app", "runner/stable/", true},
		{"rec.slack.channel.acme.", "slack", "channel", "acme/", true},
		{"gate.github.acme.", "github", "gate", "acme/", true},
		{"lease.slack-tick:", "slack", "lease", "slack-tick/", true},
		{"lease.unknown:", "", "lease", "unknown/", true},
		{"lease.slack-ti", "", "", "", false},
		{"lease.export:slack-app.", "slack", "lease", "export/slack-app.", true},
		{"lease.", "", "", "", false},
		{"issuer:code:", "oidc", "code", "", true},
		{"ws.", "", "", "", false},
		{"", "", "", "", false},
		{"gate.github", "", "", "", false},
		{"app.gh.", "", "", "", false},
		{"issuer:", "", "", "", false},
	} {
		module, kind, id, ok := port.LocatePrefix5(c.prefix)
		if ok != c.ok || string(module) != c.module || kind != c.kind || id != c.id {
			t.Errorf("LocatePrefix5(%q) = %q, %q, %q, %v; want %q, %q, %q, %v", c.prefix, module, kind, id, ok, c.module, c.kind, c.id, c.ok)
		}
	}
}

// The prefix of a listing and the address of a key agree: a key under a
// prefix has the prefix's module, kind and an id that starts with its id.
func TestALayout5PrefixAgreesWithItsKeys(t *testing.T) {
	var checked int
	for _, r := range rows5 {
		key := r.key
		for n := 1; n <= len(key); n++ {
			module, kind, id, ok := port.LocatePrefix5(key[:n])
			if !ok {
				continue
			}
			checked++
			got, err := port.Locate5(key)
			if err != nil {
				t.Fatalf("Locate5(%q): %v", key, err)
			}
			if module != got.Module || kind != got.Kind || len(id) > len(got.ID) || got.ID[:len(id)] != id {
				t.Errorf("LocatePrefix5(%q) = %q/%q/%q does not hold %q (%v)", key[:n], module, kind, id, key, got)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no prefix was checked")
	}
}

// Locate5 and Locate see the same keys: the same record lands in one place
// in each, and the sets likewise.
func TestLayout5IsAnotherAddressOfTheSameKey(t *testing.T) {
	for _, r := range rows5 {
		v4, err4 := port.Locate(r.key)
		v5, err5 := port.Locate5(r.key)
		if (err4 == nil) != (err5 == nil) && r.key != "ws.dir.entra.4b1f" {
			t.Errorf("%q: layout 4 %v, layout 5 %v", r.key, err4, err5)
		}
		if err4 == nil && err5 == nil && v4.ID == "" || v5.ID == "" && err5 == nil {
			t.Errorf("%q: an empty id", r.key)
		}
	}
}

func TestModules(t *testing.T) {
	want := []string{"oidc", "github", "slack", "cloudflare", "google", "backup"}
	var got []string
	for _, m := range port.Modules() {
		got = append(got, string(m))
		if p, err := port.ParseModule(string(m)); err != nil || p != m {
			t.Errorf("ParseModule(%q) = %q, %v", m, p, err)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("Modules() = %v, want %v", got, want)
	}
	for _, s := range []string{"", "directory", "console", "OIDC"} {
		if _, err := port.ParseModule(s); !errors.Is(err, port.ErrUnsupported) {
			t.Errorf("ParseModule(%q) = %v, want ErrUnsupported", s, err)
		}
	}
	for _, k := range port.SharedKinds {
		if got, _ := port.Locate5(map[string]string{"lease": "lease.github-tick:a", "notify": "notify.a"}[k]); got.Kind != k {
			t.Errorf("shared kind %q is not a kind of the layout", k)
		}
	}
}
