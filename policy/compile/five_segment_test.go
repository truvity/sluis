package compile_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/policy/compile"
)

func TestGroupName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name               string
		scope, thing, role string
		want               string
		valid              bool
	}{
		{"one word", "prod", "shop", "deployer", "prod:shop:deployer", true},
		{"a cluster group", "prod", "k8s", "viewer", "prod:k8s:viewer", true},
		{"three words", "prod", "finttech:landing:ie", "deployer", "prod:finttech:landing:ie:deployer", true},
		{"installation-wide", "all", "shared:arch:internal", "viewer", "all:shared:arch:internal:viewer", true},
		// GroupName only joins; the shape check is ValidThing's, and a name
		// built from a bad thing is not a grant to any reader.
		{"two words", "prod", "finttech:landing", "deployer", "prod:finttech:landing:deployer", false},
		{"four words", "prod", "a:b:c:d", "deployer", "prod:a:b:c:d:deployer", false},
		{"an empty word", "prod", "finttech::ie", "deployer", "prod:finttech::ie:deployer", false},
	} {
		got := compile.GroupName(tc.scope, tc.thing, tc.role)
		if got != tc.want {
			t.Errorf("%s: GroupName = %q, want %q", tc.name, got, tc.want)
		}

		if ok := compile.ValidThing(tc.thing); ok != tc.valid {
			t.Errorf("%s: ValidThing(%q) = %v, want %v", tc.name, tc.thing, ok, tc.valid)
		}

		scope, thing, role, ok := policy.SplitGroup(got)
		if ok != tc.valid {
			t.Errorf("%s: SplitGroup(%q) ok = %v, want %v", tc.name, got, ok, tc.valid)
		}

		if tc.valid && (scope != tc.scope || thing != tc.thing || role != tc.role) {
			t.Errorf("%s: SplitGroup(%q) = (%q, %q, %q), want the parts GroupName was given", tc.name, got, scope, thing, role)
		}
	}
}

func TestProjectThings(t *testing.T) {
	t.Parallel()

	if got := (compile.Project{}).Things("shop"); !slices.Equal(got, []string{"shop"}) {
		t.Errorf("Things without an alias = %v", got)
	}

	if got := (compile.Project{Alias: "shop"}).Things("finttech:shop:ie"); !slices.Equal(got, []string{"finttech:shop:ie", "shop"}) {
		t.Errorf("Things with an alias = %v", got)
	}
}

func TestValidateProjects(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		projects map[string]compile.Project
		wantErrs []string
	}{
		{"one word, no alias", map[string]compile.Project{"shop": {}}, nil},
		{"three words and the old word", map[string]compile.Project{"finttech:landing:ie": {Alias: "landing"}}, nil},
		{"two words", map[string]compile.Project{"finttech:landing": {}}, []string{"the key must be one word"}},
		{"four words", map[string]compile.Project{"a:b:c:d": {}}, []string{"the key must be one word"}},
		{"an empty word", map[string]compile.Project{"finttech::ie": {}}, []string{"the key must be one word"}},
		{"an alias that is the key", map[string]compile.Project{"shop": {Alias: "shop"}}, []string{"is the key itself"}},
		{"an alias of several words", map[string]compile.Project{"a:b:c": {Alias: "x:y"}}, []string{"must be one word"}},
		{
			"an alias that is another project's key",
			map[string]compile.Project{"a:b:c": {Alias: "shop"}, "shop": {}},
			[]string{`alias "shop" is already the thing of project "shop"`},
		},
		{
			"two projects with one alias",
			map[string]compile.Project{"a:b:c": {Alias: "shop"}, "a:d:c": {Alias: "shop"}},
			[]string{`alias "shop" is already the thing of project "a:b:c"`},
		},
	} {
		errs := compile.ValidateProjects("projects", tc.projects)
		if len(errs) != len(tc.wantErrs) {
			t.Errorf("%s: errors = %v, want %d", tc.name, errs, len(tc.wantErrs))

			continue
		}

		for i, want := range tc.wantErrs {
			if !strings.Contains(errs[i].Error(), want) {
				t.Errorf("%s: error %d = %q, want it to contain %q", tc.name, i, errs[i], want)
			}
		}
	}
}

// aliasedModel is exampleModel with the shop moved to the five-segment form and
// its old word kept as the alias. A workload row is its own group, named in one
// form or the other and never doubled, so the shop's is left out.
func aliasedModel(alias string) compile.Model {
	m := exampleModel()

	delete(m.Workloads, "dev:shop:metrics-gate")

	shop := m.Projects["shop"]
	shop.Alias = alias

	delete(m.Projects, "shop")
	m.Projects["acme:shop:uk"] = shop

	return m
}

