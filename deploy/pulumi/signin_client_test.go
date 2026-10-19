package sluispulumi

import (
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// The Google module checks the OAuth client's secrets, which the layout keeps in
// the issuer's namespace: its role reads exactly those and nothing else of oidc.
func TestTheGoogleRoleReadsTheSignInClientAndNothingElseOfOIDC(t *testing.T) {
	menv := mxEnv(moduleRoles())
	read := func(role string) string {
		st, err := ModuleRoleStatements(menv, moduleRole(role))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := yaml.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	const signin = "parameter/sluis/" + mxInst + "/internal/oidc/signin/*"
	if g := read(RoleGoogle); !strings.Contains(g, signin) {
		t.Errorf("the google role does not read %s:\n%s", signin, g)
	} else if strings.Contains(g, "internal/oidc/*") || strings.Contains(g, "oidc/state-secret") {
		t.Errorf("the google role reads more of oidc than the sign-in client:\n%s", g)
	}
	for _, r := range []string{RoleGitHub, RoleSlack, RoleCloudflare} {
		if strings.Contains(read(r), "oidc") {
			t.Errorf("the %s role reaches the oidc namespace", r)
		}
	}
}
