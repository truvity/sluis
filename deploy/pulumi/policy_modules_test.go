package sluispulumi

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func testModuleEnv() ModuleEnv {
	tables := map[Module]string{}
	for _, m := range Modules() {
		tables[m] = "tbl:" + string(m)
	}
	logs, fns := map[string]string{}, map[string]string{}
	for _, r := range testRoles() {
		logs[r.Name], fns[r.Name] = "lg:"+r.Name, "fn:"+r.Name
	}
	return ModuleEnv{
		Region: "r1", Account: "111122223333",
		Modules:         ModuleSet{Instance: "i1"},
		BucketArn:       "bkt",
		TableArns:       tables,
		ParameterKeyArn: "pkey",
		TableKeyArn:     "tkey",
		QueueArn:        "queue",
		LogGroupArns:    logs,
		FunctionArns:    fns,
		Roles:           testRoles(),
	}
}

func testRoles() []Role {
	return append(moduleRoles(),
		Role{Name: RoleBackup, Hosts: []Module{ModuleBackup}},
		Role{Name: RoleRestore, Hosts: []Module{ModuleBackup}},
	)
}

// testRole is the role of the name among testRoles.
func testRole(name string) Role {
	for _, r := range testRoles() {
		if r.Name == name {
			return r
		}
	}
	panic("no test role " + name)
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
		"tbl:cloudflare", "bkt/cloudflare/*", "queue", "lg:cloudflare:*", sidMaintenanceDeny, "dynamodb:LeadingKeys",
		"PARAMETER_ARN", "ssm.*.amazonaws.com",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q in %s", want, doc)
		}
	}
	// The flag is in the module's own table: no grant on the backup table, or
	// on any other, reaches it.
	for _, not := range []string{"internal/oidc", "tbl:google", "tbl:backup", "bkt/google", "fn:cf"} {
		if strings.Contains(doc, not) {
			t.Errorf("unexpected %q in %s", not, doc)
		}
	}
}

func TestModuleRoleIssuerCrossGrants(t *testing.T) {
	doc := rendered(t, testRole(RoleIssuer))
	// The issuer hosts oidc alone: it reads the Google snapshot (table by key,
	// blobs) and no other provider's table, invokes github, google and cloudflare
	// as the class issuer, and the console's functions as the class console. No
	// provider parameter, minter reference or provider table is reached.
	for _, want := range []string{"tbl:oidc", "tbl:google",
		"fn:cloudflare:live-issuer", "fn:github:live-issuer", "fn:google:live-issuer",
		"fn:cloudflare:live-console", "fn:github:live-console", "fn:google:live-console", "fn:slack:live-console",
		"fn:backup:live-console", "bkt/google/*"} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, not := range []string{"tbl:cloudflare", "tbl:github", "tbl:slack", "fn:slack:live-issuer", "SluisCrossCloudflareMinter",
		"SluisCrossGithub", "SluisCrossSlack", "SluisCrossGoogleParameters", "internal/cloudflare", "internal/google", "internal/github", "internal/slack",
		"external/google", "bkt/github", "bkt/slack", "fn:issuer"} {
		if strings.Contains(doc, not) {
			t.Errorf("issuer reaches %q: %s", not, doc)
		}
	}
	// The issuer reads the providers' tables and writes none of them.
	st, err := ModuleRoleStatements(testModuleEnv(), testRole(RoleIssuer))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st {
		sid := s["Sid"].(string)
		if strings.HasPrefix(sid, "SluisCross") && strings.HasSuffix(sid, "Table") && !slices.Equal(s["Action"].([]string), ddbSnapshotActions) {
			t.Errorf("%s is more than a read by key: %v", sid, s)
		}
	}
	// A function without an ARN is an error, never a grant on nothing.
	env := testModuleEnv()
	delete(env.FunctionArns, RoleSlack)
	if _, err := ModuleRoleStatements(env, testRole(RoleIssuer)); err == nil {
		t.Error("an invoke grant on a function with no ARN was rendered")
	}
}

// The github function serves the mint too (D-v176-7 a), and the mint reads the
// App keys under the github module's own parameters: the role has nothing
// beyond hosting github, no other module, and invokes no peer.
func TestModuleRoleGitHubServesTheMintWithNothingBeyondItsOwnModule(t *testing.T) {
	doc := rendered(t, testRole(RoleGitHub))
	for _, want := range []string{"tbl:github", "internal/github", "bkt/github/*", "lg:github:*"} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, not := range []string{"tbl:oidc", "tbl:slack", "tbl:google", "internal/slack", "internal/google", "github-mint"} {
		if strings.Contains(doc, not) {
			t.Errorf("the github function reaches %q: %s", not, doc)
		}
	}
	st, err := ModuleRoleStatements(testModuleEnv(), testRole(RoleGitHub))
	if err != nil {
		t.Fatal(err)
	}
	// Its own function on the console alias, for a run-now, and nothing else.
	for _, s := range st {
		for _, a := range actionsOf(s["Action"]) {
			if a == "lambda:InvokeFunction" && s["Sid"] != sidInvokeOwnController {
				t.Errorf("the github function invokes a peer: %v", s["Sid"])
			}
		}
	}
}

