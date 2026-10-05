package sluispulumi_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluisconfig "github.com/truvity/sluis/config"
	arp "github.com/truvity/sluis/deploy/pulumi"
)

// hiveInstallation is the Lambda-shaped installation of the root module's
// fixtures, with the test account in place of its placeholder.
func hiveInstallation(t *testing.T) *sluisconfig.Installation {
	t.Helper()
	in, err := sluisconfig.LoadInstallation(filepath.Join("..", "..", "config", "testdata", "hive.installation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	in.AWS.Account = account
	return in
}

// withInstallation is the estate of the other tests, given an installation
// instead of the two documents.
func withInstallation(in *sluisconfig.Installation, more func(*arp.LambdaArgs)) estate {
	return estate{mutate: func(a *arp.LambdaArgs) {
		a.Config, a.Policy, a.Installation = "", "", in
		a.Instance, a.FunctionName = "", ""
		a.WrappedSigning = &arp.WrappedSigningArgs{KeyArn: pulumi.String(arnp + "kms:" + region + ":" + account + ":key/wrapped")}
		if more != nil {
			more(a)
		}
	}}
}

// An installation is rendered by the root module's renderer and held by the
// layer as it is: the library adds nothing the renderer did not write, so what
// `sluisctl render` shows an estate is what the function reads.
func TestAnInstallationIsRenderedIntoTheLayerByTheRootRenderer(t *testing.T) {
	in := hiveInstallation(t)
	service, policy, err := sluisconfig.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := mustLambda(t, withInstallation(in, nil))
	files := layerFiles(t, rec)
	if files["sluis/sluis.yaml"] != string(service) {
		t.Errorf("the service document is not the renderer's:\n%s\n--- want ---\n%s", files["sluis/sluis.yaml"], service)
	}
	if files["sluis/policy.yaml"] != string(policy) {
		t.Errorf("the policy document is not the renderer's:\n%s", files["sluis/policy.yaml"])
	}
	// The function is the installation's: its name is what the trigger invokes.
	if !strings.Contains(files["sluis/sluis.yaml"], "github: sluis-http") {
		t.Errorf("the trigger does not invoke the installation's function:\n%s", files["sluis/sluis.yaml"])
	}
	f := rec.one(t, fnType, "kernel-http")
	if prop(f, "name").StringValue() != "sluis-http" {
		t.Errorf("the function is named %v, the installation's is sluis-http", prop(f, "name"))
	}
}

// What the arguments and the installation both say is said once: a disagreement
// is refused naming the argument, and the documents are never both given.
func TestAnInstallationAndTheArgumentsAgreeOrAreRefused(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*sluisconfig.Installation, *arp.LambdaArgs)
		want   string
	}{
		"another instance":      {func(_ *sluisconfig.Installation, a *arp.LambdaArgs) { a.Instance = "other" }, "Instance"},
		"another region":        {func(_ *sluisconfig.Installation, a *arp.LambdaArgs) { a.Region = "eu-west-1" }, "Region"},
		"another account":       {func(_ *sluisconfig.Installation, a *arp.LambdaArgs) { a.AccountID = strings.Repeat("2", 12) }, "AccountID"},
		"another function name": {func(_ *sluisconfig.Installation, a *arp.LambdaArgs) { a.FunctionName = "other" }, "FunctionName"},
		"another shape":         {func(in *sluisconfig.Installation, _ *arp.LambdaArgs) { in.Shape = sluisconfig.ShapeKubernetes }, "Shape"},
		"the documents too": {func(_ *sluisconfig.Installation, a *arp.LambdaArgs) {
			a.Config = "issuerURL: https://x.example\n"
		}, "replaces Config"},
		"an installation the renderer refuses": {func(in *sluisconfig.Installation, _ *arp.LambdaArgs) {
			in.Controllers.GitHub.EnabledOrgs = []string{"unbound"}
		}, "unbound"},
	} {
		t.Run(name, func(t *testing.T) {
			in := hiveInstallation(t)
			_, _, err := buildLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { c.change(in, a) }))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want one naming %q", err, c.want)
			}
		})
	}
}

// The library says what it takes from the arguments when the installation
// leaves it out: the instance, the region, the account and the function name.
func TestAnInstallationTakesWhatItLeavesOutFromTheArguments(t *testing.T) {
	in := hiveInstallation(t)
	in.Instance, in.AWS.Region, in.AWS.Account, in.AWS.FunctionName = "", "", "", ""
	rec, _ := mustLambda(t, withInstallation(in, func(a *arp.LambdaArgs) {
		a.Instance, a.FunctionName = "kernel", "sluis-fn"
	}))
	files := layerFiles(t, rec)
	for _, want := range []string{"root: /sluis/kernel", "region: " + region, "github: sluis-fn"} {
		if !strings.Contains(files["sluis/sluis.yaml"], want) {
			t.Errorf("the service document lacks %q:\n%s", want, files["sluis/sluis.yaml"])
		}
	}
}

// Recovery is one fact: the argument completes the installation, and a
// disagreement is refused.
func TestRecoveryIsSaidOnceAcrossTheArgumentsAndTheInstallation(t *testing.T) {
	yes := true
	in := hiveInstallation(t) // recovery.enabled: false
	if _, _, err := buildLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: &yes} })); err == nil ||
		!strings.Contains(err.Error(), "Recovery.Enabled") {
		t.Errorf("a disagreement about recovery: %v", err)
	}
	in = hiveInstallation(t)
	in.Recovery = nil
	rec, _ := mustLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: &yes} }))
	if got := layerFiles(t, rec)["sluis/sluis.yaml"]; !strings.Contains(got, "enabled: true") {
		t.Errorf("recovery was not enabled from the arguments:\n%s", got)
	}
}

// The library is a consumer of the root module's public surface only. An
// import of its `internal/` would compile while the module is checked out
// beside it (the path prefix permits it) and be a different, older package for
// a consumer on an earlier release: the version trap a public package ends.
func TestTheLibraryImportsNothingInternal(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no sources: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			if path := strings.Trim(imp.Path.Value, `"`); strings.Contains(path, "github.com/truvity/sluis/internal/") {
				t.Errorf("%s imports %s: use the public config package", f, path)
			}
		}
	}
}
