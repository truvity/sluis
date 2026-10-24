package sluispulumi_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluisconfig "github.com/truvity/sluis/config"
	arp "github.com/truvity/sluis/deploy/pulumi"
)

// withOutline declares the Outline module (`controllers.outline`) in the
// installation, which gives it a function of its own.
func withOutline(in *sluisconfig.Installation) {
	c := sluisconfig.Controllers{}
	if in.Controllers != nil {
		c = *in.Controllers
	}
	c.Outline = &sluisconfig.OutlineController{}
	in.Controllers = &c
}

// outlineDocument is an Outline module document an estate writes itself
// (ModuleArgs.Document), naming the secrets key of the tests.
var outlineDocument = "apiVersion: sluis.truvity.github.io/sluis-outline/v1\nsecrets:\n  kmsKeyId: " + paramKey + "\n"

// withOutlineDocument makes the Outline function of an estate without an
// installation: its document is the estate's.
func withOutlineDocument(a *arp.LambdaArgs) {
	if a.Modules == nil {
		a.Modules = map[string]arp.ModuleArgs{}
	}
	m := a.Modules[arp.RoleOutline]
	m.Document = outlineDocument
	a.Modules[arp.RoleOutline] = m
}

// outlineOutputs builds the estate and returns the outputs keyed by module.
func outlineOutputs(t *testing.T, e estate) (*recorder, map[string]string, map[string]string, map[string]map[string]string) {
	t.Helper()
	args := e.args(t)
	rec, got, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		l, err := arp.NewLambda(ctx, "staging", args)
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
	var fns, roles map[string]string
	var aliases map[string]map[string]string
	for k, v := range map[string]any{"fns": &fns, "roles": &roles, "aliases": &aliases} {
		if err := json.Unmarshal([]byte(got[k]), v); err != nil {
			t.Fatal(err)
		}
	}
	return rec, fns, roles, aliases
}

// The Outline function is made for an installation that declares
// `controllers.outline`, from its own zip and with its own document, and the
// issuer's document calls it. An installation without it renders no Outline
// document and gets no Outline function, role, log group or output; nor does an
// estate without an installation, unless it writes the module's document.
func TestTheOutlineFunctionIsMadeOnlyForControllersOutline(t *testing.T) {
	without := func(t *testing.T, e estate) {
		t.Helper()
		rec, fns, roles, aliases := outlineOutputs(t, e)
		for _, typ := range []string{fnType, "aws:iam/role:Role", policyType, layerType} {
			for _, d := range rec.ofType(typ) {
				if strings.Contains(d.Name, "outline") {
					t.Errorf("%s %s is declared without the Outline module", typ, d.Name)
				}
			}
		}
		if _, ok := fns["outline"]; ok {
			t.Errorf("Functions holds outline: %v", fns)
		}
		if _, ok := roles["outline"]; ok {
			t.Errorf("RoleArns holds outline: %v", roles)
		}
		if _, ok := aliases["outline"]; ok {
			t.Errorf("AliasArns holds outline: %v", aliases)
		}
	}
	t.Run("an installation without controllers.outline", func(t *testing.T) {
		e := withInstallation(exampleInstallation(t), nil)
		without(t, e)
		rec, _ := mustLambda(t, e)
		if got := remoteOf(issuerDocument(t, rec), "outline"); got != "" {
			t.Errorf("the issuer's document calls an Outline function %q", got)
		}
	})
	t.Run("no installation", func(t *testing.T) { without(t, estateWith()) })

	t.Run("controllers.outline", func(t *testing.T) {
		in := exampleInstallation(t)
		withOutline(in)
		e := withInstallation(in, nil)
		e.pkg = zipFile(t, nil)
		rec, fns, roles, aliases := outlineOutputs(t, e)
		f := rec.one(t, fnType, "staging-outline-fn")
		if prop(f, "name").StringValue() != "sluis-example-outline" {
			t.Errorf("the Outline function: %v", f.Inputs)
		}
		// The release's own zip, beside the issuer's.
		sib := filepath.Join(filepath.Dir(e.pkg), "sluis-outline_1.63.0_linux_arm64.zip")
		if p := packagePath(t, f); !strings.Contains(p, sha(t, sib)) {
			t.Errorf("the Outline function runs %s, not the sluis-outline zip", p)
		}
		files := archiveFiles(t, rec.one(t, layerType, "staging-outline-config"))
		if !strings.Contains(files["sluis/sluis.yaml"], "apiVersion: sluis.truvity.github.io/sluis-outline/v1") {
			t.Errorf("the Outline function's document:\n%s", files["sluis/sluis.yaml"])
		}
		if !strings.Contains(files["sluis/sluis.yaml"], "outline: sluis-example-outline") {
			t.Errorf("the Outline function's document does not name its table:\n%s", files["sluis/sluis.yaml"])
		}
		if _, ok := files["sluis/policy.yaml"]; !ok {
			t.Error("the Outline function decides by the policy document, which its layer lacks")
		}
		for _, alias := range []string{"live", "live-issuer", "live-console"} {
			if !rec.has("aws:lambda/alias:Alias", "staging-outline-alias-"+alias) {
				t.Errorf("the Outline function has no alias %s", alias)
			}
			if aliases["outline"][alias] == "" {
				t.Errorf("AliasArns[outline] lacks %s: %v", alias, aliases["outline"])
			}
		}
		if fns["outline"] == "" || roles["outline"] == "" {
			t.Errorf("Functions %v, RoleArns %v: no outline", fns, roles)
		}
		if got := remoteOf(issuerDocument(t, rec), "outline"); got != "sluis-example-outline" {
			t.Errorf("the issuer's document calls the Outline function by %q", got)
		}
	})

	t.Run("a document of the estate's", func(t *testing.T) {
		rec, _ := mustLambda(t, estateWith(withOutlineDocument))
		if prop(rec.one(t, fnType, "staging-outline-fn"), "name").StringValue() != "sluis-staging-outline" {
			t.Error("the Outline function is not named sluis-<instance>-outline")
		}
	})
}

