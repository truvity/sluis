package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
)

// policyWith is a policy document that binds one organisation and one group,
// followed by the sections a test adds.
func policyWith(t *testing.T, sections string) (*config.PolicyDocument, error) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "policy.yaml")
	head := "apiVersion: " + config.APIVersion("policy") + "\ngroups:\n  all:platform:engineer: {}\n"
	raw := head + "github:\n  example-org:\n    members: [all:platform:engineer]\n" + sections
	if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Load[config.PolicyDocument](file)
}

const renovate = "      - {id: renovate, purpose: catalogue, org: example-org, export: true, labels: {team: platform}, permissions: {contents: read}}\n"

func TestTheListGivesEveryViewAndLabel(t *testing.T) {
	d, err := policyWith(t, `apps:
  github:
    apps:
      - {id: link, purpose: link, labels: {team: platform}, export: true}
      - {purpose: runner, tier: small, labels: {pool: ci, size: s}}
      - {purpose: runner, tier: small, org: example-org, labels: {size: xs}}
      - {purpose: runner, tier: large}
`+renovate+`controllers:
  github: {enabledOrgs: [example-org], appRefs: {example-org: renovate}}
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.RunnerTiers(); !slices.Equal(got, []string{"small", "large"}) {
		t.Errorf("tiers = %v", got)
	}
	if apps := d.GitHubCatalogue().Apps; len(apps) != 1 || apps[0].ID != "renovate" || !apps[0].Export {
		t.Errorf("catalogue = %+v", apps)
	}
	if !d.GitHubAppExported("link") || !d.GitHubAppExported("renovate") || d.GitHubAppExported("other") {
		t.Error("export flags are not read from the list")
	}
	if got := d.GitHubAppLabels("renovate"); got["team"] != "platform" {
		t.Errorf("labels of renovate = %v", got)
	}
	if got := d.RunnerLabels("small", "example-org"); got["pool"] != "ci" || got["size"] != "xs" {
		t.Errorf("an organisation's entry should win over the tier's: %v", got)
	}
	if got := d.RunnerLabels("small", "other-org"); got["size"] != "s" {
		t.Errorf("the tier's entry applies to any organisation: %v", got)
	}
	if d.AppRef("example-org") != "renovate" || d.AppRef("other-org") != "" {
		t.Error("app_ref is not read from controllers.github.appRefs")
	}
}

func TestTheRetiredSectionsAreReadAsTheList(t *testing.T) {
	d, err := policyWith(t, `apps:
  github:
    runnerTiers: [stable]
    catalogue:
      - {id: renovate, org: example-org, export: true, permissions: {contents: read}}
`)
	if err != nil {
		t.Fatal(err)
	}
	apps := d.GitHubApps()
	if len(apps) != 2 || apps[0].Purpose != "runner" || apps[0].Tier != "stable" || apps[1].Purpose != "catalogue" || apps[1].ID != "renovate" {
		t.Fatalf("list = %+v", apps)
	}
	if d.Apps.GitHub.RunnerTiers != nil || d.Apps.GitHub.Catalogue != nil {
		t.Error("the retired sections should be empty once folded")
	}
	out, err := d.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "runnerTiers") || strings.Contains(string(out), "catalogue:") || !strings.Contains(string(out), "purpose: catalogue") {
		t.Errorf("the canonical document should carry the list only:\n%s", out)
	}
}

// ghApps is the apps section with the entries, one per line.
func ghApps(entries ...string) string {
	return "apps:\n  github:\n    apps:\n      - " + strings.Join(entries, "\n      - ") + "\n"
}

const (
	perms      = "permissions: {contents: read}"
	catalogued = "purpose: catalogue, org: example-org, " + perms
)

func TestTheListIsHeldToItsRules(t *testing.T) {
	refs := func(org, ref string) string { return "controllers:\n  github: {appRefs: {" + org + ": " + ref + "}}\n" }
	for name, tc := range map[string]struct{ sections, want string }{
		"no purpose":                      {ghApps("{id: x, org: example-org, " + perms + "}"), "purpose"},
		"a catalogue id runner-":          {ghApps("{id: runner-x, " + catalogued + "}"), "runner-"},
		"a catalogue id link":             {ghApps("{id: link, " + catalogued + "}"), "link"},
		"a link with another id":          {ghApps("{id: other, purpose: link}"), "link"},
		"two links":                       {ghApps("{id: link, purpose: link}", "{id: link, purpose: link}"), "declared twice"},
		"a runner without a tier":         {ghApps("{purpose: runner}"), "tier"},
		"a runner twice":                  {ghApps("{purpose: runner, tier: a}", "{purpose: runner, tier: a}"), "declared twice"},
		"a runner id elsewhere":           {ghApps("{id: runner-b, purpose: runner, tier: a}"), "runner-a"},
		"a runner with a catalogue field": {ghApps("{purpose: runner, tier: a, " + perms + "}"), "runner entry"},
		"a catalogue with a tier":         {ghApps("{id: x, tier: a, " + catalogued + "}"), "tier"},
		"a bad label":                     {ghApps("{purpose: runner, tier: a, labels: {Team: x}}"), "label"},
		"an unknown ref":                  {ghApps("{id: renovate, "+catalogued+"}") + refs("example-org", "nope"), "not a catalogue App"},
		"a ref to another org": {
			ghApps("{id: x, purpose: catalogue, org: other-org, "+perms+"}") + refs("example-org", "x"), "created under other-org"},
		"a ref for an unbound org": {ghApps("{id: renovate, "+catalogued+"}") + refs("nowhere", "renovate"), "does not bind"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := policyWith(t, tc.sections)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
}
