//nolint:lll // messages and fixtures are prose and one-line tables
package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/policy"
)

func goodCloudflare() *config.Cloudflare {
	return &config.Cloudflare{
		Accounts: map[string]config.CloudflareAccount{
			"main": {ID: "0123456789abcdef0123456789abcdef", Minter: "internal/cloudflare/main/minter"},
		},
		Presets: map[string]config.CloudflarePreset{
			"dns-example": {
				Account: "main", Prototype: "9d3b1c7a5e2f4b8a9c0d1e2f3a4b5c6d", Description: "DNS edits",
				Lifetime: config.Duration(15 * time.Minute), Rotation: config.Duration(5 * time.Minute),
			},
		},
	}
}

func TestACloudflareSectionIsValidOrOff(t *testing.T) {
	var off *config.Cloudflare
	if err := off.Validate(); err != nil {
		t.Fatalf("an absent section: %v", err)
	}
	if err := goodCloudflare().Validate(); err != nil {
		t.Fatalf("a good section: %v", err)
	}
}

func TestACloudflareSectionIsRefusedWhenItCannotWork(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*config.Cloudflare)
		want string
	}{
		"rotation equal to lifetime": {func(c *config.Cloudflare) {
			p := c.Presets["dns-example"]
			p.Rotation = p.Lifetime
			c.Presets["dns-example"] = p
		}, "shorter than lifetime"},
		"rotation longer than lifetime": {func(c *config.Cloudflare) {
			p := c.Presets["dns-example"]
			p.Rotation = config.Duration(time.Hour)
			c.Presets["dns-example"] = p
		}, "shorter than lifetime"},
		"lifetime under a minute": {func(c *config.Cloudflare) {
			p := c.Presets["dns-example"]
			p.Lifetime, p.Rotation = config.Duration(30*time.Second), config.Duration(10*time.Second)
			c.Presets["dns-example"] = p
		}, "lifetime: 30s is shorter than 1m"},
		"rotation under a minute": {func(c *config.Cloudflare) {
			p := c.Presets["dns-example"]
			p.Rotation = config.Duration(10 * time.Second)
			c.Presets["dns-example"] = p
		}, "rotation: 10s is shorter than 1m"},
		"lifetime over a day": {func(c *config.Cloudflare) {
			p := c.Presets["dns-example"]
			p.Lifetime = config.Duration(48 * time.Hour)
			c.Presets["dns-example"] = p
		}, "longer than 24h"},
		"an undeclared account": {func(c *config.Cloudflare) {
			p := c.Presets["dns-example"]
			p.Account = "other"
			c.Presets["dns-example"] = p
		}, `"other" is not declared`},
		"a minter outside internal/": {func(c *config.Cloudflare) {
			a := c.Accounts["main"]
			a.Minter = "external/cloudflare/main"
			c.Accounts["main"] = a
		}, "internal/<kind>"},
		"a minter that climbs": {func(c *config.Cloudflare) {
			a := c.Accounts["main"]
			a.Minter = "internal/cloudflare/../x/minter"
			c.Accounts["main"] = a
		}, "internal/<kind>"},
		"a preset name that is not a label": {func(c *config.Cloudflare) {
			c.Presets["Dns_Example"] = c.Presets["dns-example"]
		}, "DNS label"},
		"an endpoint with a path": {func(c *config.Cloudflare) {
			p := c.Presets["dns-example"]
			p.Endpoint = "https://acct.r2.cloudflarestorage.com/bucket"
			c.Presets["dns-example"] = p
		}, "endpoint"},
		"an endpoint over http": {func(c *config.Cloudflare) {
			p := c.Presets["dns-example"]
			p.Endpoint = "http://acct.r2.cloudflarestorage.com"
			c.Presets["dns-example"] = p
		}, "endpoint"},
		"two accounts sharing a minter": {func(c *config.Cloudflare) {
			c.Accounts["second"] = config.CloudflareAccount{ID: "fedcba9876543210fedcba9876543210", Minter: "internal/cloudflare/main/minter"}
		}, "already holds"},
	} {
		c := goodCloudflare()
		tc.edit(c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, tc.want)
		}
	}
}

func cloudflarePolicy(t *testing.T, grants ...config.CloudflareGrant) *config.PolicyDocument {
	t.Helper()
	d := config.NewPolicyDocument(policy.Policy{Groups: map[string]policy.Group{"all:infra:dns-editors": {}, "all:infra:release": {}}})
	d.CloudflareGrants = &config.PolicyCloudflare{Grants: grants}
	return d
}

func TestCloudflareGrantsAreHeldToTheirGroupsAndPresets(t *testing.T) {
	good := cloudflarePolicy(t,
		config.CloudflareGrant{Group: "all:infra:dns-editors", Presets: []string{"dns-example"}},
		config.CloudflareGrant{Group: "all:infra:release", Presets: []string{"dns-example"}})
	if err := good.Validate(); err != nil {
		t.Fatalf("good grants: %v", err)
	}
	if err := config.CheckCloudflare(goodCloudflare(), good); err != nil {
		t.Fatalf("good grants against the presets: %v", err)
	}
	for name, tc := range map[string]struct {
		grant config.CloudflareGrant
		want  string
	}{
		"no group":              {config.CloudflareGrant{Presets: []string{"a"}}, "name the group"},
		"an undeclared group":   {config.CloudflareGrant{Group: "nobody", Presets: []string{"a"}}, "not declared"},
		"a row with no presets": {config.CloudflareGrant{Group: "all:infra:dns-editors"}, "grants nothing"},
	} {
		err := cloudflarePolicy(t, tc.grant).Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
	if err := config.CheckCloudflare(goodCloudflare(), cloudflarePolicy(t, config.CloudflareGrant{Group: "all:infra:dns-editors", Presets: []string{"missing"}})); err == nil {
		t.Error("a grant for an undeclared preset was accepted")
	}
}

func TestWhoMayAskForWhat(t *testing.T) {
	p := cloudflarePolicy(t,
		config.CloudflareGrant{Group: "all:infra:dns-editors", Presets: []string{"b", "a"}},
		config.CloudflareGrant{Group: "all:infra:release", Presets: []string{"a"}})
	g := p.Cloudflare()
	if got := g.PresetsForGroups([]string{"x", "all:infra:dns-editors"}); strings.Join(got, ",") != "a,b" {
		t.Errorf("groups: %v", got)
	}
	if !g.Allows("a", []string{"all:infra:release"}) || g.Allows("b", []string{"all:infra:release"}) || g.Allows("a", []string{"x"}) {
		t.Error("Allows disagrees with the rows")
	}
}
