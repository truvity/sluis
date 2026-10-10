package sluispulumi_test

import (
	"reflect"
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
		"legacy unnamed":   {Modules: arp.ModuleSet{Instance: "k"}, Legacy: &arp.LegacyStateArgs{StateArgs: arp.StateArgs{TableName: "old"}}},
		"legacy is a module's": {Modules: arp.ModuleSet{Instance: "k"},
			Legacy: &arp.LegacyStateArgs{Name: "x", StateArgs: arp.StateArgs{TableName: "sluis-k-oidc"}}},
	} {
		if _, err := statesRun(t, a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Adoption is the same resource: the legacy table is declared as NewState alone
// declares it, at the same URN, so a preview shows no replace and no delete.
func TestTheLegacyTableIsTheSameResourceAsNewStateMadeIt(t *testing.T) {
	alone, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		_, err := arp.NewState(ctx, "staging", &arp.StateArgs{TableName: table})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	both, err := statesRun(t, &arp.StatesArgs{
		Modules: arp.ModuleSet{Instance: "prod"},
		Legacy:  &arp.LegacyStateArgs{Name: "staging", StateArgs: arp.StateArgs{TableName: table}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range alone.ofType("sluis:aws:State") {
		if !both.has("sluis:aws:State", d.Name) {
			t.Errorf("the legacy component %s is not declared", d.Name)
		}
	}
	old, adopted := alone.one(t, tableType, "staging-table"), both.one(t, tableType, "staging-table")
	if !reflect.DeepEqual(old.Inputs, adopted.Inputs) {
		t.Errorf("the legacy table's inputs changed:\n%v\n%v", old.Inputs, adopted.Inputs)
	}
	// Protected, with DynamoDB's deletion protection: leaving the argument out
	// later is refused by the engine until `pulumi state unprotect`.
	if !both.isProtected(tableType, "staging-table") || !prop(adopted, "deletionProtectionEnabled").BoolValue() {
		t.Error("the legacy table is not protected")
	}
	if got := len(both.ofType(tableType)); got != 1+len(arp.Modules()) {
		t.Errorf("%d tables", got)
	}
}

// Without the argument the library declares no legacy resource; the stack then
// holds a protected resource the program no longer names, and the engine's delete
// of it fails. The guard is the protection and the table's own flag, both
// asserted above; here, that a stack that never had one declares none.
func TestWithoutALegacyTableNoneIsDeclared(t *testing.T) {
	rec, err := statesRun(t, &arp.StatesArgs{Modules: arp.ModuleSet{Instance: "prod"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.ofType("sluis:aws:State")) != 0 {
		t.Error("a legacy component without Legacy")
	}
}

func TestTheGrantCarriesTheLegacyArnAndEachModulesTable(t *testing.T) {
	_, out, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		s, err := arp.NewStates(ctx, "access", &arp.StatesArgs{
			Modules: arp.ModuleSet{Instance: "prod"},
			Legacy:  &arp.LegacyStateArgs{Name: "staging", StateArgs: arp.StateArgs{TableName: table}},
		})
		if err != nil {
			return err
		}
		g := s.Grant()
		collect("legacy", g.TableArn)
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
	if out["legacy"] != mockTableArn(table) {
		t.Errorf("legacy %q", out["legacy"])
	}
	for _, m := range arp.Modules() {
		want := "sluis-prod-" + string(m)
		if out["arn-"+string(m)] != mockTableArn(want) || out["name-"+string(m)] != want {
			t.Errorf("%s: %q %q", m, out["arn-"+string(m)], out["name-"+string(m)])
		}
	}
}

// The role gets the tables of the modules it hosts, written; the backup table
// for the maintenance item only; the minter's only with the minter. (Layout
// v4+v5: on v4 the module tables are not granted, see layout_test.go.)
func TestTheFunctionRoleIsGrantedTheModuleTablesAndTheLegacyOne(t *testing.T) {
	tbl := map[arp.Module]pulumi.StringInput{}
	for _, m := range arp.Modules() {
		tbl[m] = pulumi.String(tableArn("sluis-staging-" + string(m)))
	}
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
		a.Layout = arp.LayoutV4V5
		a.State.Tables = tbl
		a.State.KeyArn = pulumi.String(arnp + "kms:" + region + ":" + account + ":key/tables")
	}})
	doc := statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue())
	byResource := map[string][]string{}
	for _, s := range doc {
		for _, r := range strs(s["Resource"]) {
			byResource[r] = append(byResource[r], strs(s["Action"])...)
		}
	}
	for _, m := range []arp.Module{arp.ModuleOIDC, arp.ModuleGitHub, arp.ModuleSlack, arp.ModuleGoogle} {
		acts := byResource[tableArn("sluis-staging-"+string(m))]
		sort.Strings(acts)
		if !contains(acts, "dynamodb:PutItem") || !contains(acts, "dynamodb:GetItem") {
			t.Errorf("%s: %v", m, acts)
		}
	}
	if got := byResource[tableArn("sluis-staging-backup")]; len(got) != 0 {
		t.Errorf("backup: %v", got)
	}
	if got := byResource[tableArn("sluis-staging-cloudflare")]; len(got) != 0 {
		t.Errorf("the minter's table without the minter: %v", got)
	}
	if got := byResource[tableArn(table)]; !contains(got, "dynamodb:PutItem") {
		t.Errorf("the legacy table lost its grant: %v", got)
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

// A golden: the block a layout v5 installation's document carries.
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

func TestRenderPortsTablesAndTableDoNotMix(t *testing.T) {
	for name, p := range map[string]arp.PortsArgs{
		"both":           {BucketName: bucket, TableName: table, Tables: map[arp.Module]string{arp.ModuleOIDC: "x"}},
		"unknown module": {BucketName: bucket, Tables: map[arp.Module]string{"nope": "x"}},
		"empty name":     {BucketName: bucket, Tables: map[arp.Module]string{arp.ModuleOIDC: ""}},
	} {
		if _, err := arp.RenderPorts(p); err == nil || strings.Contains(err.Error(), "<nil>") {
			t.Errorf("%s: accepted", name)
		}
	}
}
