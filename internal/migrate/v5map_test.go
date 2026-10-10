package migrate_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/portstore"
)

func TestMapSecretNamesEveryV4ParameterFamily(t *testing.T) {
	for v4, want := range map[string]string{
		"internal/config/issuer/state-secret":                      "internal/oidc/state-secret",
		"internal/config/recovery/password":                        "internal/oidc/recovery-password",
		"internal/config/providers/google/main/client-id":          "internal/oidc/signin/main/client-id",
		"internal/config/providers/google/main/client-secret":      "internal/oidc/signin/main/client-secret",
		"internal/config/clients/local-dev/secret":                 "internal/oidc/clients/local-dev",
		"internal/config/directory/C01/key":                        "internal/google/workspaces/C01/key",
		"internal/credentials/console/session-key":                 "internal/oidc/console-session-key",
		"internal/credentials/directory/google/C01/<ref>":          "internal/google/workspaces/C01/key",
		"internal/credentials/github-app/link/<ref>":               "internal/github/apps/link/<ref>",
		"internal/credentials/github-app/renovate/<ref>":           "internal/github/apps/renovate/<ref>",
		"internal/credentials/github-runner-app/stable/acme/<ref>": "internal/github/apps/runner-stable-acme/<ref>",
		"internal/credentials/github-link/101/<ref>":               "internal/github/links/101/<ref>",
		"internal/credentials/slack-workspace/acme/<ref>":          "internal/slack/workspaces/acme/<ref>",
		"internal/credentials/slack-app/notifier/<ref>":            "internal/slack/apps/notifier/<ref>",
		"internal/cloudflare/prod/minter":                          "internal/cloudflare/prod/minter",
		"internal/cloudflare-minted/r2":                            "internal/cloudflare/minted/r2",
		"external/oidc/local-dev":                                  "external/oidc/local-dev",
		"external/github/runner-stable-acme":                       "external/github/runner-stable-acme",
		"external/slack/notifier":                                  "external/slack/notifier",
		"external/cloudflare/r2":                                   "external/cloudflare/r2",
	} {
		got, err := migrate.MapSecret(v4)
		if err != nil || got.V5 != want {
			t.Errorf("MapSecret(%s) = %q, %v, want %s", v4, got.V5, err, want)
		}
	}
	for v4, want := range map[string]error{
		"internal/config/valkey/password":               migrate.ErrNotMigrated,
		"internal/credentials/github-org/acme/<ref>":    migrate.ErrNotMigrated,
		"internal/credentials/directory/entra/T1/<ref>": migrate.ErrNoTarget,
		"internal/config/anything/else":                 migrate.ErrNoTarget,
		"internal/credentials/unheard-of/x/<ref>":       migrate.ErrNoTarget,
		"external/backup/x":                             migrate.ErrNoTarget,
		"somewhere/else":                                migrate.ErrNoTarget,
	} {
		if _, err := migrate.MapSecret(v4); !errors.Is(err, want) {
			t.Errorf("MapSecret(%s) = %v, want %v", v4, err, want)
		}
	}
}

func TestStateTargetsLocateEveryRecordFamily(t *testing.T) {
	for key, want := range map[string]string{
		"ws.dir.google.C01":                   "google/workspace/C01",
		"gh.org.acme":                         "github/org/acme",
		"app.gh.link":                         "github/app/link",
		"app.gh.cat.renovate":                 "github/app/renovate",
		"app.gh.runner.stable.acme":           "github/app/runner-stable-acme",
		"gh.link.101":                         "github/link/101",
		"gate.github.acme.confirm":            "github/gate/acme/confirm",
		"ws.slack.acme":                       "slack/workspace/acme",
		"app.slack.cat.notifier":              "slack/app/notifier",
		"rec.slack.channel.acme.ops":          "slack/channel/acme/ops",
		"rec.console.session-key":             "oidc/console/session-key",
		"issuer:session-token:abc":            "oidc/session-token/abc",
		"issuer:keyring:entry:ES384:kid":      "oidc/keyring/ES384/kid",
		"issuer:kms:state-secret-fingerprint": "oidc/guard/state-secret-fingerprint",
	} {
		got, err := migrate.StateTarget(key)
		if err != nil || got.String() != want {
			t.Errorf("StateTarget(%s) = %v, %v, want %s", key, got, err, want)
		}
	}
	for _, key := range []string{"ws.dir.entra.T1", "app.gh.cat.link", "app.gh.cat.runner-x", "no.such.family"} {
		if got, err := migrate.StateTarget(key); !errors.Is(err, migrate.ErrNoTarget) {
			t.Errorf("StateTarget(%s) = %v, %v, want a refusal", key, got, err)
		}
	}
}

func TestMapS3RefMovesTheCredentialUnderTheBlobPrefixOwner(t *testing.T) {
	for ref, want := range map[string]string{
		"internal/blobs/r2":        "internal/google/blobs-r2",
		"internal/google/blobs-r2": "internal/google/blobs-r2",
	} {
		if got, err := migrate.MapS3Ref(ref); err != nil || got != want {
			t.Errorf("MapS3Ref(%s) = %q, %v, want %s", ref, got, err, want)
		}
	}
	if _, err := migrate.MapS3Ref("external/blobs/r2"); err == nil {
		t.Error("an external ref was mapped")
	}
}

