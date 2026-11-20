package tenant_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/policy/compile"
	"github.com/truvity/sluis/policy/tenant"
)

func TestMatchGroupFiveSegments(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pattern, group string
		want           bool
	}{
		// A five-segment pattern against a five-segment group, a segment at a time.
		{"prod:acme:web:ie:deployer", "prod:acme:web:ie:deployer", true},
		{"prod:acme:web:ie:deployer", "prod:acme:web:uk:deployer", false},
		{"prod:acme:web:ie:*", "prod:acme:web:ie:viewer", true},
		{"prod:acme:*:ie:*", "prod:acme:shop:ie:viewer", true},
		{"prod:acme:*:ie:*", "prod:acme:shop:uk:viewer", false},
		{"*:acme:web:ie:deployer", "stage:acme:web:ie:deployer", true},
		{"*:acme:web:ie:deployer", "all:acme:web:ie:deployer", false},
		{"all:shared:arch:internal:viewer", "all:shared:arch:internal:viewer", true},
		// A single `*` thing in a three-segment pattern is any thing.
		{"prod:*:deployer", "prod:acme:web:ie:deployer", true},
		{"prod:*:deployer", "prod:web:deployer", true},
		{"prod:*:deployer", "prod:acme:web:ie:viewer", false},
		{"*:*:*", "all:acme:web:ie:viewer", false},
		// A named one-word thing never matches a three-word one, nor the reverse.
		{"prod:web:deployer", "prod:acme:web:ie:deployer", false},
		{"prod:acme:web:ie:deployer", "prod:web:deployer", false},
		// Neither pattern nor group is anything but three or five segments.
		{"prod:acme:web:deployer", "prod:acme:web:ie:deployer", false},
		{"prod:acme:web:ie:deployer", "prod:acme:web:deployer", false},
		{"prod:acme:web:ie:deployer", "prod:acme:web:ie:extra:deployer", false},
		{"prod:acme:web:ie:extra:deployer", "prod:acme:web:ie:extra:deployer", false},
		{"prod:acme::ie:deployer", "prod:acme:web:ie:deployer", false},
		{"prod:acme:we*:ie:deployer", "prod:acme:web:ie:deployer", false},
		{"prod:acme:web:ie:deployer", "prod:acme::ie:deployer", false},
	} {
		if got := tenant.MatchGroup(tc.pattern, tc.group); got != tc.want {
			t.Errorf("MatchGroup(%q, %q) = %v, want %v", tc.pattern, tc.group, got, tc.want)
		}
	}
}

func TestCeilingValidateFiveSegments(t *testing.T) {
	t.Parallel()

	if err := (tenant.Ceiling{Groups: []string{"prod:acme:*:ie:*", "*:acme:web:ie:deployer", "dev:web:*"}}).Validate(); err != nil {
		t.Fatalf("five-segment patterns were refused: %v", err)
	}

	for _, pattern := range []string{"prod:acme:web:deployer", "a:b:c:d:e:f", "prod:acme::ie:deployer", "prod:acme:we*:ie:deployer"} {
		if err := (tenant.Ceiling{Groups: []string{pattern}}).Validate(); err == nil {
			t.Errorf("%q was accepted", pattern)
		}
	}

	scopes := (tenant.Ceiling{Groups: []string{"prod:acme:web:ie:*", "*:acme:web:ie:*", "dev:web:*"}}).Scopes()
	if len(scopes) != 2 || scopes[0] != "dev" || scopes[1] != "prod" {
		t.Errorf("Scopes() = %v, want dev and prod", scopes)
	}
}

func ceilingFor(groups ...string) tenant.Owner {
	o := acme()
	o.Ceiling.Groups = groups

	return o
}

func projectFragment(rows map[string]compile.Project) tenant.Fragment {
	return tenant.Fragment{Projects: rows}
}

