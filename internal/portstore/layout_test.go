package portstore

import "testing"

// The Secrets path of every credential kind: `credentials/<kind>/<id>/<ref>`,
// which the ssm adapter keeps at `/sluis/private/credentials/...`. The keys
// are the ones the stores of this package build, so a kind added here without a
// layout name falls under `other` and fails.
func TestEveryCredentialKindHasItsSecretPath(t *testing.T) {
	for _, c := range []struct{ key, ref, want string }{
		{wsDirKey("C01ipl6j0"), "r1", "credentials/workspace/C01ipl6j0/r1"},
		{wsSlackKey("T01"), "r1", "credentials/slack-workspace/T01/r1"},
		{ghOrgKey("opwerm"), "r1", "credentials/github-org/opwerm/r1"},
		{ghLinkKey(299386), "r1", "credentials/github-link/299386/r1"},
		{ghLinkAppKey, "r1", "credentials/github-app/link/r1"},
		{ghCatalogueKey("renovate"), "r1", "credentials/github-app/renovate/r1"},
		{runnerKey("stable", "opwerm"), "r1", "credentials/github-runner-app/stable/opwerm/r1"},
		{slackCatalogueKey("alerts"), "r1", "credentials/slack-app/alerts/r1"},
		{sessionKeyKey, "", "credentials/console/session-key"},
		// A segment a secret path cannot hold is hex.
		{wsDirKey("x~y"), "r1", "credentials/workspace/u-787e374579/r1"},
		{wsDirKey("u-1"), "r1", "credentials/workspace/u-752d31/r1"},
	} {
		if got := secretPath(c.key, c.ref); got != c.want {
			t.Errorf("secretPath(%q, %q) = %q, want %q", c.key, c.ref, got, c.want)
		}
	}
}

// Every record key the stores build has a kind of its own.
func TestEveryRecordKeyHasAKind(t *testing.T) {
	for _, key := range []string{
		wsDirKey("C01"), wsSlackKey("T01"), ghOrgKey("opwerm"), ghLinkKey(1), ghLinkAppKey,
		ghCatalogueKey("a"), runnerKey("stable", "opwerm"), slackCatalogueKey("a"),
		slackSharedPfx + "partners", slackChannelPfx + "acme.ops", sessionKeyKey,
		ghConfirmKey("opwerm"), ghPassKey("opwerm"), slackConfirmKey("acme", ""), slackConfirmKey("acme", "ops"),
		slackPassKey("acme"), ghClaimPrefix + "42", shareKey("acme", "partners"), userCacheKey("acme", "U1"),
	} {
		if got := secretPath(key, "r"); len(got) >= 19 && got[:19] == "credentials/other/" {
			t.Errorf("the key %q has no kind in the storage layout", key)
		}
	}
}
