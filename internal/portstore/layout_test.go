package portstore

import (
	"context"
	"testing"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/port/memory"
)

// The Secrets path of every credential kind: `credentials/<kind>/<id>/<ref>`,
// which the ssm adapter keeps at `/sluis/private/credentials/...`. The keys
// are the ones the stores of this package build, so a kind added here without a
// layout name falls under `other` and fails.
func TestEveryCredentialKindHasItsSecretPath(t *testing.T) {
	for _, c := range []struct{ key, ref, want string }{
		{wsDirKey("google", "C01ipl6j0"), "r1", "credentials/directory/google/C01ipl6j0/r1"},
		{wsDirKey("entra", "4b1f"), "r1", "credentials/directory/entra/4b1f/r1"},
		{wsSlackKey("T01"), "r1", "credentials/slack-workspace/T01/r1"},
		{ghOrgKey("acme"), "r1", "credentials/github-org/acme/r1"},
		{ghLinkKey(299386), "r1", "credentials/github-link/299386/r1"},
		{ghLinkAppKey, "r1", "credentials/github-app/link/r1"},
		{ghCatalogueKey("renovate"), "r1", "credentials/github-app/renovate/r1"},
		{runnerKey("stable", "acme"), "r1", "credentials/github-runner-app/stable/acme/r1"},
		{slackCatalogueKey("alerts"), "r1", "credentials/slack-app/alerts/r1"},
		{sessionKeyKey, "", "credentials/console/session-key"},
		// A segment a secret path cannot hold is hex.
		{wsDirKey("google", "x~y"), "r1", "credentials/directory/google/u-787e374579/r1"},
		{wsDirKey("google", "u-1"), "r1", "credentials/directory/google/u-752d31/r1"},
	} {
		if got := secretPath(c.key, c.ref); got != c.want {
			t.Errorf("secretPath(%q, %q) = %q, want %q", c.key, c.ref, got, c.want)
		}
	}
}

// Every record key the stores build has a kind of its own.
func TestEveryRecordKeyHasAKind(t *testing.T) {
	for _, key := range []string{
		wsDirKey("google", "C01"), wsSlackKey("T01"), ghOrgKey("acme"), ghLinkKey(1), ghLinkAppKey,
		ghCatalogueKey("a"), runnerKey("stable", "acme"), slackCatalogueKey("a"),
		slackSharedPfx + "partners", slackChannelPfx + "acme.ops", sessionKeyKey,
		ghConfirmKey("acme"), ghPassKey("acme"), slackConfirmKey("acme", ""), slackConfirmKey("acme", "ops"),
		slackPassKey("acme"), ghClaimPrefix + "42", shareKey("acme", "partners"), userCacheKey("acme", "U1"),
	} {
		if got := secretPath(key, "r"); len(got) >= 19 && got[:19] == "credentials/other/" {
			t.Errorf("the key %q has no kind in the storage layout", key)
		}
	}
}

// The provider segment of a directory workspace is its record's backend; a
// credential saved before its record, which says none, is under the default
// provider, and the record then finds the key it has.
func TestADirectoryWorkspaceIsUnderItsBackendsProvider(t *testing.T) {
	ctx := context.Background()
	set := memory.New().Set()
	set.Secrets = memory.NewSecrets()
	b := New(set)
	ws, creds := NewWorkspaces(b), NewCredentials(b)
	if err := ws.Put(ctx, hub.Workspace{ID: "T1", Backend: "entra"}); err != nil {
		t.Fatal(err)
	}
	if _, err := set.State.Get(ctx, "ws.dir.entra.T1"); err != nil {
		t.Fatalf("a workspace of backend entra is not ws.dir.entra.T1: %v", err)
	}
	if err := creds.Save(ctx, "C01", backend.Credential{Type: "service-account-key", Data: []byte("k")}); err != nil {
		t.Fatal(err)
	}
	if err := ws.Put(ctx, hub.Workspace{ID: "C01", Backend: "google"}); err != nil {
		t.Fatal(err)
	}
	if _, err := set.State.Get(ctx, "ws.dir."+DefaultDirectoryProvider+".C01"); err != nil {
		t.Fatalf("the credential's key was not kept: %v", err)
	}
	if got, err := ws.Get(ctx, "C01"); err != nil || got.Backend != "google" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, ok, err := creds.Load(ctx, "C01"); err != nil || !ok {
		t.Fatalf("Load = %v, %v", ok, err)
	}
}
