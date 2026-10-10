package sluispulumi_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"go.yaml.in/yaml/v3"

	sluisconfig "github.com/truvity/sluis/config"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

const ssmParameterType = "aws:ssm/parameter:Parameter"

// withLayout is an estate on a layout: the module tables beside the legacy one.
func withLayout(l arp.Layout, more ...func(*arp.LambdaArgs)) estate {
	return estate{mutate: func(a *arp.LambdaArgs) {
		a.Layout = l
		tbl := map[arp.Module]pulumi.StringInput{}
		for _, m := range arp.Modules() {
			tbl[m] = pulumi.String(tableArn("sluis-staging-" + string(m)))
		}
		a.State.Tables = tbl
		for _, f := range more {
			f(a)
		}
	}}
}

func policyOf(t *testing.T, e estate) []map[string]any {
	t.Helper()
	rec, _ := mustLambda(t, e)
	return statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue())
}

func resourcesOf(st []map[string]any) []string {
	var out []string
	for _, s := range st {
		out = append(out, strs(s["Resource"])...)
	}
	return out
}

func sids(st []map[string]any) []string {
	var out []string
	for _, s := range st {
		out = append(out, s["Sid"].(string))
	}
	return out
}

var (
	paramRoot = arnp + "ssm:" + region + ":" + account + ":parameter/sluis/staging"
	legacyArn = tableArn(table)
)

// Layout v4 is what it was: the legacy table and the v4 paths, and nothing of
// the module tables or the v5 paths.
func TestLayoutV4KeepsTheV4GrantsOnly(t *testing.T) {
	for _, l := range []arp.Layout{"", arp.LayoutV4} {
		st := policyOf(t, withLayout(l))
		res := resourcesOf(st)
		if !slices.Contains(res, legacyArn) || !slices.Contains(res, paramRoot+"/internal/config/*") {
			t.Errorf("%q: the v4 grants are missing: %v", l, res)
		}
		for _, r := range res {
			if strings.Contains(r, "sluis-staging-") || strings.Contains(r, "/internal/oidc") || strings.Contains(r, "/external/oidc") {
				t.Errorf("%q: a v5 resource on layout v4: %s", l, r)
			}
		}
	}
}

// Layout v5 is the v5 grants only: the modules the one function hosts, each its
// own table, parameters and blob prefix; no legacy table, no v4 path.
func TestLayoutV5GetsOnlyTheV5Grants(t *testing.T) {
	st := policyOf(t, withLayout(arp.LayoutV5))
	res := resourcesOf(st)
	for _, want := range []string{
		tableArn("sluis-staging-oidc"), tableArn("sluis-staging-github"), tableArn("sluis-staging-slack"), tableArn("sluis-staging-google"),
		paramRoot + "/internal/oidc/*", paramRoot + "/external/oidc/*", paramRoot + "/internal/google/*", paramRoot + "/internal/github/*",
		paramRoot + "/internal/slack/*", arnp + "s3:::" + bucket + "/oidc/*", arnp + "s3:::" + bucket + "/google/*",
	} {
		if !slices.Contains(res, want) {
			t.Errorf("v5: no grant on %s", want)
		}
	}
	for _, r := range res {
		switch {
		case r == legacyArn, strings.HasPrefix(r, paramRoot+"/internal/config"), strings.HasPrefix(r, paramRoot+"/internal/credentials"),
			r == paramRoot+"/external/*", r == arnp+"s3:::"+bucket+"/*", r == paramRoot+"/internal/*",
			strings.Contains(r, "sluis-staging-cloudflare"), strings.Contains(r, "/internal/cloudflare"):
			t.Errorf("v5: a v4 or an unhosted resource is granted: %s", r)
		}
	}
	for _, r := range res {
		if r == tableArn("sluis-staging-backup") {
			t.Errorf("the backup table is named: %v", r)
		}
	}
	// The rest of the role is the same on every layout.
	for _, sid := range []string{"SluisLogs", "SluisRunAPass", "SluisWebIdentity"} {
		if !slices.Contains(sids(st), sid) {
			t.Errorf("v5: %s is gone", sid)
		}
	}
}

