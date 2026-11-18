package tenant_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/policy/tenant"
)

func orgOwner() tenant.Owner {
	return tenant.Owner{Name: "acme", Ceiling: tenant.Ceiling{Org: tenant.OrgCeiling{
		PlatformVariables: []string{"CI_RUNNER_LABEL_*", "CI_REMOTE_BUILDERS*"},
		PlatformValued:    []string{"ACCESS_ROSTER_ISSUER"},
		SecretPrefix:      "/acme/sluis/credentials/",
	}}}
}

func goodOrg() tenant.OrgSection {
	return tenant.OrgSection{
		Variables: map[string]tenant.OrgVariable{
			"ACME_REGION":          {Value: "eu-west-1", Visibility: "all"},
			"ACME_HOSTS":           {Value: "a,b", Visibility: "selected", Repositories: []string{"shop", "web"}},
			"ACCESS_ROSTER_ISSUER": {Visibility: "selected", Repositories: []string{"shop"}},
		},
		Secrets: map[string]tenant.OrgSecret{
			"ACME_KEY": {Parameter: "/acme/sluis/credentials/key", Visibility: "private"},
		},
		SecurityConfigurations: map[string]tenant.SecurityConfiguration{
			"baseline": {
				Description: "Scanning on.",
				Settings:    map[string]string{"secret_scanning": "enabled", "enforcement": "unenforced"},
				Attach:      tenant.Attachment{Scope: "all"},
			},
		},
	}
}

func TestCheckOrg_AcceptsASectionInsideItsCeiling(t *testing.T) {
	t.Parallel()

	if vs := tenant.CheckOrg(orgOwner(), goodOrg()); len(vs) != 0 {
		t.Fatalf("a section inside its ceiling was refused: %v", vs)
	}

	if vs := tenant.CheckOrg(orgOwner(), tenant.OrgSection{}); len(vs) != 0 {
		t.Fatalf("an empty section was refused: %v", vs)
	}
}

func TestCheckOrg_Refuses(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		mutate func(*tenant.OrgSection)
		code   string
		path   string
	}{
		"a platform variable by pattern": {
			func(s *tenant.OrgSection) {
				s.Variables["CI_RUNNER_LABEL_DEFAULT"] = tenant.OrgVariable{Value: "x", Visibility: "all"}
			},
			tenant.OwnerOrgPlatform, "org.variables[CI_RUNNER_LABEL_DEFAULT]",
		},
		"a platform variable by a second pattern": {
			func(s *tenant.OrgSection) {
				s.Variables["CI_REMOTE_BUILDERS"] = tenant.OrgVariable{Value: "x", Visibility: "all"}
			},
			tenant.OwnerOrgPlatform, "org.variables[CI_REMOTE_BUILDERS]",
		},
		"a value for a platform-valued name": {
			func(s *tenant.OrgSection) {
				s.Variables["ACCESS_ROSTER_ISSUER"] = tenant.OrgVariable{Value: "https://x", Visibility: "all"}
			},
			tenant.OwnerOrgPlatform, "org.variables[ACCESS_ROSTER_ISSUER]",
		},
		"a variable with no value": {
			func(s *tenant.OrgSection) { s.Variables["ACME_EMPTY"] = tenant.OrgVariable{Visibility: "all"} },
			tenant.OwnerOrg, "org.variables[ACME_EMPTY]",
		},
		"a variable name GitHub refuses": {
			func(s *tenant.OrgSection) {
				s.Variables["GITHUB_TOKEN"] = tenant.OrgVariable{Value: "x", Visibility: "all"}
			},
			tenant.OwnerOrg, "org.variables[GITHUB_TOKEN]",
		},
		"a variable name that starts with a digit": {
			func(s *tenant.OrgSection) { s.Variables["1X"] = tenant.OrgVariable{Value: "x", Visibility: "all"} },
			tenant.OwnerOrg, "org.variables[1X]",
		},
		"an unknown visibility": {
			func(s *tenant.OrgSection) { s.Variables["ACME_V"] = tenant.OrgVariable{Value: "x", Visibility: "some"} },
			tenant.OwnerOrg, "org.variables[ACME_V].visibility",
		},
		"selected with no repository": {
			func(s *tenant.OrgSection) {
				s.Variables["ACME_V"] = tenant.OrgVariable{Value: "x", Visibility: "selected"}
			},
			tenant.OwnerOrg, "org.variables[ACME_V].repositories",
		},
		"repositories with visibility all": {
			func(s *tenant.OrgSection) {
				s.Variables["ACME_V"] = tenant.OrgVariable{Value: "x", Visibility: "all", Repositories: []string{"shop"}}
			},
			tenant.OwnerOrg, "org.variables[ACME_V].repositories",
		},
		"a repository with an owner": {
			func(s *tenant.OrgSection) {
				s.Variables["ACME_V"] = tenant.OrgVariable{Value: "x", Visibility: "selected", Repositories: []string{"acme/shop"}}
			},
			tenant.OwnerOrg, "org.variables[ACME_V].repositories[0]",
		},
		"a secret outside the prefix": {
			func(s *tenant.OrgSection) {
				s.Secrets["ACME_KEY"] = tenant.OrgSecret{Parameter: "/globex/sluis/credentials/key", Visibility: "all"}
			},
			tenant.OwnerOrg, "org.secrets[ACME_KEY].parameter",
		},
		"a secret that climbs out of the prefix": {
			func(s *tenant.OrgSection) {
				s.Secrets["ACME_KEY"] = tenant.OrgSecret{Parameter: "/acme/sluis/credentials/../other", Visibility: "all"}
			},
			tenant.OwnerOrg, "org.secrets[ACME_KEY].parameter",
		},
		"a secret with no parameter": {
			func(s *tenant.OrgSection) { s.Secrets["ACME_KEY"] = tenant.OrgSecret{Visibility: "all"} },
			tenant.OwnerOrg, "org.secrets[ACME_KEY].parameter",
		},
		"a setting GitHub does not have": {
			func(s *tenant.OrgSection) {
				c := s.SecurityConfigurations["baseline"]
				c.Settings = map[string]string{"made_up": "enabled"}
				s.SecurityConfigurations["baseline"] = c
			},
			tenant.OwnerOrg, "org.security_configurations[baseline].settings[made_up]",
		},
		"a setting value GitHub does not have": {
			func(s *tenant.OrgSection) {
				c := s.SecurityConfigurations["baseline"]
				c.Settings = map[string]string{"secret_scanning": "sometimes"}
				s.SecurityConfigurations["baseline"] = c
			},
			tenant.OwnerOrg, "org.security_configurations[baseline].settings[secret_scanning]",
		},
		"a configuration with no description": {
			func(s *tenant.OrgSection) {
				c := s.SecurityConfigurations["baseline"]
				c.Description = ""
				s.SecurityConfigurations["baseline"] = c
			},
			tenant.OwnerOrg, "org.security_configurations[baseline].description",
		},
		"an attachment with no scope": {
			func(s *tenant.OrgSection) {
				c := s.SecurityConfigurations["baseline"]
				c.Attach = tenant.Attachment{}
				s.SecurityConfigurations["baseline"] = c
			},
			tenant.OwnerOrg, "org.security_configurations[baseline].attach.scope",
		},
		"selected attachment with no repository": {
			func(s *tenant.OrgSection) {
				c := s.SecurityConfigurations["baseline"]
				c.Attach = tenant.Attachment{Scope: "selected"}
				s.SecurityConfigurations["baseline"] = c
			},
			tenant.OwnerOrg, "org.security_configurations[baseline].attach.repositories",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			section := goodOrg()
			tc.mutate(&section)

			vs := tenant.CheckOrg(orgOwner(), section)
			if len(vs) == 0 {
				t.Fatal("was accepted")
			}

			if len(vs) != 1 || vs[0].Code != tc.code || vs[0].Path != tc.path {
				t.Fatalf("want one %s at %s, got %v", tc.code, tc.path, vs)
			}
		})
	}
}

