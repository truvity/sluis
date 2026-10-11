package sluispulumi_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

var moduleRoles = []string{"cloudflare", "github", "google", "slack"}

func v5Estate(more ...func(*arp.LambdaArgs)) estate { return withLayout(arp.LayoutV5, more...) }

// NewLambda always makes the four module functions beside the issuer's: there is
// no switch that leaves a module in the issuer.
func TestNewLambdaMakesAFunctionAndARolePerModule(t *testing.T) {
	rec, _ := mustLambda(t, v5Estate())
	if n := len(rec.ofType(fnType)); n != 1+len(moduleRoles) {
		t.Fatalf("%d functions, want %d", n, 1+len(moduleRoles))
	}
	for _, m := range moduleRoles {
		name := "sluis-staging-" + m
		f := rec.one(t, fnType, "staging-"+m+"-fn")
		if prop(f, "name").StringValue() != name || prop(f, "runtime").StringValue() != "provided.al2023" {
			t.Errorf("%s: %v", m, f.Inputs)
		}
		if r := rec.one(t, "aws:iam/role:Role", "staging-"+m+"-role"); prop(r, "name").StringValue() != name {
			t.Errorf("%s: role %v", m, r.Inputs)
		}
		rec.one(t, policyType, "staging-"+m+"-policy")
		rec.one(t, "aws:cloudwatch/logGroup:LogGroup", "staging-"+m+"-logs")
		for _, al := range []string{"live", "live-issuer"} {
			rec.one(t, "aws:lambda/alias:Alias", "staging-"+m+"-alias-"+al)
		}
		// A run that failed is the next pass's: no platform retry of an event. A
		// synchronous call (the github mint) is not an event and is unaffected.
		if !rec.has("aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig", "staging-"+m+"-async-live") {
			t.Errorf("%s: no no-retry config", m)
		}
	}
	// The issuer's function is named for its module like the others (V13).
	if prop(rec.one(t, fnType, "staging-issuer"), "name").StringValue() != "sluis-staging-issuer" {
		t.Error("the issuer's function was renamed")
	}
}

func TestTheModuleFunctionsRunTheirOwnZips(t *testing.T) {
	e := v5Estate()
	e.pkg = zipFile(t, nil)
	rec, _ := mustLambda(t, e)
	for _, m := range moduleRoles {
		got := packagePath(t, rec.one(t, fnType, "staging-"+m+"-fn"))
		sib := filepath.Join(filepath.Dir(e.pkg), "sluis-"+m+"_1.63.0_linux_arm64.zip")
		if !strings.Contains(got, sha(t, sib)) {
			t.Errorf("%s runs %s, not the %s zip", m, got, m)
		}
	}
}

func TestModulesConfigureMemoryConcurrencyAndName(t *testing.T) {
	one := 3
	rec, _ := mustLambda(t, v5Estate(func(a *arp.LambdaArgs) {
		a.Modules = map[string]arp.ModuleArgs{
			"slack": {MemoryMB: 1024, TimeoutSeconds: 60, ReservedConcurrency: &one, FunctionName: "acme-slack"},
		}
	}))
	f := rec.one(t, fnType, "staging-slack-fn")
	if prop(f, "name").StringValue() != "acme-slack" || prop(f, "memorySize").NumberValue() != 1024 ||
		prop(f, "timeout").NumberValue() != 60 || prop(f, "reservedConcurrentExecutions").NumberValue() != 3 {
		t.Errorf("slack: %v", f.Inputs)
	}
	if prop(rec.one(t, "aws:iam/role:Role", "staging-slack-role"), "name").StringValue() != "acme-slack" {
		t.Error("the role does not follow the name override")
	}
	g := rec.one(t, fnType, "staging-github-fn")
	if prop(g, "memorySize").NumberValue() != 512 || prop(g, "timeout").NumberValue() != 300 || prop(g, "reservedConcurrentExecutions").HasValue() {
		t.Errorf("github defaults: %v", g.Inputs)
	}
	// The issuer's role invokes the overridden name.
	issuer := resourcesOf(statements(t, prop(rec.one(t, policyType, "staging-issuer-policy"), "policy").StringValue()))
	if !slices.Contains(issuer, fnArn("acme-slack")+":live-issuer") {
		t.Errorf("the issuer does not invoke the renamed slack function: %v", issuer)
	}
}