// Layout v4+v5 is both, and a statement id is unique in the policy.
func TestLayoutV4V5GetsBothAndNoSidTwice(t *testing.T) {
	key := func(a *arp.LambdaArgs) {
		a.ParameterKeyArn = paramKey
		a.State.KeyArn = pulumi.String(arnp + "kms:" + region + ":" + account + ":key/tables")
	}
	st := policyOf(t, withLayout(arp.LayoutV4V5, key))
	res := resourcesOf(st)
	for _, want := range []string{
		legacyArn, paramRoot + "/internal/config/*", paramRoot + "/internal/credentials/*", paramRoot + "/external/*",
		tableArn("sluis-staging-oidc"), paramRoot + "/internal/oidc/*", arnp + "s3:::" + bucket + "/*", arnp + "s3:::" + bucket + "/oidc/*",
	} {
		if !slices.Contains(res, want) {
			t.Errorf("v4+v5: no grant on %s", want)
		}
	}
	seen := map[string]bool{}
	for _, sid := range sids(st) {
		if seen[sid] {
			t.Errorf("Sid %s is in the policy twice", sid)
		}
		seen[sid] = true
	}
	// The v5 policy alone has the same ids for what the layouts share.
	for _, l := range []arp.Layout{arp.LayoutV4, arp.LayoutV5} {
		for _, sid := range sids(policyOf(t, withLayout(l, key))) {
			if !seen[sid] {
				t.Errorf("v4+v5 lacks %s, which %s has", sid, l)
			}
		}
	}
}

// The Cloudflare minter's module is hosted when the installation declares
// presets; the v5 role then has its table and its parameters, and not before.
func TestLayoutV5HostsTheCloudflareModuleWithPresets(t *testing.T) {
	in := exampleInstallation(t)
	withCloudflare(in)
	in.AWS.Table = ""
	st := policyOf(t, withInstallation(in, func(a *arp.LambdaArgs) {
		withLayout(arp.LayoutV5).mutate(a)
	}))
	res := resourcesOf(st)
	if !slices.Contains(res, tableArn("sluis-staging-cloudflare")) {
		t.Errorf("no table for the minter: %v", res)
	}
	if !slices.Contains(res, arnp+"ssm:"+region+":"+account+":parameter/sluis/example/internal/cloudflare/*") {
		t.Errorf("no parameters for the minter: %v", res)
	}
}

func TestTheLayoutIsValidated(t *testing.T) {
	for name, e := range map[string]estate{
		"unknown layout":                withLayout("v6"),
		"v5 without the module tables":  {mutate: func(a *arp.LambdaArgs) { a.Layout = arp.LayoutV5 }},
		"v4+v5 without the legacy":      withLayout(arp.LayoutV4V5, func(a *arp.LambdaArgs) { a.State.TableArn = nil }),
		"DropV4 on v4":                  {mutate: func(a *arp.LambdaArgs) { a.Compat = &arp.CompatArgs{DropV4: true} }},
		"DropV4 on v4+v5":               withLayout(arp.LayoutV4V5, func(a *arp.LambdaArgs) { a.Compat = &arp.CompatArgs{DropV4: true} }),
		"an unknown module's table":     withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) { a.TableNames = map[arp.Module]string{"nope": "x"} }),
		"a migration role that is not":  withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) { a.MigrationRoleArn = arnp + "iam::" + account + ":user/x" }),
		"a v4 secrets.layout under v5":  withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) { a.Config += "secrets: {layout: v4}\n" }),
		"the v4 table in a v5 document": withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) { a.Config += "ports: {dynamodb: {table: t}}\n" }),
	} {
		_, _, err := buildLambda(t, e)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func noTable(in *sluisconfig.Installation) *sluisconfig.Installation {
	in.AWS.Table = ""
	return in
}

// portsDynamoOf reads the rendered service document's ports.dynamodb.
func portsDynamoOf(t *testing.T, doc string) map[string]any {
	t.Helper()
	var m struct {
		Ports struct {
			Adapter  string         `yaml:"adapter"`
			DynamoDB map[string]any `yaml:"dynamodb"`
		} `yaml:"ports"`
	}
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	return m.Ports.DynamoDB
}

// Only layout v5 writes `secrets.layout: v5` and the tables into the document,
// from a Config and from an Installation; v4 and v4+v5 write neither.
func TestOnlyLayoutV5WritesTheTablesIntoTheDocument(t *testing.T) {
	for _, l := range []arp.Layout{"", arp.LayoutV4, arp.LayoutV4V5} {
		rec, _ := mustLambda(t, withLayout(l))
		doc := layerFiles(t, rec)["sluis/sluis.yaml"]
		if s := secretsOf(t, doc); s["layout"] != nil {
			t.Errorf("%q: secrets.layout = %v", l, s["layout"])
		}
		if d := portsDynamoOf(t, doc); d["tables"] != nil {
			t.Errorf("%q: tables in the document: %v", l, d)
		}
	}
	rename := func(a *arp.LambdaArgs) { a.TableNames = map[arp.Module]string{arp.ModuleSlack: "custom-slack"} }
	for name, c := range map[string]struct {
		e        estate
		instance string
	}{
		"config": {withLayout(arp.LayoutV5, rename), "staging"},
		"installation": {withInstallation(noTable(exampleInstallation(t)), func(a *arp.LambdaArgs) {
			withLayout(arp.LayoutV5, rename).mutate(a)
		}), "example"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := mustLambda(t, c.e)
			doc := layerFiles(t, rec)["sluis/sluis.yaml"]
			if s := secretsOf(t, doc); s["layout"] != "v5" || s["source"] != "ssm" {
				t.Errorf("secrets = %v", s)
			}
			d := portsDynamoOf(t, doc)
			tables, _ := d["tables"].(map[string]any)
			if len(tables) != len(arp.Modules()) || tables["oidc"] != "sluis-"+c.instance+"-oidc" || tables["slack"] != "custom-slack" {
				t.Errorf("tables = %v", d)
			}
			if d["table"] != nil {
				t.Errorf("the v4 table beside the tables: %v", d)
			}
		})
	}
}

