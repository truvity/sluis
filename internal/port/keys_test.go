package port_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/truvity/sluis/internal/port"
)

// Every kind of the storage layout, one row each (more for a family that has
// several shapes): the logical key, and where the layout puts it. A kind added
// to internal/port/keys.go without a row here fails TestEveryKindIsPinned.
var stateRows = []struct{ key, kind, id string }{
	{"ws.dir.google.C01ipl6j0", "directory", "google/C01ipl6j0"},
	{"ws.dir.entra.4b1f", "directory", "entra/4b1f"},
	{"ws.slack.T01", "slack-workspace", "T01"},
	{"gh.org.acme", "github-org", "acme"},
	{"gh.link.299386", "github-link", "299386"},
	{"app.gh.link", "github-app", "link"},
	{"app.gh.cat.renovate", "github-app", "renovate"},
	{"app.gh.runner.stable.acme", "github-runner-app", "stable/acme"},
	{"app.slack.cat.alerts", "slack-app", "alerts"},
	{"rec.slack.shared.partners", "slack-shared", "partners"},
	{"rec.slack.channel.acme.ops", "slack-channel", "acme/ops"},
	{"rec.console.session-key", "console", "session-key"},
	{"gate.github-claim.42", "github-claim", "42"},
	{"gate.github.acme.confirm", "github-gate", "acme/confirm"},
	{"gate.github.acme.pass", "github-gate", "acme/pass"},
	{"gate.slack.acme.confirm", "slack-gate", "acme/confirm"},
	{"gate.slack.acme.confirm.ops", "slack-gate", "acme/confirm/ops"},
	{"gate.slack.acme.pass", "slack-gate", "acme/pass"},
	{"gate.other.thing", "gate", "other/thing"},
	{"share.acme.partners", "slack-share", "acme/partners"},
	{"cache.slack.user.acme.U1", "slack-user-cache", "acme/U1"},
	{"cache.deadbeef.groups", "cache", "deadbeef/groups"},
	{"dedupe.abc", "dedupe", "abc"},
	{"lease.github-tick:acme", "lease", "github-tick/acme"},
	{"lease.github-links:acme", "lease", "github-links/acme"},
	{"lease.slack-tick:acme", "lease", "slack-tick/acme"},
	{"lease.refresh:C01ipl6j0", "lease", "refresh/C01ipl6j0"},
	{"lease.export:slack-app.alerts", "lease", "export/slack-app.alerts"},
	{"notify.acme", "notify", "acme"},
	{"rec.maintenance", "maintenance", "flag"},
	{"rec.backup.run.20261010T020000Z-a1b2c3", "backup-run", "20261010T020000Z-a1b2c3"},
	{"rec.backup.retention", "backup-retention", "last"},
	{"lease.backup:run", "lease", "backup/run"},
	{"ses.ada.s1", "session", "ada/s1"},
	{"sid.s1", "session-pointer", "s1"},
	{"req.r1", "issuer-request", "r1"},
	{"code.c1", "issuer-code", "c1"},
	{"codesess.r1", "issuer-code-session", "r1"},
	{"tok.j1", "issuer-token", "j1"},
	{"sso.s1", "issuer-sso", "s1"},
	{"rt.h1", "issuer-session-token", "h1"},
	{"rtrot.h1", "issuer-session-rotated", "h1"},
	{"keyring.ES384:kid1", "keyring", "ES384/kid1"},
	{"issuer:request:5b0f1f0e", "issuer-request", "5b0f1f0e"},
	{"issuer:code:c1", "issuer-code", "c1"},
	{"issuer:code-session:r1", "issuer-code-session", "r1"},
	{"issuer:token:5b0f1f0e", "issuer-token", "5b0f1f0e"},
	{"issuer:sso:s1", "issuer-sso", "s1"},
	{"issuer:sso-of:ada@acme.example", "issuer-sso-of", "ada@acme.example"},
	{"issuer:sso-cookie:h1", "issuer-sso-cookie", "h1"},
	{"issuer:session:s1", "issuer-session", "s1"},
	{"issuer:session-token:h1", "issuer-session-token", "h1"},
	{"issuer:session-rotated:h1", "issuer-session-rotated", "h1"},
	{"issuer:keyring:entry:ES384:kid1", "keyring", "ES384/kid1"},
	{"issuer:keyring:retired:ES384:kid1", "keyring-retired", "ES384/kid1"},
	{"issuer:held:ada@acme.example", "issuer-held", "ada@acme.example"},
	{"issuer:kms:state-secret-fingerprint", "issuer-guard", "state-secret-fingerprint"},
	{"anything.else", port.KindOther, "anything.else"},
}