func actionsOf(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []string:
		return x
	}
	return nil
}

func TestModuleRoleBackupAndRestore(t *testing.T) {
	bk, rs := rendered(t, testRole(RoleBackup)), rendered(t, testRole(RoleRestore))
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
		first, second := rendered(t, r), rendered(t, r)
		if first != second {
			t.Errorf("role %s: not deterministic", r.Name)
		}
	}
}

func TestModuleRoleRefusals(t *testing.T) {
	env := testModuleEnv()
	if _, err := ModuleRoleStatements(env, Role{Name: "x", Hosts: []Module{"nope"}}); err == nil {
		t.Error("unknown module accepted")
	}
	// The flag is read from the module's own table, so the cloudflare role
	// needs no table of the backup module.
	delete(env.TableArns, ModuleBackup)
	if _, err := ModuleRoleStatements(env, testRole(RoleCloudflare)); err != nil {
		t.Errorf("a role that does not host the backup module needs its table: %v", err)
	}
	delete(env.TableArns, ModuleCloudflare)
	if _, err := ModuleRoleStatements(env, testRole(RoleCloudflare)); err == nil {
		t.Error("a missing table of the role's own module was accepted")
	}
}

func TestRestoreInvokeStatement(t *testing.T) {
	st := RestoreInvokeStatement("fn", []string{"a", "b"})
	if st["Effect"] != "Allow" || st["Resource"] != "fn" {
		t.Fatalf("%v", st)
	}
}

// A role that hosts the Google module names it `google` everywhere: its table,
// its parameters and its blob prefix (the hub's snapshots). `directory` and
// `snapshots` are not names in IAM.
func TestGoogleRoleNamesGoogleInStorageAndPaths(t *testing.T) {
	doc := rendered(t, Role{Name: "google", Hosts: []Module{ModuleGoogle}})
	for _, want := range []string{
		"/sluis/i1/internal/google/*", "/sluis/i1/external/google/*", "tbl:google", "bkt/google/*",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q in %s", want, doc)
		}
	}
	for _, not := range []string{"directory", "snapshots", "oidc/state-secret", "oidc/recovery", "oidc/clients", "tbl:oidc", "bkt/oidc"} {
		if strings.Contains(doc, not) {
			t.Errorf("unexpected %q in %s", not, doc)
		}
	}
}

// credentials.preset: the issuer holds no reference to the minter's parameter
// whatever the document declares (it asks the cloudflare function to mint, v1.76
// E-FIN), and the module's own role has it already and gets no extra statement.
func TestModuleRoleMinterCrossGrant(t *testing.T) {
	env := testModuleEnv()
	env.MinterRefs = []string{"internal/cloudflare/main/minter"}
	issuer, err := ModuleRolePolicy(env, testRoles()[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, not := range []string{"SluisCrossCloudflareMinter", "internal/cloudflare", "SluisParameterKeyCross"} {
		if strings.Contains(issuer, not) {
			t.Errorf("the issuer carries %q: %s", not, issuer)
		}
	}
	own, err := ModuleRolePolicy(env, testRole(RoleCloudflare))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(own, "SluisCrossCloudflareMinter") {
		t.Errorf("the module's own role gets a cross-grant: %s", own)
	}
}

// A listing is authorised against the bare path ARN, so a role lists exactly
// its own modules' two paths: not `/*`, not a sibling, not the root.
func TestModuleRoleListsOnlyItsOwnBarePaths(t *testing.T) {
	env := testModuleEnv()
	base := arnPrefix + "ssm:" + env.Region + ":" + env.Account + ":parameter/sluis/i1/"
	for _, role := range testRoles() {
		st, err := ModuleRoleStatements(env, role)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for _, m := range role.Hosts {
			want[base+"internal/"+string(m)] = true
			want[base+"external/"+string(m)] = true
		}
		got := map[string]bool{}
		for _, s := range st {
			b, _ := json.Marshal(s)
			if !strings.Contains(string(b), ssmGetParametersByPath) {
				continue
			}
			acts, _ := s["Action"].([]string)
			if len(acts) != 1 || acts[0] != ssmGetParametersByPath || s["Effect"] != "Allow" {
				t.Errorf("%s: list statement carries more than the list: %s", role.Name, b)
			}
			res, _ := s["Resource"].([]string)
			for _, r := range res {
				if strings.HasSuffix(r, "*") {
					t.Errorf("%s: list resource %q is wider than a bare path", role.Name, r)
				}
				got[r] = true
			}
		}
		if len(got) != len(want) {
			t.Errorf("%s: list resources %v, want %v", role.Name, got, want)
		}
		for r := range want {
			if !got[r] {
				t.Errorf("%s: no list on %s", role.Name, r)
			}
		}
	}
}