// The Outline role holds its own module, as the Slack role does: its table, its
// internal/outline and external/outline parameters, its outline/ blobs, the
// audit sink and the web identity token for the console's audience, and no
// other module's state and no function to invoke. The issuer holds nothing of
// the Outline module but the invoke of its live-console alias.
func TestTheOutlineRoleHoldsItsModuleAndTheIssuerNone(t *testing.T) {
	in := exampleInstallation(t)
	withOutline(in)
	rec, _ := mustLambda(t, withInstallation(in, nil))
	policy := func(role string) []map[string]any {
		return statements(t, prop(rec.one(t, policyType, "staging-"+role+"-policy"), "policy").StringValue())
	}
	table := tableArn("sluis-staging-outline")
	params := arnp + "ssm:" + region + ":" + account + ":parameter/sluis/example/"
	blobs := arnp + "s3:::" + bucket + "/outline/*"

	own := grants(policy("outline"))
	for action, res := range map[string]string{
		"dynamodb:PutItem": table, "dynamodb:Query": table, "dynamodb:DescribeTable": table,
		"ssm:GetParameter": params + "internal/outline/*", "ssm:PutParameter": params + "external/outline/*",
		"ssm:GetParametersByPath": params + "internal/outline",
		"s3:GetObject":            blobs, "s3:PutObject": blobs,
		"sqs:SendMessage":         arnp + "sqs:" + region + ":" + account + ":audit-ingest",
		"sts:GetWebIdentityToken": "*",
	} {
		if !slices.Contains(own[action], res) {
			t.Errorf("the Outline role may not %s %s: %v", action, res, own[action])
		}
	}
	if inv := own["lambda:InvokeFunction"]; len(inv) != 0 {
		t.Errorf("the Outline role invokes %v: it has no run-now of its own", inv)
	}
	for action, res := range own {
		for _, r := range res {
			for _, other := range []string{"oidc", "github", "slack", "google", "cloudflare", "backup"} {
				if strings.Contains(r, "sluis-staging-"+other) || strings.Contains(r, "/"+other+"/") || strings.HasSuffix(r, "/"+other) {
					t.Errorf("the Outline role may %s %s, the %s module's", action, r, other)
				}
			}
		}
	}

	var invokes []string
	for action, res := range grants(policy("issuer")) {
		for _, r := range res {
			if !strings.Contains(r, "outline") {
				continue
			}
			if action != "lambda:InvokeFunction" {
				t.Errorf("the issuer may %s %s", action, r)
				continue
			}
			invokes = append(invokes, r)
		}
	}
	if want := []string{fnArn("sluis-example-outline") + ":live-console"}; !slices.Equal(invokes, want) {
		t.Errorf("the issuer invokes %v of the Outline function, want %v", invokes, want)
	}
}

// The Outline table is needed only by an installation that makes the Outline
// function: a State grant without it serves one that does not, and is refused
// beside `controllers.outline`.
func TestTheOutlineTableIsNeededOnlyWithTheOutlineFunction(t *testing.T) {
	noOutlineTable := func(a *arp.LambdaArgs) {
		tables := moduleTables()
		delete(tables, arp.ModuleOutline)
		a.State = &arp.StateGrant{Tables: tables}
	}
	if _, _, err := buildLambda(t, withInstallation(exampleInstallation(t), noOutlineTable)); err != nil {
		t.Errorf("an installation without the Outline module needs its table: %v", err)
	}
	in := exampleInstallation(t)
	withOutline(in)
	if _, _, err := buildLambda(t, withInstallation(in, noOutlineTable)); err == nil || !strings.Contains(err.Error(), "outline") {
		t.Errorf("error %v, want the missing Outline table named", err)
	}
}