func TestCheckOrg_SecretsAreOutOfScopeWithoutAPrefix(t *testing.T) {
	t.Parallel()

	o := orgOwner()
	o.Ceiling.Org.SecretPrefix = ""

	vs := tenant.CheckOrg(o, goodOrg())
	if len(vs) != 1 || vs[0].Code != tenant.OwnerOrg || !strings.Contains(vs[0].Path, "secrets[ACME_KEY]") {
		t.Fatalf("want the secret refused, got %v", vs)
	}
}

func TestCheck_RunsTheOrgSectionAndAZeroCeilingRefusesIt(t *testing.T) {
	t.Parallel()

	f := tenant.Fragment{Org: goodOrg()}

	if vs := tenant.Check(nil, orgOwner(), f); len(vs) != 0 {
		t.Fatalf("a fragment with only an org section was refused: %v", vs)
	}

	f.Org.Variables["CI_RUNNER_LABEL_X"] = tenant.OrgVariable{Value: "x", Visibility: "all"}

	if vs := tenant.Check(nil, orgOwner(), f); len(vs) != 1 || vs[0].Code != tenant.OwnerOrgPlatform {
		t.Fatalf("want owner-org-platform from Check, got %v", vs)
	}

	vs := tenant.Check(nil, tenant.Owner{Name: "acme"}, tenant.Fragment{Org: goodOrg()})
	if len(vs) != 1 || vs[0].Code != tenant.OwnerEmptyCeiling {
		t.Fatalf("the zero ceiling must refuse an org section, got %v", vs)
	}
}

func TestOrgCeiling_Validate(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]tenant.OrgCeiling{
		"a bad pattern":        {PlatformVariables: []string{"[a"}},
		"an empty pattern":     {PlatformVariables: []string{""}},
		"a bad valued name":    {PlatformValued: []string{"not a name"}},
		"a prefix with no end": {SecretPrefix: "/acme/secrets"},
		"a relative prefix":    {SecretPrefix: "acme/credentials/"},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if err := orgOwner().Ceiling.Org.Validate(); err != nil {
		t.Errorf("a good ceiling was refused: %v", err)
	}
}

func TestDecodeFragment_ReadsTheOrgSectionStrictly(t *testing.T) {
	t.Parallel()

	f, err := tenant.DecodeFragment([]byte(`
org:
  variables:
    A_B: {value: x, visibility: all}
  secrets:
    S: {parameter: /acme/sluis/credentials/s, visibility: all}
`))
	if err != nil {
		t.Fatal(err)
	}

	if f.Org.Variables["A_B"].Value != "x" || f.Org.Secrets["S"].Parameter != "/acme/sluis/credentials/s" {
		t.Fatalf("not read: %+v", f.Org)
	}

	if _, err := tenant.DecodeFragment([]byte("org:\n  secrets:\n    S: {value: hunter2, visibility: all}\n")); err == nil {
		t.Fatal("a secret with a value key was accepted")
	}
}
