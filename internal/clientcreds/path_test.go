package clientcreds

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
)

// Every id that is not one plain segment is spelled u-<hex>, and so is every id
// that starts with u-, so an encoded id and a plain one never meet.
func TestPathSpellsAnythingButOnePlainSegmentAsHex(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ id, want string }{
		{"a/b", "credentials/oidc-client/u-612f62/secret"},
		{"/", "credentials/oidc-client/u-2f/secret"},
		{"a/", "credentials/oidc-client/u-612f/secret"},
		{"/a", "credentials/oidc-client/u-2f61/secret"},
		{"a//b", "credentials/oidc-client/u-612f2f62/secret"},
		{"https://x.example/c.json", "credentials/oidc-client/u-68747470733a2f2f782e6578616d706c652f632e6a736f6e/secret"},
		{"", "credentials/oidc-client/u-/secret"},
		{".", "credentials/oidc-client/u-2e/secret"},
		{"..", "credentials/oidc-client/u-2e2e/secret"},
		{"u-", "credentials/oidc-client/u-752d/secret"},
		{"u-abc", "credentials/oidc-client/u-752d616263/secret"},
		{"a b", "credentials/oidc-client/u-612062/secret"},
		{"grafana", "credentials/oidc-client/grafana/secret"},
		{"..a", "credentials/oidc-client/..a/secret"},
	} {
		got := Path(tc.id)
		if got != tc.want {
			t.Errorf("Path(%q) = %q, want %q", tc.id, got, tc.want)
		}
		if err := port.CheckSecretPath(got); err != nil {
			t.Errorf("Path(%q) is not a path the port takes: %v", tc.id, err)
		}
		// One segment under the kind, whatever the id.
		rest := strings.TrimSuffix(strings.TrimPrefix(got, "credentials/oidc-client/"), "/secret")
		if strings.Contains(rest, "/") {
			t.Errorf("Path(%q) = %q has more than one segment for the id", tc.id, got)
		}
	}
}

func TestPathNoTwoIdsShareAPath(t *testing.T) {
	t.Parallel()
	ids := []string{
		"a", "a/b", "a/b/c", "ab", "u-a", "u-612f62", "u-", "", ".", "..", "u-2e", "a b", `a\b`, "a:b",
		"u-752d612f62", "x/secret", "x", "secret", "a/secret", "é", "u-c3a9",
	}
	seen := map[string]string{}
	for _, id := range ids {
		p := Path(id)
		if other, dup := seen[p]; dup {
			t.Errorf("%q and %q share %q", id, other, p)
		}
		seen[p] = id
	}
	// And a path never lies under another id's record, which hierarchical stores
	// cannot hold beside a leaf.
	for p, id := range seen {
		for q, other := range seen {
			if p != q && strings.HasPrefix(q, strings.TrimSuffix(p, "secret")) {
				t.Errorf("%q (%q) lies under %q (%q)", other, q, id, p)
			}
		}
	}
}

// What is made for such an id is found by the resolver, and listed as the same
// client by the orphan look.
func TestAnIdWithASlashIsReconciledAndResolvedAtTheSamePath(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"a/b", "u-ab", "has space", "", "..", "https://x.example/app"} {
		store := memory.NewSecrets()
		res := Reconcile(ctx0, []string{id}, store, nil, t0, quiet(), Hooks{})
		if res.Outcomes[id] != OutcomeCreated {
			t.Errorf("%q: outcome %q", id, res.Outcomes[id])
			continue
		}
		rec := stored(t, store, id)
		got, ok := NewResolver(store, nil, quiet()).Resolve(ctx0, id)
		switch {
		case id == "" || strings.ContainsAny(id, `/\`):
			// The token endpoint never asks for these: such an id cannot be a
			// client of the token endpoint. The record is still made and found.
			if ok {
				t.Errorf("%q resolved", id)
			}
		case !ok || got.Current != rec.Current:
			t.Errorf("%q: resolver %+v, %v; record %q", id, got, ok, rec.Current)
		}
		if again := Reconcile(ctx0, []string{id}, store, nil, t0, quiet(), Hooks{}); again.Outcomes[id] != OutcomeExisting {
			t.Errorf("%q: second pass %q", id, again.Outcomes[id])
		}
		// It is not an orphan when declared, and is when not.
		if got := ReconcileOrphans(ctx0, []string{id}, store, t0, quiet(), Hooks{}); len(got) != 0 {
			t.Errorf("%q declared but reported orphaned: %v", id, got)
		}
		if got := ReconcileOrphans(ctx0, nil, store, t0, quiet(), Hooks{}); len(got) != 1 || got[0] != id {
			t.Errorf("%q undeclared: reported %q", id, got)
		}
	}
}
