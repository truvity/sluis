//nolint:lll // messages and fixtures are prose and one-line tables
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"

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
		"an unknown store": {func(c *config.Cloudflare) {
			p := c.Presets["dns-example"]
			p.Store = "deploy"
			c.Presets["dns-example"] = p
		}, "store"},
		"a rotation on an on-demand preset": {func(c *config.Cloudflare) {
			p := c.Presets["dns-example"]
			p.Store = config.CloudflareStoreNone
			c.Presets["dns-example"] = p
		}, "never rotated"},
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

func TestCloudflareRemoteNamesOneTransportAndHoldsNoPresets(t *testing.T) {
	ok := func(r *config.CloudflareRemote) *config.Cloudflare { return &config.Cloudflare{Remote: r} }
	for name, c := range map[string]*config.Cloudflare{
		"function": ok(&config.CloudflareRemote{Function: "fn"}),
		"url":      ok(&config.CloudflareRemote{URL: "https://cloudflare.example"}),
	} {
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, c := range map[string]*config.Cloudflare{
		"neither":    ok(&config.CloudflareRemote{}),
		"both":       ok(&config.CloudflareRemote{Function: "fn", URL: "https://cloudflare.example"}),
		"bad url":    ok(&config.CloudflareRemote{URL: "cloudflare"}),
		"and preset": {Remote: &config.CloudflareRemote{Function: "fn"}, Presets: map[string]config.CloudflarePreset{"dns": {}}},
	} {
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "cloudflare.remote") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The grants name presets the remote module's document declares.
func TestGrantsAreNotHeldToPresetsOfARemoteMinter(t *testing.T) {
	p := &config.PolicyDocument{CloudflareGrants: &config.PolicyCloudflare{Grants: []config.CloudflareGrant{{Group: "ops", Presets: []string{"dns"}}}}}
	if err := config.CheckCloudflare(&config.Cloudflare{Remote: &config.CloudflareRemote{Function: "fn"}}, p); err != nil {
		t.Fatal(err)
	}
	if err := config.CheckCloudflare(&config.Cloudflare{}, p); err == nil {
		t.Fatal("a grant for an undeclared preset was accepted")
	}
}

func TestCloudflareServeNeedsPresetsAndNotRemote(t *testing.T) {
	preset := map[string]config.CloudflarePreset{"dns": {}}
	for name, c := range map[string]*config.Cloudflare{
		"address": {Serve: &config.CloudflareServe{Address: ":8080", Audience: "cloudflare"}, Presets: preset},
		"lambda":  {Serve: &config.CloudflareServe{}, Presets: preset},
	} {
		if err := c.Validate(); err != nil && strings.Contains(err.Error(), "cloudflare.serve") {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, c := range map[string]*config.Cloudflare{
		"no presets": {Serve: &config.CloudflareServe{Address: ":8080"}},
		"remote":     {Serve: &config.CloudflareServe{Address: ":8080"}, Remote: &config.CloudflareRemote{Function: "fn"}},
		"bad":        {Serve: &config.CloudflareServe{Address: "8080"}, Presets: preset},
	} {
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "cloudflare.serve") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAPresetWithStoreNoneNeedsNoRotation(t *testing.T) {
	c := goodCloudflare()
	p := c.Presets["dns-example"]
	p.Store, p.Rotation = config.CloudflareStoreNone, 0
	c.Presets["dns-example"] = p
	if err := c.Validate(); err != nil {
		t.Fatalf("an on-demand preset without a rotation: %v", err)
	}
	if p.Stored() || c.AnyStored() {
		t.Errorf("store: none reads as stored")
	}
	if !(config.CloudflarePreset{}).Stored() || !goodCloudflare().AnyStored() {
		t.Errorf("the default store is not external")
	}
}

// The schema is the first check: it names the two stores, refuses any other,
// and asks for a rotation only where the schedule rotates.
func TestTheSchemaHoldsStoreToExternalOrNone(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "sluis.full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		store    any
		rotation any
		valid    bool
	}{
		"external with a rotation":    {"external", "5m", true},
		"unset with a rotation":       {nil, "5m", true},
		"none without a rotation":     {"none", nil, true},
		"external without a rotation": {"external", nil, false},
		"unset without a rotation":    {nil, nil, false},
		"none with a rotation":        {"none", "5m", false},
		"an unknown value":            {"deploy", "5m", false},
		"a differently cased value":   {"None", nil, false},
		"an empty value":              {"", "5m", false},
		"a boolean":                   {false, "5m", false},
	} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			preset := doc["cloudflare"].(map[string]any)["presets"].(map[string]any)["dns-example"].(map[string]any)
			delete(preset, "rotation")
			if tc.store != nil {
				preset["store"] = tc.store
			}
			if tc.rotation != nil {
				preset["rotation"] = tc.rotation
			}
			err := config.Validate("sluis", doc)
			if tc.valid && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !tc.valid && err == nil {
				t.Error("accepted")
			}
		})
	}
}