// An installation with the v4 table is refused under v5: the layouts do not mix.
func TestAnInstallationWithTheV4TableIsRefusedOnV5(t *testing.T) {
	in := exampleInstallation(t)
	if in.AWS.Table == "" {
		t.Skip("the example installation names no table")
	}
	if _, _, err := buildLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { withLayout(arp.LayoutV5).mutate(a) })); err == nil ||
		!strings.Contains(err.Error(), "AWS.Table") {
		t.Errorf("error: %v", err)
	}
}

// The v5 parameters hold the very value of the v4 ones: the same generators.
func TestTheV5ParametersAreTheSameValuesAtTheOIDCAddresses(t *testing.T) {
	rec, out := mustLambda(t, withLayout(arp.LayoutV4V5))
	byPath := map[string]declared{}
	for _, p := range rec.ofType(ssmParameterType) {
		byPath[prop(p, "name").StringValue()] = p
	}
	for v4, v5 := range map[string]string{
		arp.StateSecretParameterName("staging"):      arp.StateSecretParameterNameV5("staging"),
		arp.RecoveryPasswordParameterName("staging"): arp.RecoveryPasswordParameterNameV5("staging"),
	} {
		a, b := byPath[v4], byPath[v5]
		if a.Name == "" || b.Name == "" {
			t.Fatalf("parameters at %s and %s: %v", v4, v5, slices.Collect(mapKeys(byPath)))
		}
		if !prop(a, "value").DeepEquals(prop(b, "value")) {
			t.Errorf("%s and %s hold different values", v4, v5)
		}
		if prop(b, "type").StringValue() != "SecureString" || !prop(b, "overwrite").BoolValue() {
			t.Errorf("%s: %v", v5, b.Inputs)
		}
	}
	if got := arp.StateSecretParameterNameV5("staging"); got != "/sluis/staging/internal/oidc/state-secret" {
		t.Errorf("state secret v5 = %s", got)
	}
	if got := arp.RecoveryPasswordParameterNameV5("staging"); got != "/sluis/staging/internal/oidc/recovery-password" {
		t.Errorf("recovery v5 = %s", got)
	}
	if got := arp.ConfigParameterPrefixV5("staging"); got != "/sluis/staging/internal/oidc" {
		t.Errorf("prefix v5 = %s", got)
	}
	// One generator each, however many parameters.
	if n := len(rec.ofType("random:index/randomBytes:RandomBytes")) + len(rec.ofType("random:index/randomPassword:RandomPassword")); n != 2 {
		t.Errorf("%d generators", n)
	}
	// On v4+v5 the outputs still name the v4 addresses.
	if out["stateSecretParameter"] != arp.StateSecretParameterName("staging") {
		t.Errorf("stateSecretParameter = %s", out["stateSecretParameter"])
	}
}

