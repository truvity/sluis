package sluispulumi_test

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"

	sluisconfig "github.com/truvity/sluis/config"
	arp "github.com/truvity/sluis/deploy/pulumi"
)

// moduleInstallation is one of the installations the per-module documents are
// golden-rendered from (config/testdata/modules-*.installation.yaml), with the
// test account in place of its placeholder.
func moduleInstallation(t *testing.T, name string) *sluisconfig.Installation {
	t.Helper()
	in, err := sluisconfig.LoadInstallation(filepath.Join("..", "..", "config", "testdata", name+".installation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	in.AWS.Account = account
	in.AWS.AuditQueueURL = ""
	return in
}

// moduleEstate is a layout v5 estate with an audit queue the library makes.
// queueLine is the audit queue a document names: the one the library installs
// in the test, not the fixture's.
var queueLine = regexp.MustCompile(`queueURL: \S+`)

func moduleEstate(t *testing.T, in *sluisconfig.Installation, more ...func(*arp.LambdaArgs)) estate {
	t.Helper()
	return withInstallation(in, func(a *arp.LambdaArgs) {
		a.AuditQueueArn = nil
		a.Audit = installAudit(t)
		withLayout(arp.LayoutV5).mutate(a)
		for _, f := range more {
			f(a)
		}
	})
}

func issuerDocument(t *testing.T, rec *recorder) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(layerFiles(t, rec)["sluis/sluis.yaml"]), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// remoteOf is the function a document calls a module by, or "".
func remoteOf(doc map[string]any, module string) string {
	sec, _ := doc[module].(map[string]any)
	remote, _ := sec["remote"].(map[string]any)
	fn, _ := remote["function"].(string)
	return fn
}

// A module function reads the document the renderer wrote for it: the
// service document, and the policy document where it decides by one. The
// golden files of config/testdata hold what the renderer writes.
func TestEachModuleFunctionReadsItsRenderedDocuments(t *testing.T) {
	for _, name := range []string{"modules-small", "modules-full"} {
		t.Run(name, func(t *testing.T) {
			in := moduleInstallation(t, name)
			rec, _ := mustLambda(t, moduleEstate(t, in))
			golden := func(module string) string {
				raw, err := os.ReadFile(filepath.Join("..", "..", "config", "testdata", name+"."+module+".yaml"))
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			policy := golden("policy")
			for _, m := range sluisconfig.Modules() {
				made := rec.has(fnType, "staging-"+m+"-fn")
				if _, rendered := os.Stat(filepath.Join("..", "..", "config", "testdata", name+"."+m+".yaml")); (rendered == nil) != made {
					t.Errorf("%s: function made = %v, document rendered = %v", m, made, rendered == nil)
				}
				if !made {
					continue
				}
				files := archiveFiles(t, rec.one(t, layerType, "staging-"+m+"-config"))
				want := golden(m)
				if got := queueLine.ReplaceAllString(files["sluis/sluis.yaml"], "queueURL: Q"); got != queueLine.ReplaceAllString(want, "queueURL: Q") {
					t.Errorf("%s: the service document is not the renderer's:\n%s\n--- want ---\n%s", m, files["sluis/sluis.yaml"], want)
				}
				p, hasPolicy := files["sluis/policy.yaml"]
				if hasPolicy != (m != "google") || (hasPolicy && p != policy) {
					t.Errorf("%s: policy document present = %v (the Google module reads none; the rest read the issuer's)", m, hasPolicy)
				}
			}
			// The issuer's document calls them.
			issuer := issuerDocument(t, rec)
			for _, m := range []string{"github", "slack", "google"} {
				if got := remoteOf(issuer, m); got != sluisconfig.FunctionNameOf(in, m) {
					t.Errorf("the issuer's document calls %s by %q, want %s", m, got, sluisconfig.FunctionNameOf(in, m))
				}
			}
		})
	}
}

// The functions are named by the installation (`aws.functions`) or by the
// arguments, and the one name is the function's, its role's and the issuer's
// call.
func TestModuleFunctionNamesAreSaidOnce(t *testing.T) {
	in := moduleInstallation(t, "modules-full")
	rec, _ := mustLambda(t, moduleEstate(t, in))
	for m, want := range map[string]string{"github": "sluis-staging-github", "slack": "sluis-staging-chat"} {
		if got := prop(rec.one(t, fnType, "staging-"+m+"-fn"), "name").StringValue(); got != want {
			t.Errorf("%s is named %q, want %q", m, got, want)
		}
		if got := prop(rec.one(t, "aws:iam/role:Role", "staging-"+m+"-role"), "name").StringValue(); got != want {
			t.Errorf("%s: its role is named %q, want %q", m, got, want)
		}
	}
	// The arguments name a function the installation does not: its document calls it.
	small := moduleInstallation(t, "modules-small")
	rec, _ = mustLambda(t, moduleEstate(t, small, func(a *arp.LambdaArgs) {
		a.Modules = map[string]arp.ModuleArgs{"google": {FunctionName: "acme-directory"}}
	}))
	if got := prop(rec.one(t, fnType, "staging-google-fn"), "name").StringValue(); got != "acme-directory" {
		t.Errorf("google is named %q", got)
	}
	if got := remoteOf(issuerDocument(t, rec), "google"); got != "acme-directory" {
		t.Errorf("the issuer calls google by %q", got)
	}
	// Both say it and they differ: refused, naming the argument.
	_, _, err := buildLambda(t, moduleEstate(t, moduleInstallation(t, "modules-full"), func(a *arp.LambdaArgs) {
		a.Modules = map[string]arp.ModuleArgs{"slack": {FunctionName: "other"}}
	}))
	if err == nil || !strings.Contains(err.Error(), "LambdaArgs.Modules[slack].FunctionName") {
		t.Errorf("a name said twice: %v", err)
	}
	// The same name twice is not a disagreement.
	mustLambda(t, moduleEstate(t, moduleInstallation(t, "modules-full"), func(a *arp.LambdaArgs) {
		a.Modules = map[string]arp.ModuleArgs{"slack": {FunctionName: "sluis-staging-chat"}}
	}))
}

// A module with nothing to do has no function: the Cloudflare module of an
// installation that declares no preset would fail its check at start.
func TestTheCloudflareFunctionExistsOnlyWithPresets(t *testing.T) {
	rec, _ := mustLambda(t, moduleEstate(t, moduleInstallation(t, "modules-small")))
	if rec.has(fnType, "staging-cloudflare-fn") {
		t.Error("the Cloudflare function is made with no preset")
	}
	qp := prop(rec.one(t, "aws:sqs/queuePolicy:QueuePolicy", "audit-example-ingest"), "policy").StringValue()
	if strings.Contains(qp, "-cloudflare") {
		t.Errorf("the audit queue names the role of a function that does not exist:\n%s", qp)
	}
	rec, _ = mustLambda(t, moduleEstate(t, moduleInstallation(t, "modules-full")))
	f := rec.one(t, fnType, "staging-cloudflare-fn")
	if prop(f, "name").StringValue() != "sluis-staging-cloudflare" {
		t.Errorf("cloudflare: %v", f.Inputs)
	}
	if doc := archiveFiles(t, rec.one(t, layerType, "staging-cloudflare-config"))["sluis/sluis.yaml"]; !strings.Contains(doc, "  serve: {}\n") {
		t.Errorf("the Cloudflare module's document does not serve:\n%s", doc)
	}
}

// What the functions declare is disjoint but for the blob store's credential,
// which the issuer and the Google module both read.
func TestTheSecretsEachFunctionDeclaresAreDisjointButForTheBlobCredential(t *testing.T) {
	in := moduleInstallation(t, "modules-full")
	in.AWS.Bucket = ""
	rec, _ := mustLambda(t, moduleEstate(t, in, func(a *arp.LambdaArgs) {
		ext := r2()
		ext.CredentialsRef = "internal/google/blobs-r2"
		a.Storage = &arp.StorageGrant{External: ext}
	}))
	owners := map[string][]string{}
	policy := layerFiles(t, rec)["sluis/policy.yaml"]
	collect := func(module, doc string) {
		secrets, err := sluisconfig.ModuleSecrets(module, []byte(doc), []byte(policy))
		if err != nil {
			t.Fatalf("%s: %v", module, err)
		}
		for _, s := range secrets {
			key := s.Kind + "/" + s.Subject + "/" + s.Name
			owners[key] = append(owners[key], module)
		}
	}
	collect(sluisconfig.ModuleIssuer, layerFiles(t, rec)["sluis/sluis.yaml"])
	for _, m := range sluisconfig.Modules() {
		collect(m, archiveFiles(t, rec.one(t, layerType, "staging-"+m+"-config"))["sluis/sluis.yaml"])
	}
	shared := 0
	for _, key := range slices.Sorted(maps.Keys(owners)) {
		switch who := owners[key]; {
		case strings.HasPrefix(key, "blob-credentials/"):
			shared++
			slices.Sort(who)
			if !slices.Equal(who, []string{"google", "issuer"}) {
				t.Errorf("%s is declared by %v, want the issuer and the Google module", key, who)
			}
		case len(who) != 1:
			t.Errorf("%s is declared by %v", key, who)
		}
	}
	if shared != 1 {
		t.Errorf("%d blob credentials are declared, want 1: %v", shared, owners)
	}
}

// The rendered trigger starts the controller's own function, not the issuer.
func TestTheRunNowTriggerNamesTheControllerFunctions(t *testing.T) {
	rec, _ := mustLambda(t, moduleEstate(t, moduleInstallation(t, "modules-full")))
	trigger := issuerDocument(t, rec)["adapters"].(map[string]any)["trigger"].(map[string]any)["settings"].(map[string]any)
	if trigger["github"] != "sluis-staging-github" || trigger["slack"] != "sluis-staging-chat" {
		t.Errorf("the issuer's trigger: %v", trigger)
	}
}