// The mapping is held to the layout v5 writers: what the destination's own
// stores write for each record is at the address the table promises, and what
// they write reads back as the same record.
func TestTheMappingMatchesWhatTheV5StoresWrite(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	declared := portstore.DeclaredGitHubApps{AppRef: planOptions().AppRef}
	s, err := migrate.OpenDomains(ctx, src.st, false)
	if err != nil {
		t.Fatal(err)
	}
	d, err := migrate.OpenDomainsFor(ctx, dst.st, true, func(id string) bool { return id == "renovate" }, declared)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	workspaces, err := s.Workspaces.List(ctx)
	must(err)
	for _, ws := range workspaces {
		must(d.Workspaces.Put(ctx, ws))
		if cred, found, err := s.Credentials.Load(ctx, ws.ID); err != nil {
			t.Fatal(err)
		} else if found {
			must(d.Credentials.Save(ctx, ws.ID, cred))
		}
	}
	app, found, err := s.Orgs.LinkApp(ctx)
	must(err)
	if linkCred, ok, err := s.Orgs.LinkAppCredential(ctx); err != nil || !found || !ok {
		t.Fatal(err)
	} else {
		must(d.Orgs.PutLinkApp(ctx, app, linkCred))
	}
	runners, err := s.Runner.List(ctx)
	must(err)
	for _, r := range runners {
		key, _, err := s.Runner.PrivateKey(ctx, r.Tier, r.Org)
		must(err)
		must(d.Runner.Put(ctx, r, key))
	}
	apps, err := s.Catalogue.List(ctx)
	must(err)
	for _, a := range apps {
		rec, key, _, err := s.Catalogue.Get(ctx, a.ID)
		must(err)
		must(d.Catalogue.Put(ctx, rec, key))
	}
	orgs, err := s.Orgs.List(ctx)
	must(err)
	for _, o := range orgs {
		cred, _, err := s.Orgs.Credential(ctx, o.Org)
		must(err)
		must(d.Orgs.Put(ctx, o, cred))
	}
	links, err := s.Links.List(ctx)
	must(err)
	for _, l := range links {
		must(d.Links.Restore(ctx, l))
	}
	slackWorkspaces, err := s.Slack.List(ctx)
	must(err)
	for _, w := range slackWorkspaces {
		rec, cred, _, err := s.Slack.Get(ctx, w.Workspace)
		must(err)
		must(d.Slack.Put(ctx, rec, cred))
	}
	slackApps, err := s.SlackApps.List(ctx)
	must(err)
	for _, a := range slackApps {
		rec, creds, _, err := s.SlackApps.Get(ctx, a.ID)
		must(err)
		must(d.SlackApps.Put(ctx, rec, creds))
	}
	key, err := s.SessionKey.Get(ctx, func() ([]byte, error) { return nil, errors.New("none") })
	must(err)
	must(d.SessionKey.Put(ctx, key))

	report, err := migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions())
	if err != nil {
		t.Fatal(err)
	}
	puts := dst.rec.Addresses("put")
	carried := 0
	for _, m := range report.Modules {
		for _, it := range m.Items {
			if it.Concern != "state" || (it.Secrets == 0 && it.Kind != "org") {
				continue
			}
			carried++
			if it.Status != migrate.PlanSame {
				t.Errorf("%s %s/%s = %s after the destination's own stores wrote it: %s", m.Module, it.Kind, it.ID, it.Status, it.Reason)
			}
			if it.ToSecret == "" {
				continue
			}
			prefix := strings.TrimSuffix(it.ToSecret, "<ref>")
			if !slices.ContainsFunc(puts, func(a string) bool { return strings.HasPrefix(a, prefix) }) {
				t.Errorf("%s %s/%s: the table promises %s and the store wrote %v", m.Module, it.Kind, it.ID, it.ToSecret, puts)
			}
		}
	}
	if carried < 8 {
		t.Errorf("only %d carried records were checked", carried)
	}
}

func TestTwoRunnerAppsThatJoinToOneIdAreRefused(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	// The store refuses the second write, so the clash is made in State: the
	// runner App stable/acme, twice, as stable/acme-x and stable-acme/x.
	base, err := src.mem.Get(ctx, "app.gh.runner.stable.acme")
	if err != nil {
		t.Fatal(err)
	}
	for key, swap := range map[string][2]string{
		"app.gh.runner.stable.acme-x": {`"org":"acme-x"`, ""},
		"app.gh.runner.stable-acme.x": {`"tier":"stable-acme"`, `"org":"x"`},
	} {
		raw := string(base.Value)
		if swap[1] == "" {
			raw = strings.Replace(raw, `"org":"acme"`, swap[0], 1)
		} else {
			raw = strings.Replace(strings.Replace(raw, `"tier":"stable"`, swap[0], 1), `"org":"acme"`, swap[1], 1)
		}
		if raw == string(base.Value) {
			t.Fatalf("the record is not as the test expects: %s", raw)
		}
		if _, err = src.mem.Put(ctx, key, []byte(raw), 0); err != nil {
			t.Fatal(err)
		}
	}
	doc, _, err := src.v4.External.GitHubRunnerApp("stable", "acme").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Both are the one exported document, which is what the clash is about.
	if _, err = src.v4.External.GitHubRunnerApp("stable", "acme-x").Put(ctx, doc, ""); err != nil {
		t.Fatal(err)
	}
	report, err := migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions())
	if !errors.Is(err, migrate.ErrRefused) {
		t.Fatalf("Plan = %v, want a refusal", err)
	}
	var refused int
	for _, m := range report.Modules {
		for _, it := range m.Items {
			if it.Status == migrate.PlanRefused && strings.Contains(it.Reason, "join to the id runner-stable-acme-x") {
				refused++
			}
		}
	}
	if refused != 1 {
		t.Errorf("%d runner Apps refused for the joined id, want the second: %s", refused, report.JSON())
	}
}
