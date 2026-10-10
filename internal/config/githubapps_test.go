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
	raw := "apiVersion: " + config.APIVersion("policy") + "\ngroups:\n  all:platform:engineer: {}\ngithub:\n  example-org:\n    members: [all:platform:engineer]\n" + sections
	if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Load[config.PolicyDocument](file)
}

const renovate = `      - {id: renovate, purpose: catalogue, org: example-org, export: true, labels: {team: platform}, permissions: {contents: read}}
`

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

func TestTheListIsHeldToItsRules(t *testing.T) {
	for name, tc := range map[string]struct{ sections, want string }{
		"no purpose":               {"apps:\n  github:\n    apps:\n      - {id: x, org: example-org, permissions: {contents: read}}\n", "purpose"},
		"a catalogue id runner-":   {"apps:\n  github:\n    apps:\n      - {id: runner-x, purpose: catalogue, org: example-org, permissions: {contents: read}}\n", "runner-"},
		"a catalogue id link":      {"apps:\n  github:\n    apps:\n      - {id: link, purpose: catalogue, org: example-org, permissions: {contents: read}}\n", "link"},
		"a link with another id":   {"apps:\n  github:\n    apps:\n      - {id: other, purpose: link}\n", "link"},
		"two links":                {"apps:\n  github:\n    apps:\n      - {id: link, purpose: link}\n      - {id: link, purpose: link}\n", "declared twice"},
		"a runner without a tier":  {"apps:\n  github:\n    apps:\n      - {purpose: runner}\n", "tier"},
		"a runner twice":           {"apps:\n  github:\n    apps:\n      - {purpose: runner, tier: a}\n      - {purpose: runner, tier: a}\n", "declared twice"},
		"a runner id elsewhere":    {"apps:\n  github:\n    apps:\n      - {id: runner-b, purpose: runner, tier: a}\n", "runner-a"},
		"a runner with a webhook":  {"apps:\n  github:\n    apps:\n      - {purpose: runner, tier: a, permissions: {contents: read}}\n", "runner entry"},
		"a catalogue with a tier":  {"apps:\n  github:\n    apps:\n      - {id: x, purpose: catalogue, tier: a, org: example-org, permissions: {contents: read}}\n", "tier"},
		"a bad label":              {"apps:\n  github:\n    apps:\n      - {purpose: runner, tier: a, labels: {Team: x}}\n", "label"},
		"an unknown ref":           {"apps:\n  github:\n    apps:\n" + renovate + "controllers:\n  github: {appRefs: {example-org: nope}}\n", "not a catalogue App"},
		"a ref to another org":     {"apps:\n  github:\n    apps:\n      - {id: x, purpose: catalogue, org: other-org, permissions: {contents: read}}\ncontrollers:\n  github: {appRefs: {example-org: x}}\n", "created under other-org"},
		"a ref for an unbound org": {"apps:\n  github:\n    apps:\n" + renovate + "controllers:\n  github: {appRefs: {nowhere: renovate}}\n", "does not bind"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := policyWith(t, tc.sections)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
}
