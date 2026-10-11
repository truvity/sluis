package sluispulumi_test

import (
	"slices"
	"sort"
	"strings"
	"testing"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// oldResource is a resource of the stack as v1.75 left it: its type, its
// logical name and the physical name the provider knows it by.
type oldResource struct{ typ, logical, physical string }

// replacedOnName are the types whose physical name cannot change in place: a new
// name is a replacement. The event invoke config is keyed on its function.
var replacedOnName = map[string]bool{
	"aws:lambda/function:Function":                                   true,
	"aws:iam/role:Role":                                              true,
	"aws:iam/rolePolicy:RolePolicy":                                  true,
	"aws:cloudwatch/logGroup:LogGroup":                               true,
	"aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig": true,
}

// previewOf is what `pulumi preview` shows of the program against the old state:
// a resource found by its logical name or an alias is the same resource (and a
// replacement if its physical name moved), one not found is a create, and an old
// one nothing claims is a delete.
func previewOf(t *testing.T, rec *recorder, old []oldResource) (replaced, created, deleted []string) {
	t.Helper()
	claimed := map[int]bool{}
	for _, d := range rec.allDeclared() {
		names := append([]string{d.Name}, rec.optionsOf(d.Type, d.Name).AliasNames...)
		found := -1
		for i, o := range old {
			if o.typ == d.Type && slices.Contains(names, o.logical) {
				found = i
				break
			}
		}
		key := d.Type + "/" + d.Name
		if found < 0 {
			created = append(created, key)
			continue
		}
		claimed[found] = true
		if !replacedOnName[d.Type] {
			continue
		}
		if physicalOf(d) != old[found].physical {
			replaced = append(replaced, key)
			if rec.optionsOf(d.Type, d.Name).DeleteBeforeReplace {
				t.Errorf("%s asks for delete-before-replace: the new one must exist before the old is deleted", key)
			}
		}
	}
	for i, o := range old {
		if !claimed[i] {
			deleted = append(deleted, o.typ+"/"+o.logical)
		}
	}
	sort.Strings(replaced)
	sort.Strings(created)
	sort.Strings(deleted)
	return
}

// physicalOf is the name the provider knows a resource by.
func physicalOf(d declared) string {
	key := "name"
	if d.Type == "aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig" {
		key = "functionName"
	}
	if v := prop(d, key); v.IsString() {
		return v.StringValue()
	}
	return d.Name
}

// v175Issuer is the issuer's resources in the state of v1.75: the logical names
// `<name>-http*` under the physical name the estate had.
func v175Issuer(physical string) []oldResource {
	return []oldResource{
		{"aws:lambda/function:Function", "staging-http", physical},
		{"aws:iam/role:Role", "staging-http-role", physical},
		{"aws:iam/rolePolicy:RolePolicy", "staging-http-policy", physical},
		{"aws:cloudwatch/logGroup:LogGroup", "staging-http", "/aws/lambda/" + physical},
		{"aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig", "staging-http", physical},
	}
}

// oldStateOf is the state of an estate on v1.75: every resource the new program
// declares that v1.75 had (the issuer's under their old logical names and the
// physical name the estate had), and none of the module functions.
func oldStateOf(rec *recorder, physical string) []oldResource {
	issuer := map[string]bool{}
	for _, o := range v175Issuer(physical) {
		issuer[o.typ+"/"+strings.Replace(o.logical, "-http", "-issuer", 1)] = true
	}
	moduleOnly := func(name string) bool {
		for _, m := range moduleRoles {
			if strings.HasPrefix(name, "staging-"+m+"-") {
				return true
			}
		}
		return false
	}
	old := v175Issuer(physical)
	for _, d := range rec.allDeclared() {
		if issuer[d.Type+"/"+d.Name] || moduleOnly(d.Name) {
			continue
		}
		old = append(old, oldResource{d.Type, d.Name, physicalOf(d)})
	}
	return old
}

// The issuer's logical names move with an alias from the name each had, so the
// resources are the same ones, and the rename is a replacement of the function,
// its role, its role policy, its log group and its async config: nothing else
// of the stack changes, and nothing is deleted before its replacement exists.
func TestTheIssuerRenameIsAliasedReplacementsOnly(t *testing.T) {
	for _, old := range []string{"sluis", "sluis-http"} {
		t.Run(old, func(t *testing.T) {
			rec, _ := mustLambda(t, v5Estate())
			replaced, created, deleted := previewOf(t, rec, oldStateOf(rec, old))
			want := []string{
				"aws:cloudwatch/logGroup:LogGroup/staging-issuer",
				"aws:iam/role:Role/staging-issuer-role",
				"aws:iam/rolePolicy:RolePolicy/staging-issuer-policy",
				"aws:lambda/function:Function/staging-issuer",
				"aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig/staging-issuer",
			}
			if !slices.Equal(replaced, want) {
				t.Errorf("replaced %v, want %v", replaced, want)
			}
			if len(deleted) != 0 {
				t.Errorf("deleted %v: an alias is missing", deleted)
			}
			for _, c := range created {
				// What v1.76 adds is the module functions' and their schedules' and checks'.
				for _, stateful := range []string{"dynamodb", "ssm", "kms", "s3/bucket:"} {
					if strings.HasPrefix(c, "aws:"+stateful) {
						t.Errorf("%s is created: the data layer changes", c)
					}
				}
			}
			for _, r := range replaced {
				for _, stateful := range []string{"dynamodb", "ssm", "kms", "s3"} {
					if strings.HasPrefix(r, "aws:"+stateful) {
						t.Errorf("%s is replaced: the data layer changes", r)
					}
				}
			}
		})
	}
}

// An estate that keeps the name it has (FunctionName) is not replaced: the
// aliases carry its resources to the new logical names and nothing else moves.
func TestAnEstateThatKeepsItsFunctionNameReplacesNothing(t *testing.T) {
	rec, _ := mustLambda(t, v5Estate(func(a *arp.LambdaArgs) { a.FunctionName = "sluis" }))
	replaced, _, deleted := previewOf(t, rec, oldStateOf(rec, "sluis"))
	if len(replaced) != 0 || len(deleted) != 0 {
		t.Errorf("replaced %v, deleted %v", replaced, deleted)
	}
}

// Every logical name the issuer's resources had is aliased, and the old log
// group survives the replacement (RetainOnDelete), so nothing is delete-before.
func TestTheIssuerResourcesAnswerToTheirOldLogicalNames(t *testing.T) {
	rec, _ := mustLambda(t, v5Estate())
	for typ, names := range map[string][2]string{
		"aws:lambda/function:Function":                                   {"staging-issuer", "staging-http"},
		"aws:iam/role:Role":                                              {"staging-issuer-role", "staging-http-role"},
		"aws:iam/rolePolicy:RolePolicy":                                  {"staging-issuer-policy", "staging-http-policy"},
		"aws:cloudwatch/logGroup:LogGroup":                               {"staging-issuer", "staging-http"},
		"aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig": {"staging-issuer", "staging-http"},
	} {
		o := rec.optionsOf(typ, names[0])
		if !slices.Equal(o.AliasNames, []string{names[1]}) {
			t.Errorf("%s/%s aliases %v, want [%s]", typ, names[0], o.AliasNames, names[1])
		}
		if o.DeleteBeforeReplace {
			t.Errorf("%s/%s is delete-before-replace", typ, names[0])
		}
	}
	if !rec.optionsOf("aws:cloudwatch/logGroup:LogGroup", "staging-issuer").RetainOnDelete {
		t.Error("the issuer's log group is deleted with its resource: the old events go with it")
	}
}

// The golden names: every function, role, policy, log group and alias is
// `sluis-<instance>-<module>` by default, and an estate overrides any of them
// through the installation's `aws.functions`.
func TestEveryFunctionIsNamedForItsInstanceAndModule(t *testing.T) {
	rec, _ := mustLambda(t, v5Estate())
	got := map[string]string{}
	for _, f := range rec.ofType(fnType) {
		got[f.Name] = prop(f, "name").StringValue()
	}
	want := map[string]string{"staging-issuer": "sluis-staging-issuer"}
	for _, m := range moduleRoles {
		want["staging-"+m+"-fn"] = "sluis-staging-" + m
	}
	if len(got) != len(want) {
		t.Errorf("functions %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s is named %q, want %q", k, got[k], v)
		}
	}
	if n := prop(rec.one(t, "aws:iam/role:Role", "staging-issuer-role"), "name").StringValue(); n != "sluis-staging-issuer" {
		t.Errorf("the issuer's role is %q", n)
	}
	if n := prop(rec.one(t, "aws:cloudwatch/logGroup:LogGroup", "staging-issuer"), "name").StringValue(); n != "/aws/lambda/sluis-staging-issuer" {
		t.Errorf("the issuer's log group is %q", n)
	}
	for _, m := range moduleRoles {
		if n := prop(rec.one(t, "aws:cloudwatch/logGroup:LogGroup", "staging-"+m+"-logs"), "name").StringValue(); n != "/aws/lambda/sluis-staging-"+m {
			t.Errorf("%s: log group %q", m, n)
		}
	}
}

