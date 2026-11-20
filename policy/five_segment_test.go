package policy_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
)

func TestValidThing(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		thing string
		want  bool
	}{
		{"shop", true},
		{"finttech:landing:ie", true},
		{"shared:arch:internal", true},
		{"", false},
		{"finttech:landing", false},
		{"a:b:c:d", false},
		{"a:b:c:d:e", false},
		{":landing:ie", false},
		{"finttech::ie", false},
		{"finttech:landing:", false},
		{"::", false},
		{":", false},
	} {
		if got := policy.ValidThing(tc.thing); got != tc.want {
			t.Errorf("ValidThing(%q) = %v, want %v", tc.thing, got, tc.want)
		}
	}
}

// A thing's name is what a client lists under `groups:`, so the five-segment
// form is the whole middle of the grant, not its first word.
const fiveSegmentVocabulary = `
version: 1
vocabulary:
  scopes:
    prod: {}
    all: {}
  things:
    shop:
      scopes: [prod]
      roles: { deployer: [] }
    truvity:dms:generic:
      scopes: [prod]
      roles: { deployer: [], viewer: [] }
    truvity:wallet:generic:
      scopes: [prod]
      roles: { deployer: [] }
    shared:arch:internal:
      scopes: [all]
      roles: { viewer: [] }
`

func TestScopeGroupsFiveSegmentThings(t *testing.T) {
	t.Parallel()

	held := []string{
		"prod:truvity:dms:generic:deployer",
		"prod:truvity:dms:generic:viewer",
		"prod:truvity:wallet:generic:deployer",
		"all:shared:arch:internal:viewer",
		"prod:shop:deployer",
	}

	for _, tc := range []struct {
		name     string
		requires []string
		groups   policy.GroupsOverride
		kept     []string
	}{
		{
			"requires pairs the whole thing, in any role",
			[]string{"prod:truvity:dms:generic:deployer"},
			policy.GroupsOverride{},
			[]string{"prod:truvity:dms:generic:deployer", "prod:truvity:dms:generic:viewer"},
		},
		{
			"a groups entry is the whole thing, and reaches no sibling project of the vendor",
			[]string{"prod:shop:deployer"},
			policy.GroupsOverride{Things: []string{"truvity:dms:generic"}},
			[]string{"prod:shop:deployer", "prod:truvity:dms:generic:deployer", "prod:truvity:dms:generic:viewer"},
		},
		{
			"an installation-wide five-segment grant",
			[]string{"all:shared:arch:internal:viewer"},
			policy.GroupsOverride{},
			[]string{"all:shared:arch:internal:viewer"},
		},
		{
			"every five-segment thing a list names",
			[]string{"prod:shop:deployer"},
			policy.GroupsOverride{Things: []string{"truvity:dms:generic", "shared:arch:internal"}},
			[]string{
				"all:shared:arch:internal:viewer", "prod:shop:deployer",
				"prod:truvity:dms:generic:deployer", "prod:truvity:dms:generic:viewer",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, err := policy.Parse([]byte(fiveSegmentVocabulary))
			if err != nil {
				t.Fatal(err)
			}

			p.Groups = map[string]policy.Group{}
			for _, name := range held {
				p.Groups[name] = policy.Group{}
			}

			p.Clients = map[string]policy.Client{
				"console": {Kind: policy.KindConfidential, Secret: policy.ClientSecret{Name: "console"}, Requires: tc.requires, Groups: tc.groups},
			}

			if err := p.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}

			kept, _ := p.ScopeGroups("console", held)
			if !slices.Equal(kept, tc.kept) {
				t.Errorf("kept = %v, want %v", kept, tc.kept)
			}
		})
	}
}

func TestGroupsOverrideNamesADeclaredFiveSegmentThing(t *testing.T) {
	t.Parallel()

	p, err := policy.Parse([]byte(fiveSegmentVocabulary))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		entry   string
		wantErr string
	}{
		{"truvity:dms:generic", ""},
		{"truvity:nonesuch:generic", `groups names "truvity:nonesuch:generic", which is not declared under vocabulary.things`},
		// A first word alone is not the thing.
		{"truvity", `groups names "truvity", which is not declared under vocabulary.things`},
		// Two words are an exact two-segment name, as before.
		{"rung:sre", ""},
	} {
		q := p
		q.Groups = map[string]policy.Group{"prod:shop:deployer": {}}
		q.Clients = map[string]policy.Client{
			"console": {
				Kind: policy.KindConfidential, Secret: policy.ClientSecret{Name: "console"},
				Requires: []string{"prod:shop:deployer"}, Groups: policy.GroupsOverride{Things: []string{tc.entry}},
			},
		}

		err := q.Validate()

		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("groups [%s]: %v", tc.entry, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("groups [%s]: err = %v, want %q", tc.entry, err, tc.wantErr)
		}
	}
}

func TestVocabularyFitsAFiveSegmentGrant(t *testing.T) {
	t.Parallel()

	p, err := policy.Parse([]byte(fiveSegmentVocabulary + `
groups:
  prod:truvity:dms:generic:deployer: {}
`))
	if err != nil {
		t.Fatal(err)
	}

	if err := p.Validate(); err != nil {
		t.Fatalf("a declared five-segment grant is refused: %v", err)
	}

	bad, err := policy.Parse([]byte(fiveSegmentVocabulary + `
groups:
  prod:truvity:dms:generic:admin: {}
`))
	if err != nil {
		t.Fatal(err)
	}

	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("err = %v, want an undeclared role of a five-segment thing refused", err)
	}

	if got := p.UnconventionalGroups(); len(got) != 0 {
		t.Errorf("UnconventionalGroups = %v, want a five-segment grant counted as one", got)
	}
}