// wantShapeViolation asserts one of the violations is the shape of a project's
// key or alias. The ceiling may refuse the groups such a name derives as well.
func wantShapeViolation(t *testing.T, vs []tenant.Violation, detail string) {
	t.Helper()

	for _, v := range vs {
		if v.Code == tenant.OwnerName && strings.Contains(v.Detail, detail) {
			return
		}
	}

	t.Fatalf("want an owner-name violation containing %q, got %v", detail, vs)
}

func TestCheck_ProjectInTheFiveSegmentForm(t *testing.T) {
	t.Parallel()

	web := compile.Project{Scopes: []string{"dev"}, Deployers: []string{"team-web@acme.example"}}

	t.Run("inside a five-segment ceiling", func(t *testing.T) {
		t.Parallel()

		f := projectFragment(map[string]compile.Project{"acme:web:ie": web})
		if vs := tenant.Check(nil, ceilingFor("dev:acme:web:ie:*"), f); len(vs) != 0 {
			t.Fatalf("refused: %v", vs)
		}
	})

	t.Run("a ceiling written for one word still covers it with a wildcard thing", func(t *testing.T) {
		t.Parallel()

		f := projectFragment(map[string]compile.Project{"acme:web:ie": web})
		if vs := tenant.Check(nil, ceilingFor("dev:*:*"), f); len(vs) != 0 {
			t.Fatalf("refused: %v", vs)
		}
	})

	t.Run("outside the ceiling", func(t *testing.T) {
		t.Parallel()

		f := projectFragment(map[string]compile.Project{"acme:web:ie": web})
		wantOnly(t, tenant.Check(nil, ceilingFor("dev:acme:web:uk:*"), f), tenant.OwnerName, "projects[acme:web:ie]")
	})

	t.Run("the alias is held to the ceiling too", func(t *testing.T) {
		t.Parallel()

		p := web
		p.Alias = "web"

		f := projectFragment(map[string]compile.Project{"acme:web:ie": p})

		if vs := tenant.Check(nil, ceilingFor("dev:acme:web:ie:*", "dev:web:*"), f); len(vs) != 0 {
			t.Fatalf("refused: %v", vs)
		}

		vs := tenant.Check(nil, ceilingFor("dev:acme:web:ie:*"), f)
		if len(vs) != 1 || vs[0].Code != tenant.OwnerName || !strings.Contains(vs[0].Detail, "dev:web:deployer") {
			t.Fatalf("want the old form named as outside the ceiling, got %v", vs)
		}
	})

	t.Run("a key that is not one word or three", func(t *testing.T) {
		t.Parallel()

		f := projectFragment(map[string]compile.Project{"acme:web": web})
		wantShapeViolation(t, tenant.Check(nil, ceilingFor("dev:*:*"), f), "the key must be one word")
	})

	t.Run("an alias that is not one word", func(t *testing.T) {
		t.Parallel()

		p := web
		p.Alias = "acme:web"

		f := projectFragment(map[string]compile.Project{"acme:web:ie": p})
		wantShapeViolation(t, tenant.Check(nil, ceilingFor("dev:*:*"), f), "must be one word")
	})

	t.Run("an alias the installation already holds", func(t *testing.T) {
		t.Parallel()

		p := web
		p.Alias = "web"

		base := &compile.Model{Projects: map[string]compile.Project{"web": {}}}
		f := projectFragment(map[string]compile.Project{"acme:web:ie": p})

		wantOnly(t, tenant.Check(base, ceilingFor("dev:*:*", "dev:*:*:*:*"), f), tenant.OwnerCollision, "projects[web]")
	})
}

func TestDecodeFragmentProjectAlias(t *testing.T) {
	t.Parallel()

	f, err := tenant.DecodeFragment([]byte(`
projects:
  acme:web:ie:
    alias: web
    scopes: [dev]
    deployers: [team-web@acme.example]
`))
	if err != nil {
		t.Fatal(err)
	}

	if got := f.Projects["acme:web:ie"].Alias; got != "web" {
		t.Errorf("alias = %q, want web", got)
	}
}