// An installation's `aws.functions` names the issuer and the modules, and the
// library holds the documents to the same names.
func TestAnInstallationNamesTheIssuerAndTheModulesThroughFunctions(t *testing.T) {
	in := exampleInstallation(t)
	in.AWS.FunctionName, in.AWS.Table = "", ""
	in.AWS.Functions = map[string]string{"issuer": "acme-front", "slack": "acme-chat"}
	e := withInstallation(in, nil)
	install, layout := e.mutate, v5Estate().mutate
	e.mutate = func(a *arp.LambdaArgs) { install(a); layout(a) }
	rec, _ := mustLambda(t, e)
	if n := prop(rec.one(t, fnType, "staging-issuer"), "name").StringValue(); n != "acme-front" {
		t.Errorf("the issuer is %q", n)
	}
	if n := prop(rec.one(t, fnType, "staging-slack-fn"), "name").StringValue(); n != "acme-chat" {
		t.Errorf("slack is %q", n)
	}
	if n := prop(rec.one(t, fnType, "staging-github-fn"), "name").StringValue(); n != "sluis-example-github" {
		t.Errorf("github is %q", n)
	}
	if !strings.Contains(layerFiles(t, rec)["sluis/sluis.yaml"], "acme-chat") {
		t.Errorf("the issuer's document does not call the module by its name:\n%s", layerFiles(t, rec)["sluis/sluis.yaml"])
	}
}