func mapKeys[K comparable, V any](m map[K]V) func(func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// previewDiff is what a preview would say between two programs, from the resources
// the mocks recorded: a resource (type and logical name) only after is created,
// only before is deleted, and in both with another physical name is replaced.
type previewDiff struct{ created, deleted, replaced []string }

func previewShape(before, after *recorder, typ string) previewDiff {
	index := func(r *recorder) map[string]string {
		m := map[string]string{}
		for _, d := range r.ofType(typ) {
			m[d.Name] = prop(d, "name").StringValue()
		}
		return m
	}
	b, a := index(before), index(after)
	var s previewDiff
	for n, name := range a {
		old, had := b[n]
		switch {
		case !had:
			s.created = append(s.created, n)
		case old != name:
			s.replaced = append(s.replaced, n)
		}
	}
	for n := range b {
		if _, kept := a[n]; !kept {
			s.deleted = append(s.deleted, n)
		}
	}
	slices.Sort(s.created)
	slices.Sort(s.deleted)
	slices.Sort(s.replaced)
	return s
}

func (s previewDiff) String() string {
	return strings.Join([]string{
		strconv.Itoa(len(s.created)) + " created", strconv.Itoa(len(s.deleted)) + " deleted", strconv.Itoa(len(s.replaced)) + " replaced",
	}, ", ")
}

// An estate goes v4, v4+v5, v5 and drops v4: the first apply creates the two v5
// parameters and deletes or replaces nothing; the flip creates nothing and
// deletes nothing (the v4 parameters stay for the rollback window); dropping
// them deletes exactly the two.
func TestThePreviewShapeAcrossTheLayouts(t *testing.T) {
	build := func(e estate) *recorder { rec, _ := mustLambda(t, e); return rec }
	v4 := build(withLayout(arp.LayoutV4))
	window := build(withLayout(arp.LayoutV4V5))
	flipped := build(withLayout(arp.LayoutV5))
	dropped := build(withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) { a.Compat = &arp.CompatArgs{DropV4: true} }))

	for _, c := range []struct {
		name          string
		before, after *recorder
		want          string
	}{
		{"v4 -> v4+v5", v4, window, "2 created, 0 deleted, 0 replaced"},
		{"v4+v5 -> v5", window, flipped, "0 created, 0 deleted, 0 replaced"},
		{"v5 -> v5 + DropV4", flipped, dropped, "0 created, 2 deleted, 0 replaced"},
		{"v4+v5 -> v5 + DropV4", window, dropped, "0 created, 2 deleted, 0 replaced"},
	} {
		if got := previewShape(c.before, c.after, ssmParameterType).String(); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	got := previewShape(window, dropped, ssmParameterType)
	if !slices.Equal(got.deleted, []string{"staging-recovery-password-internal", "staging-state-secret-internal"}) {
		t.Errorf("deleted: %v", got.deleted)
	}
	// Nothing else of the stack is created, deleted or replaced by a layout change,
	// bar the configuration layer's new version and the check's re-run.
	for _, typ := range []string{fnType, policyType, "aws:iam/role:Role", "aws:dynamodb/table:Table"} {
		if s := previewShape(v4, window, typ); len(s.created)+len(s.deleted)+len(s.replaced) != 0 {
			t.Errorf("%s: %s", typ, s)
		}
	}
	// A born-on-v5 estate has no v4 parameters at all.
	if n := len(dropped.ofType(ssmParameterType)); n != 2 {
		t.Errorf("a v5 estate with DropV4 has %d parameters, want the two v5 ones", n)
	}
}

