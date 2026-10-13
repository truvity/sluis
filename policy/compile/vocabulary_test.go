package compile_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/policy/compile"
)

func handVocabulary() *policy.Vocabulary {
	return &policy.Vocabulary{
		Scopes: map[string]policy.ScopeSpec{
			"all": {}, "ops": {}, "dev": {}, "acme": {}, "main": {},
		},
		Things: map[string]policy.ThingSpec{
			"k8s":   {Scopes: []string{"ops", "dev"}, Roles: map[string]policy.RoleSpec{"admin": {}, "viewer": {}, "auditor": {}}},
			"sluis": {Scopes: []string{"all"}, Roles: map[string]policy.RoleSpec{"viewer": {}}},
		},
	}
}

func TestBuildVocabularyGeneratesEveryFamily(t *testing.T) {
	m := exampleModel()

	extra := compile.Family{Name: "a generated CI tenant", Things: map[string]policy.ThingSpec{
		"ci-acme": {Scopes: []string{"dev"}, Roles: map[string]policy.RoleSpec{"job": {}}},
	}}

	v, err := compile.BuildVocabulary(m, exampleConventions(), handVocabulary(), extra)
	if err != nil {
		t.Fatalf("BuildVocabulary: %v", err)
	}

	shop, ok := v.Things["shop"]
	if !ok {
		t.Fatalf("no thing for the project: %v", v.Things)
	}

	if !slices.Equal(shop.Scopes, []string{"dev"}) {
		t.Errorf("shop scopes = %v", shop.Scopes)
	}

	for _, role := range []string{compile.ProjectDeployer, compile.ProjectApprover, "admin", "read", compile.ProjectViewer, "metrics-gate"} {
		if _, ok := shop.Roles[role]; !ok {
			t.Errorf("shop has no %s role: %v", role, shop.Roles)
		}
	}

	if _, ok := shop.Roles["observer"]; ok {
		t.Error("shop declares observer, which no rung holds on its scopes")
	}

	// The ledger's viewer is on its secret scope only, so no role covering
	// more ground may imply it.
	ledger := v.Things["ledger"]
	if !slices.Equal(ledger.Roles[compile.ProjectViewer].Scopes, []string{"ops"}) ||
		slices.Contains(ledger.Roles[compile.ProjectDeployer].Implies, compile.ProjectViewer) {
		t.Errorf("ledger = %+v", ledger)
	}

	// The viewer covers every scope of the thing, so each implying role gets
	// the edge.
	for _, role := range []string{compile.ProjectDeployer, compile.ProjectApprover, "admin"} {
		if !slices.Contains(shop.Roles[role].Implies, compile.ProjectViewer) {
			t.Errorf("%s does not imply viewer: %+v", role, shop.Roles[role])
		}
	}

	for name, wantScopes := range map[string][]string{
		"repo-shop":  {"acme"},
		"org":        {"acme"},
		"team-shop":  {"acme"},
		"slack-shop": {"main"},
		"ci-acme":    {"dev"},
	} {
		thing, ok := v.Things[name]
		if !ok || !slices.Equal(thing.Scopes, wantScopes) {
			t.Errorf("thing %s = %+v, want scopes %v", name, thing, wantScopes)
		}
	}
}

func TestBuildVocabularyRefusesACollision(t *testing.T) {
	m := exampleModel()

	hand := handVocabulary()
	hand.Things["shop"] = policy.ThingSpec{Scopes: []string{"dev"}, Roles: map[string]policy.RoleSpec{"owner": {}}}

	_, err := compile.BuildVocabulary(m, exampleConventions(), hand)
	if err == nil || !strings.Contains(err.Error(), compile.FamilyHandWritten) || !strings.Contains(err.Error(), compile.FamilyProjects) {
		t.Fatalf("err = %v, want a collision naming both families", err)
	}
}

func TestBuildVocabularyRefusesAnUndeclaredScope(t *testing.T) {
	m := exampleModel()

	_, err := compile.BuildVocabulary(m, exampleConventions(), &policy.Vocabulary{
		Scopes: map[string]policy.ScopeSpec{"dev": {}},
		Things: map[string]policy.ThingSpec{},
	})
	if err == nil || !strings.Contains(err.Error(), "do not declare") {
		t.Fatalf("err = %v, want an undeclared scope refused", err)
	}
}

func TestBuildVocabularyIsOptIn(t *testing.T) {
	v, err := compile.BuildVocabulary(exampleModel(), exampleConventions(), nil)
	if v != nil || err != nil {
		t.Fatalf("BuildVocabulary(nil) = %v, %v; want nil, nil", v, err)
	}
}

func TestValidateHandWrittenVocabulary(t *testing.T) {
	if err := compile.ValidateHandWrittenVocabulary(handVocabulary()); err != nil {
		t.Fatalf("valid vocabulary refused: %v", err)
	}

	cases := map[string]func(v *policy.Vocabulary){
		"declares no scopes": func(v *policy.Vocabulary) {
			v.Things["x"] = policy.ThingSpec{Roles: map[string]policy.RoleSpec{"a": {}}}
		},
		"declares no roles": func(v *policy.Vocabulary) { v.Things["x"] = policy.ThingSpec{Scopes: []string{"dev"}} },
		"which scopes does not declare": func(v *policy.Vocabulary) {
			v.Things["x"] = policy.ThingSpec{Scopes: []string{"nowhere"}, Roles: map[string]policy.RoleSpec{"a": {}}}
		},
		"not among its own scopes": func(v *policy.Vocabulary) {
			v.Things["x"] = policy.ThingSpec{Scopes: []string{"dev"}, Roles: map[string]policy.RoleSpec{"a": {Scopes: []string{"ops"}}}}
		},
		"not a declared role": func(v *policy.Vocabulary) {
			v.Things["x"] = policy.ThingSpec{Scopes: []string{"dev"}, Roles: map[string]policy.RoleSpec{"a": {Implies: []string{"b"}}}}
		},
	}

	for want, mutate := range cases {
		v := handVocabulary()
		mutate(v)

		if err := compile.ValidateHandWrittenVocabulary(v); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want one saying %q", err, want)
		}
	}
}