func TestRosterEmitsBothFormsOfAnAliasedProject(t *testing.T) {
	t.Parallel()

	groups := groupsByName(compile.Roster(aliasedModel("shop"), exampleConventions()))

	for _, role := range []string{"deployer", "approver", "admin", "read", "viewer"} {
		long, short := groups["dev:acme:shop:uk:"+role], groups["dev:shop:"+role]
		if long.Name == "" || short.Name == "" {
			t.Errorf("%s: long %q, old %q: both forms must exist", role, long.Name, short.Name)

			continue
		}

		if !slices.Equal(long.Members, short.Members) || !slices.Equal(long.Emails, short.Emails) || !slices.Equal(long.Rungs, short.Rungs) {
			t.Errorf("%s: %+v and %+v differ; an alias carries the same members", role, long, short)
		}
	}

	if got := groups["dev:acme:shop:uk:deployer"].Members; !slices.Equal(got, []string{"team-shop@example.com"}) {
		t.Errorf("deployers = %v", got)
	}
}

func TestRosterWithoutAnAliasEmitsOneForm(t *testing.T) {
	t.Parallel()

	groups := groupsByName(compile.Roster(aliasedModel(""), exampleConventions()))

	if _, ok := groups["dev:acme:shop:uk:deployer"]; !ok {
		t.Error("the five-segment group is missing")
	}

	for name := range groups {
		if strings.HasPrefix(name, "dev:shop:") {
			t.Errorf("%s: no alias, so no old form", name)
		}
	}
}

func TestRosterAliasLeavesTheRestOfTheModelAlone(t *testing.T) {
	t.Parallel()

	with := groupsByName(compile.Roster(aliasedModel("shop"), exampleConventions()))
	without := groupsByName(compile.Roster(aliasedModel(""), exampleConventions()))

	for name, group := range without {
		got, ok := with[name]
		if !ok || !slices.Equal(got.Members, group.Members) {
			t.Errorf("%s changed when the alias was added: %+v, was %+v", name, got, group)
		}
	}

	for name := range with {
		if _, ok := without[name]; !ok && !strings.HasPrefix(name, "dev:shop:") {
			t.Errorf("%s appeared with the alias and is not an old-form group", name)
		}
	}
}

func TestBuildVocabularyDeclaresBothFormsOfAnAliasedProject(t *testing.T) {
	t.Parallel()

	v, err := compile.BuildVocabulary(aliasedModel("shop"), exampleConventions(), handVocabulary())
	if err != nil {
		t.Fatalf("BuildVocabulary: %v", err)
	}

	long, short := v.Things["acme:shop:uk"], v.Things["shop"]
	if len(long.Roles) == 0 || len(short.Roles) == 0 {
		t.Fatalf("things = %v, want both forms declared", v.Things)
	}

	if !slices.Equal(long.Scopes, short.Scopes) || len(long.Roles) != len(short.Roles) {
		t.Errorf("%+v and %+v differ", long, short)
	}

	for role, spec := range short.Roles {
		if got := long.Roles[role]; !slices.Equal(got.Implies, spec.Implies) || !slices.Equal(got.Scopes, spec.Scopes) {
			t.Errorf("role %s: %+v and %+v differ", role, got, spec)
		}
	}

	without, err := compile.BuildVocabulary(aliasedModel(""), exampleConventions(), handVocabulary())
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := without.Things["shop"]; ok {
		t.Error("no alias, so no old form")
	}
}

// The two halves meet in a policy: a client may require, and carry, either
// form while the alias stands.
func TestAliasedProjectIsRequirableUnderEitherForm(t *testing.T) {
	t.Parallel()

	m := aliasedModel("shop")
	c := exampleConventions()

	v, err := compile.BuildVocabulary(m, c, handVocabulary())
	if err != nil {
		t.Fatal(err)
	}

	roster := compile.Roster(m, c)
	groups := map[string]policy.Group{}

	for _, g := range roster.Groups {
		if strings.HasPrefix(g.Name, "dev:shop:") || strings.HasPrefix(g.Name, "dev:acme:shop:uk:") {
			groups[g.Name] = policy.Group{}
		}
	}

	for _, tc := range []struct {
		name     string
		requires []string
		override policy.GroupsOverride
	}{
		{"new form", []string{"dev:acme:shop:uk:deployer"}, policy.GroupsOverride{}},
		{"old form", []string{"dev:shop:deployer"}, policy.GroupsOverride{}},
		{"new form listed", []string{"dev:shop:deployer"}, policy.GroupsOverride{Things: []string{"acme:shop:uk"}}},
		{"old form listed", []string{"dev:acme:shop:uk:deployer"}, policy.GroupsOverride{Things: []string{"shop"}}},
	} {
		p := policy.Policy{
			Version:    1,
			Vocabulary: v,
			Groups:     groups,
			Clients: map[string]policy.Client{
				"console": {Kind: policy.KindConfidential, Secret: policy.ClientSecret{Name: "console"}, Requires: tc.requires, Groups: tc.override},
			},
		}

		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