func TestModulesAreRefusedWhenWrong(t *testing.T) {
	one, zero := 1, 0
	for name, e := range map[string]estate{
		"an unknown module":  v5Estate(func(a *arp.LambdaArgs) { a.Modules = map[string]arp.ModuleArgs{"backup": {}} }),
		"a bad name":         v5Estate(func(a *arp.LambdaArgs) { a.Modules = map[string]arp.ModuleArgs{"slack": {FunctionName: "a b"}} }),
		"a zero concurrency": v5Estate(func(a *arp.LambdaArgs) { a.Modules = map[string]arp.ModuleArgs{"slack": {ReservedConcurrency: &zero}} }),
		"too little memory":  v5Estate(func(a *arp.LambdaArgs) { a.Modules = map[string]arp.ModuleArgs{"slack": {MemoryMB: 64}} }),
		"a name taken twice": v5Estate(func(a *arp.LambdaArgs) {
			a.Modules = map[string]arp.ModuleArgs{"slack": {FunctionName: "sluis-staging-github"}}
		}),
		"the issuer's name taken": v5Estate(func(a *arp.LambdaArgs) {
			a.Modules = map[string]arp.ModuleArgs{"google": {FunctionName: "sluis-staging-issuer"}}
		}),
		"modules on layout v4": {mutate: func(a *arp.LambdaArgs) {
			a.Modules = map[string]arp.ModuleArgs{"slack": {ReservedConcurrency: &one}}
		}},
	} {
		if _, _, err := buildLambda(t, e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Layout v4 has no per-module tables to give the roles: it keeps the one function.
func TestLayoutV4HasNoModuleFunctions(t *testing.T) {
	rec, _ := mustLambda(t, withLayout(arp.LayoutV4))
	if n := len(rec.ofType(fnType)); n != 1 {
		t.Errorf("%d functions on layout v4", n)
	}
}

func TestTheOutputsAreKeyedByModule(t *testing.T) {
	_, got, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		l, err := arp.NewLambda(ctx, "staging", v5Estate().args(t))
		if err != nil {
			return err
		}
		js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
		collect("fns", l.Functions.ApplyT(func(m map[string]string) string { return js(m) }).(pulumi.StringOutput))
		collect("roles", l.RoleArns.ApplyT(func(m map[string]string) string { return js(m) }).(pulumi.StringOutput))
		collect("aliases", l.AliasArns.ApplyT(func(m map[string]map[string]string) string { return js(m) }).(pulumi.StringOutput))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := append([]string{"issuer"}, moduleRoles...)
	var fns, roles map[string]string
	var aliases map[string]map[string]string
	for k, v := range map[string]any{"fns": &fns, "roles": &roles, "aliases": &aliases} {
		if err := json.Unmarshal([]byte(got[k]), v); err != nil {
			t.Fatalf("%s: %v (%q)", k, err, got[k])
		}
	}
	for _, k := range keys {
		if fns[k] == "" || roles[k] == "" {
			t.Errorf("%s: function %q role %q", k, fns[k], roles[k])
		}
		if k != "issuer" && (aliases[k]["live"] == "" || aliases[k]["live-issuer"] == "") {
			t.Errorf("%s: aliases %v", k, aliases[k])
		}
	}
	if len(fns) != len(keys) || len(roles) != len(keys) || len(aliases) != len(keys) {
		t.Errorf("outputs hold %d, %d and %d keys, want %d", len(fns), len(roles), len(aliases), len(keys))
	}
	if aliases["issuer"]["live"] == "" {
		t.Error("the issuer's live alias is not in AliasArns")
	}
}

// The issuer's role invokes the aliases it calls through and nothing else of the
// other functions: the exact list, each qualified, never the function itself.
func TestTheIssuerCanInvokeOnlyTheAliasesItNeeds(t *testing.T) {
	rec, _ := mustLambda(t, v5Estate())
	var invokes []string
	for _, s := range statements(t, prop(rec.one(t, policyType, "staging-issuer-policy"), "policy").StringValue()) {
		if slices.Contains(strs(s["Action"]), "lambda:InvokeFunction") {
			invokes = append(invokes, strs(s["Resource"])...)
		}
	}
	want := []string{
		fnArn("sluis-staging-issuer") + ":live",
		fnArn("sluis-staging-cloudflare") + ":live-issuer", fnArn("sluis-staging-github") + ":live-issuer",
		fnArn("sluis-staging-google") + ":live-issuer",
		fnArn("sluis-staging-slack") + ":live-issuer", fnArn("sluis-staging-backup") + ":live-console",
	}
	slices.Sort(invokes)
	slices.Sort(want)
	if !slices.Equal(invokes, want) {
		t.Errorf("the issuer may invoke\n%v\nwant\n%v", invokes, want)
	}
	// No module function's role invokes anything.
	for _, m := range moduleRoles {
		for _, s := range statements(t, prop(rec.one(t, policyType, "staging-"+m+"-policy"), "policy").StringValue()) {
			if slices.Contains(strs(s["Action"]), "lambda:InvokeFunction") {
				t.Errorf("%s may invoke %v", m, s["Resource"])
			}
		}
	}
}

// Every module role admits the audit queue's records only if the queue names it.
func TestTheAuditQueueNamesTheModuleRolesToo(t *testing.T) {
	rec, _ := mustLambda(t, v5Estate())
	for _, m := range moduleRoles {
		var sends bool
		for _, s := range statements(t, prop(rec.one(t, policyType, "staging-"+m+"-policy"), "policy").StringValue()) {
			sends = sends || slices.Contains(strs(s["Action"]), "sqs:SendMessage")
		}
		if !sends {
			t.Errorf("%s cannot send to the audit queue", m)
		}
	}
}

// The github function serves the mint too (D-v176-7 a): no `github-mint`
// function, role, policy, alias, log group or check exists, and the issuer's
// invoke grant on the github function is the `live-issuer` alias.
func TestThereIsNoGitHubMintResource(t *testing.T) {
	rec, _ := mustLambda(t, v5Estate())
	for _, n := range rec.names() {
		if strings.Contains(n, "github-mint") {
			t.Errorf("a github-mint resource is declared: %s", n)
		}
	}
	issuer := resourcesOf(statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()))
	if !slices.Contains(issuer, fnArn("sluis-staging-github")+":live-issuer") {
		t.Errorf("the issuer cannot mint through the github function's live-issuer alias: %v", issuer)
	}
	for _, r := range issuer {
		if strings.Contains(r, "github-mint") {
			t.Errorf("the issuer's policy names a github-mint resource: %s", r)
		}
	}
}