var setRows = []struct{ set, kind, id string }{
	{"issuer:keyring:index:ES384", "keyring-index", "ES384"},
	{"issuer:sso-clients:s1", "sso-clients", "s1"},
	{"issuer:sessions-of:ada@acme.example", "sessions-of", "ada@acme.example"},
	{"issuer:sessions-for:console", "sessions-for", "console"},
	{"issuer:sessions-for:https://a/b", "sessions-for", "https:~2F~2Fa~2Fb"},
	{"issuer:sso", "sso-index", "all"},
	{"issuer:sessions", "sessions-index", "all"},
	{"something", port.KindIndexOther, "something"},
}

func TestEveryKeyHasItsLayoutAddress(t *testing.T) {
	for _, r := range stateRows {
		got, err := port.Locate(r.key)
		if err != nil || got.Kind != r.kind || got.ID != r.id {
			t.Errorf("Locate(%q) = %v, %v; want %s/%s", r.key, got, err, r.kind, r.id)
		}
	}
	for _, r := range setRows {
		got, err := port.LocateSet(r.set)
		if err != nil || got.Kind != r.kind || got.ID != r.id {
			t.Errorf("LocateSet(%q) = %v, %v; want %s/%s", r.set, got, err, r.kind, r.id)
		}
	}
}

func TestEveryKindIsPinned(t *testing.T) {
	state, sets := port.Kinds()
	var pinned, pinnedSets []string
	for _, r := range stateRows {
		pinned = append(pinned, r.kind)
	}
	for _, r := range setRows {
		pinnedSets = append(pinnedSets, r.kind)
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

func TestAnAmbiguousKeyIsRefused(t *testing.T) {
	for _, key := range []string{"", "ws.dir.", "ws.dir.a/b", "app.gh.cat.link", "lease.a:b/c"} {
		if _, err := port.Locate(key); !errors.Is(err, port.ErrUnsupported) {
			t.Errorf("Locate(%q) = %v, want ErrUnsupported", key, err)
		}
	}
}

func TestAPrefixIsAQueryOnlyWhenItNamesOneKind(t *testing.T) {
	for _, c := range []struct {
		prefix, kind, id string
		ok               bool
	}{
		{"ws.dir.", "directory", "", true},
		{"ws.dir.google.", "directory", "google/", true},
		{"ws.dir.google.C01", "directory", "google/C01", true},
		{"rec.slack.channel.acme.", "slack-channel", "acme/", true},
		{"gate.github.acme.", "github-gate", "acme/", true},
		{"gate.slack.acme.confirm.", "slack-gate", "acme/confirm/", true},
		{"lease.slack-tick:", "lease", "slack-tick/", true},
		{"issuer:code:", "issuer-code", "", true},
		{"ws.", "", "", false},
		{"", "", "", false},
		{"gate.github", "", "", false},
		{"gate.", "", "", false},
		{"cache.slack.", "", "", false},
		{"app.gh.", "", "", false},
		{"issuer:", "", "", false},
		{"issuer:keyring:", "", "", false},
	} {
		kind, id, ok := port.LocatePrefix(c.prefix)
		if ok != c.ok || kind != c.kind || id != c.id {
			t.Errorf("LocatePrefix(%q) = %q, %q, %v; want %q, %q, %v", c.prefix, kind, id, ok, c.kind, c.id, c.ok)
		}
	}
}

// The browser sign-in's pointer has a partition of its own, apart from the
// record it names, so a table can tell the two apart.
func TestTheSSOCookiePointerHasItsOwnAddress(t *testing.T) {
	got, err := port.Locate("issuer:sso-cookie:x")
	if err != nil || got.String() != "issuer-sso-cookie/x" {
		t.Errorf("Locate(issuer:sso-cookie:x) = %v, %v; want issuer-sso-cookie/x", got, err)
	}

	if rec, err := port.Locate("issuer:sso:x"); err != nil || rec.Kind == got.Kind {
		t.Errorf("the record and its pointer share a kind: %v, %v", rec, err)
	}
}