// A process that mints its blob credentials from a Cloudflare preset reads the
// minter's parameter, and only that one, when it does not host the module (the
// document declares the preset and no installation says the function hosts it).
func TestAPresetBlobCredentialReadsTheMinterParameterOnV5(t *testing.T) {
	const r2 = "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
	cfg := "issuerURL: https://x.example\n" +
		"cloudflare: {accounts: {main: {id: 0123456789abcdef0123456789abcdef, minter: internal/cloudflare/main/minter}}, " +
		"presets: {blobs: {account: main, prototype: proto-r2-0001, description: R2, lifetime: 15m, rotation: 5m, endpoint: '" + r2 + "'}}}\n" +
		"ports: {blob: {adapter: s3, s3: {bucket: b, endpoint: '" + r2 + "', credentials: {preset: blobs}}}}\n"
	e := withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) { a.AllowEndpoints = true })
	e.config = cfg
	st := policyOf(t, e)
	want := paramRoot + "/internal/cloudflare/main/minter"
	var got map[string]any
	for _, s := range st {
		if s["Sid"] == "SluisCrossCloudflareMinter" {
			got = s
		}
	}
	if got == nil || !slices.Equal(strs(got["Resource"]), []string{want}) {
		t.Fatalf("minter grant: %v", got)
	}
	if a := strs(got["Action"]); slices.Contains(a, "ssm:PutParameter") || slices.Contains(a, "ssm:DeleteParameter") {
		t.Errorf("the minter's credential is writable: %v", a)
	}
	for _, r := range resourcesOf(st) {
		if strings.Contains(r, "/internal/cloudflare") && r != want {
			t.Errorf("more of the Cloudflare module than the minter: %s", r)
		}
	}
	// Without the preset, nothing of the module.
	for _, r := range resourcesOf(policyOf(t, withLayout(arp.LayoutV5))) {
		if strings.Contains(r, "/internal/cloudflare") {
			t.Errorf("a Cloudflare grant without a preset: %s", r)
		}
	}
	// On v4 the document is the v4 one and nothing is added.
	v4 := estate{config: cfg, mutate: func(a *arp.LambdaArgs) { a.AllowEndpoints = true }}
	if slices.Contains(sids(policyOf(t, v4)), "SluisCrossCloudflareMinter") {
		t.Error("the v5 cross-grant on layout v4")
	}
}

// Only the migration role reads the legacy table, and only reads it; nothing is
// attached without the argument.
func TestTheMigrationRoleReadsTheLegacyTableAndNothingElse(t *testing.T) {
	roleArn := arnp + "iam::" + account + ":role/ops/migration"
	rec, _ := mustLambda(t, withLayout(arp.LayoutV5))
	if rec.has(policyType, "staging-migration-legacy-read") {
		t.Error("a policy is attached to a role nobody named")
	}
	rec, _ = mustLambda(t, withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) {
		a.MigrationRoleArn = roleArn
		a.State.KeyArn = pulumi.String(arnp + "kms:" + region + ":" + account + ":key/tables")
	}))
	p := rec.one(t, policyType, "staging-migration-legacy-read")
	if prop(p, "role").StringValue() != "migration" {
		t.Errorf("role = %v", prop(p, "role"))
	}
	st := statements(t, prop(p, "policy").StringValue())
	g := grants(st)
	if got := resourcesOf(st); !slices.Contains(got, legacyArn) {
		t.Errorf("resources: %v", got)
	}
	for _, w := range []string{"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:BatchWriteItem", "kms:Encrypt", "kms:GenerateDataKey"} {
		if _, has := g[w]; has {
			t.Errorf("the migration role may %s", w)
		}
	}
	for _, w := range []string{"dynamodb:GetItem", "dynamodb:Query", "dynamodb:Scan", "dynamodb:DescribeTable", "kms:Decrypt"} {
		if _, has := g[w]; !has {
			t.Errorf("the migration role may not %s", w)
		}
	}
	// The function's own role has no part of the legacy table on v5.
	for _, r := range resourcesOf(policyOf(t, withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) { a.MigrationRoleArn = roleArn }))) {
		if r == legacyArn {
			t.Error("the function's role reads the legacy table on v5")
		}
	}
}

// The maintenance partition is denied for writes on the module tables the role
// holds, in one statement, on v5 and v4+v5. Layout v4 has no module tables and
// is what it was.
func TestTheMaintenanceDenyCoversTheModuleTables(t *testing.T) {
	var want []string
	for _, m := range []string{"oidc", "github", "slack", "google"} {
		want = append(want, tableArn("sluis-staging-"+m))
	}
	slices.Sort(want)
	for _, l := range []arp.Layout{arp.LayoutV4, arp.LayoutV5, arp.LayoutV4V5} {
		var denies []map[string]any
		for _, s := range policyOf(t, withLayout(l)) {
			if s["Sid"] == "SluisMaintenanceDeny" {
				denies = append(denies, s)
			}
		}
		if l == arp.LayoutV4 {
			if len(denies) != 0 {
				t.Errorf("v4: %v", denies)
			}
			continue
		}
		if len(denies) != 1 || denies[0]["Effect"] != "Deny" {
			t.Fatalf("%s: %v", l, denies)
		}
		got := strs(denies[0]["Resource"])
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: deny on %v, want %v", l, got, want)
		}
	}
}
