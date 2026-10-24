package sluispulumi_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	yaml "go.yaml.in/yaml/v3"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

const tableType = "aws:dynamodb/table:Table"

func mockTableArn(name string) string {
	return arnp + "dynamodb:eu-west-1:" + account + ":table/" + name
}

func tableArn(name string) string {
	return arnp + "dynamodb:" + region + ":" + account + ":table/" + name
}

// moduleTables is a grant of every module's table, `sluis-staging-<module>`, as
// States.Grant() gives it.
func moduleTables() map[arp.Module]pulumi.StringInput {
	tbl := map[arp.Module]pulumi.StringInput{}
	for _, m := range arp.Modules() {
		tbl[m] = pulumi.String(tableArn("sluis-staging-" + string(m)))
	}
	return tbl
}

func statesRun(t *testing.T, args *arp.StatesArgs) (*recorder, error) {
	t.Helper()
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		_, err := arp.NewStates(ctx, "access", args)
		return err
	})
	return rec, err
}

func TestStatesCreateOneProtectedTablePerModule(t *testing.T) {
	rec, err := statesRun(t, &arp.StatesArgs{Modules: arp.ModuleSet{Instance: "prod"}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range arp.Modules() {
		res := "access-" + string(m) + "-table"
		d := rec.one(t, tableType, res)
		want := "sluis-prod-" + string(m)
		names = append(names, want)
		if prop(d, "name").StringValue() != want {
			t.Errorf("%s: name %q", m, prop(d, "name").StringValue())
		}
		if prop(d, "billingMode").StringValue() != "PAY_PER_REQUEST" ||
			!prop(d, "deletionProtectionEnabled").BoolValue() ||
			!prop(d, "pointInTimeRecovery").ObjectValue()["enabled"].BoolValue() {
			t.Errorf("%s: %v", m, d.Inputs)
		}
		ttl := prop(d, "ttl").ObjectValue()
		if ttl["attributeName"].StringValue() != "expires" || !ttl["enabled"].BoolValue() {
			t.Errorf("%s: ttl %v", m, ttl)
		}
		if !rec.isProtected(tableType, res) {
			t.Errorf("%s: the table is not protected", m)
		}
	}
	if got := len(rec.ofType(tableType)); got != len(names) {
		t.Errorf("%d tables, want %d", got, len(names))
	}
}

func TestStatesNameOverridesAndOnlyAreHonoured(t *testing.T) {
	rec, err := statesRun(t, &arp.StatesArgs{
		Modules: arp.ModuleSet{Instance: "k", Tables: map[arp.Module]string{arp.ModuleSlack: "k-slack"}},
		Only:    []arp.Module{arp.ModuleOIDC, arp.ModuleSlack},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(rec.ofType(tableType)); got != 2 {
		t.Fatalf("%d tables", got)
	}
	if n := prop(rec.one(t, tableType, "access-slack-table"), "name").StringValue(); n != "k-slack" {
		t.Errorf("override: %s", n)
	}
}

func TestStatesRefuseWhatCannotBeDeclared(t *testing.T) {
	for name, a := range map[string]*arp.StatesArgs{
		"nil":              nil,
		"no instance":      {},
		"unknown module":   {Modules: arp.ModuleSet{Instance: "k"}, Only: []arp.Module{"nope"}},
		"same table twice": {Modules: arp.ModuleSet{Instance: "k", Tables: map[arp.Module]string{arp.ModuleSlack: "sluis-k-oidc"}}},
	} {
		if _, err := statesRun(t, a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheGrantCarriesEachModulesTable(t *testing.T) {
	_, out, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		s, err := arp.NewStates(ctx, "access", &arp.StatesArgs{Modules: arp.ModuleSet{Instance: "prod"}})
		if err != nil {
			return err
		}
		g := s.Grant()
		for m, a := range g.Tables {
			collect("arn-"+string(m), a)
		}
		for m, n := range s.Tables {
			collect("name-"+string(m), n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range arp.Modules() {
		want := "sluis-prod-" + string(m)
		if out["arn-"+string(m)] != mockTableArn(want) || out["name-"+string(m)] != want {
			t.Errorf("%s: %q %q", m, out["arn-"+string(m)], out["name-"+string(m)])
		}
	}
}

// The role gets the tables of the modules it hosts, written; the backup table
// for the maintenance item only; the minter's only with the minter.
func TestTheFunctionRoleIsGrantedTheModuleTables(t *testing.T) {
	rec, _ := mustLambda(t, estateWith(func(a *arp.LambdaArgs) {
		a.State.KeyArn = pulumi.String(arnp + "kms:" + region + ":" + account + ":key/tables")
	}))
	doc := statements(t, prop(rec.one(t, policyType, "staging-issuer-policy"), "policy").StringValue())
	byResource := map[string][]string{}
	for _, s := range doc {
		for _, r := range strs(s["Resource"]) {
			byResource[r] = append(byResource[r], strs(s["Action"])...)
		}
	}
	for _, m := range []arp.Module{arp.ModuleOIDC, arp.ModuleGoogle} {
		acts := byResource[tableArn("sluis-staging-"+string(m))]
		sort.Strings(acts)
		if !contains(acts, "dynamodb:PutItem") || !contains(acts, "dynamodb:GetItem") {
			t.Errorf("%s: %v", m, acts)
		}
	}
	// The providers' tables are theirs: the issuer holds no grant on them.
	for _, m := range []arp.Module{arp.ModuleGitHub, arp.ModuleSlack} {
		if got := byResource[tableArn("sluis-staging-"+string(m))]; len(got) != 0 {
			t.Errorf("%s: the issuer is granted %v", m, got)
		}
	}
	if got := byResource[tableArn("sluis-staging-backup")]; len(got) != 0 {
		t.Errorf("backup: %v", got)
	}
	if got := byResource[tableArn("sluis-staging-cloudflare")]; len(got) != 0 {
		t.Errorf("the minter's table without the minter: %v", got)
	}
	if got := byResource[tableArn(table)]; len(got) != 0 {
		t.Errorf("a single table is granted: %v", got)
	}
}

func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// A golden: the block an installation's document carries.
func TestRenderPortsTablesGolden(t *testing.T) {
	tables := map[arp.Module]string{}
	for _, m := range arp.Modules() {
		tables[m] = "sluis-prod-" + string(m)
	}
	y, err := arp.RenderPortsYAML(arp.PortsArgs{BucketName: bucket, Region: "eu-west-1", Tables: tables})
	if err != nil {
		t.Fatal(err)
	}
	const golden = `ports:
    adapter: dynamodb
    blob:
        adapter: s3
        s3:
            bucket: acme-sluis
            region: eu-west-1
    dynamodb:
        region: eu-west-1
        tables:
            backup: sluis-prod-backup
            cloudflare: sluis-prod-cloudflare
            github: sluis-prod-github
            google: sluis-prod-google
            oidc: sluis-prod-oidc
            outline: sluis-prod-outline
            slack: sluis-prod-slack
`
	if y != golden {
		t.Errorf("ports:\n%s", y)
	}
	// The schema takes it with the layout that goes with it.
	var ports map[string]any
	if err := yaml.Unmarshal([]byte(y), &ports); err != nil {
		t.Fatal(err)
	}
	ports["secrets"] = map[string]any{"source": "ssm", "root": "/sluis/prod", "layout": "v5"}
	if err := validate(t, "serve", ports); err != nil {
		t.Errorf("serve schema: %v", err)
	}
}

func TestRenderPortsRefusesBadTables(t *testing.T) {
	for name, p := range map[string]arp.PortsArgs{
		"unknown module": {BucketName: bucket, Tables: map[arp.Module]string{"nope": "x"}},
		"empty name":     {BucketName: bucket, Tables: map[arp.Module]string{arp.ModuleOIDC: ""}},
	} {
		if _, err := arp.RenderPorts(p); err == nil || strings.Contains(err.Error(), "<nil>") {
			t.Errorf("%s: accepted", name)
		}
	}
}
