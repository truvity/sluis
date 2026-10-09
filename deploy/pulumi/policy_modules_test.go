package sluispulumi

import (
	"encoding/json"
	"strings"
	"testing"
)

func testModuleEnv() ModuleEnv {
	tables := map[Module]string{}
	for _, m := range Modules() {
		tables[m] = "tbl:" + string(m)
	}
	return ModuleEnv{
		Region: "r1", Account: "000000000000",
		Modules:         ModuleSet{Instance: "i1"},
		BucketArn:       "bkt",
		TableArns:       tables,
		ParameterKeyArn: "pkey",
		TableKeyArn:     "tkey",
		QueueArn:        "queue",
		LogGroupArns:    map[string]string{RoleIssuer: "lg:issuer", RoleCloudflare: "lg:cf", RoleBackup: "lg:bk", RoleRestore: "lg:rs"},
		FunctionArns:    map[string]string{RoleIssuer: "fn:issuer", RoleCloudflare: "fn:cf"},
		Roles:           testRoles(),
	}
}

func testRoles() []Role {
	return []Role{
		{Name: RoleIssuer, Hosts: []Module{ModuleOIDC, ModuleGitHub, ModuleSlack, ModuleGoogle}},
		{Name: RoleCloudflare, Hosts: []Module{ModuleCloudflare}},
		{Name: RoleBackup, Hosts: []Module{ModuleBackup}},
		{Name: RoleRestore, Hosts: []Module{ModuleBackup}},
	}
}

func rendered(t *testing.T, role Role) string {
	t.Helper()
	doc, err := ModuleRolePolicy(testModuleEnv(), role)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestModuleTableNameDefaultAndOverride(t *testing.T) {
	s := ModuleSet{Instance: "kernel", Tables: map[Module]string{ModuleSlack: "custom"}}
	if got := s.TableName(ModuleGoogle); got != "sluis-kernel-google" {
		t.Fatalf("default: %s", got)
	}
	if got := s.TableName(ModuleSlack); got != "custom" {
		t.Fatalf("override: %s", got)
	}
	if err := (ModuleSet{Instance: "k", Tables: map[Module]string{"nope": "x"}}).validate(); err == nil {
		t.Fatal("unknown module accepted")
	}
}

func TestModuleRoleOwnPattern(t *testing.T) {
	doc := rendered(t, Role{Name: RoleCloudflare, Hosts: []Module{ModuleCloudflare}})
	for _, want := range []string{
		"/sluis/i1/internal/cloudflare/*", "/sluis/i1/external/cloudflare/*",
		"tbl:cloudflare", "bkt/cloudflare/*", "queue", "lg:cf:*", "SluisMaintenance", "tbl:backup", "dynamodb:LeadingKeys",
		"PARAMETER_ARN", "ssm.*.amazonaws.com",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q in %s", want, doc)
		}
	}
	for _, not := range []string{"internal/oidc", "tbl:google", "bkt/google", "fn:cf", "GetParametersByPath"} {
		if strings.Contains(doc, not) {
			t.Errorf("unexpected %q in %s", not, doc)
		}
	}
}

func TestModuleRoleIssuerCrossGrants(t *testing.T) {
	doc := rendered(t, testRoles()[0])
	// The issuer hosts google, github and slack here, so they are its own; the
	// minter is invoked, never read.
	for _, want := range []string{"fn:cf", "tbl:oidc", "tbl:google"} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(doc, "tbl:cloudflare") || strings.Contains(doc, "internal/cloudflare") {
		t.Errorf("issuer reaches into the minter's state: %s", doc)
	}
	// Split google out: the issuer then reads its table and invokes its function.
	env := testModuleEnv()
	env.Roles = []Role{
		{Name: RoleIssuer, Hosts: []Module{ModuleOIDC, ModuleGitHub, ModuleSlack}},
		{Name: "google", Hosts: []Module{ModuleGoogle}},
		testRoles()[1],
	}
	env.FunctionArns["google"] = "fn:google"
	d, err := ModuleRolePolicy(env, env.Roles[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "tbl:google") || !strings.Contains(d, "fn:google") {
		t.Errorf("split issuer: %s", d)
	}
	if strings.Contains(d, "internal/google") || strings.Contains(d, "bkt/google") {
		t.Errorf("split issuer reaches google's secrets or blobs: %s", d)
	}
}

func TestModuleRoleBackupAndRestore(t *testing.T) {
	roles := testRoles()
	bk, rs := rendered(t, roles[2]), rendered(t, roles[3])
	for _, m := range Modules() {
		if m == ModuleBackup {
			continue
		}
		if !strings.Contains(bk, "tbl:"+string(m)) || !strings.Contains(rs, "tbl:"+string(m)) {
			t.Errorf("module %s missing", m)
		}
	}
	var parsed struct{ Statement []map[string]any }
	if err := json.Unmarshal([]byte(bk), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, s := range parsed.Statement {
		if s["Sid"] == "SluisCrossOidcTable" && len(s["Action"].([]any)) != len(ddbReadActions) {
			t.Errorf("backup table grant is not read-only: %v", s)
		}
	}
	if !strings.Contains(rs, "s3:PutObject") || !strings.Contains(rs, "kms:Encrypt") {
		t.Error("restore cannot write")
	}
}

func TestModuleRoleSidsUniqueAndDeterministic(t *testing.T) {
	for _, r := range testRoles() {
		st, err := ModuleRoleStatements(testModuleEnv(), r)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, s := range st {
			sid := s["Sid"].(string)
			if seen[sid] {
				t.Errorf("role %s: duplicate Sid %s", r.Name, sid)
			}
			seen[sid] = true
		}
		if rendered(t, r) != rendered(t, r) {
			t.Errorf("role %s: not deterministic", r.Name)
		}
	}
}

func TestModuleRoleRefusals(t *testing.T) {
	env := testModuleEnv()
	if _, err := ModuleRoleStatements(env, Role{Name: "x", Hosts: []Module{"nope"}}); err == nil {
		t.Error("unknown module accepted")
	}
	delete(env.TableArns, ModuleBackup)
	if _, err := ModuleRoleStatements(env, testRoles()[1]); err == nil {
		t.Error("missing maintenance table accepted")
	}
}

func TestRestoreInvokeStatement(t *testing.T) {
	st := RestoreInvokeStatement("fn", []string{"a", "b"})
	if st["Effect"] != "Allow" || st["Resource"] != "fn" {
		t.Fatalf("%v", st)
	}
}
