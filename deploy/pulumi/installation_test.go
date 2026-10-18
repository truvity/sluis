package sluispulumi_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluisconfig "github.com/truvity/sluis/config"
	arp "github.com/truvity/sluis/deploy/pulumi"
	"github.com/truvity/sluis/storage/keys"
)

// exampleInstallation is the Lambda-shaped installation of the root module's
// fixtures, with the test account in place of its placeholder.
func exampleInstallation(t *testing.T) *sluisconfig.Installation {
	t.Helper()
	in, err := sluisconfig.LoadInstallation(filepath.Join("..", "..", "config", "testdata", "example.installation.yaml"))
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
	in := exampleInstallation(t)
	in.Secrets = &sluisconfig.Secrets{KMSKeyID: paramKey} // the key is the installation's to say
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
	f := rec.one(t, fnType, "staging-issuer")
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
			in := exampleInstallation(t)
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
	in := exampleInstallation(t)
	in.Instance, in.AWS.Region, in.AWS.Account, in.AWS.FunctionName = "", "", "", ""
	rec, _ := mustLambda(t, withInstallation(in, func(a *arp.LambdaArgs) {
		a.Instance, a.FunctionName = "staging", "sluis-fn"
	}))
	files := layerFiles(t, rec)
	for _, want := range []string{"root: /sluis/staging", "region: " + region, "github: sluis-fn"} {
		if !strings.Contains(files["sluis/sluis.yaml"], want) {
			t.Errorf("the service document lacks %q:\n%s", want, files["sluis/sluis.yaml"])
		}
	}
}

// Recovery is one fact: the argument completes the installation, and a
// disagreement is refused.
func TestRecoveryIsSaidOnceAcrossTheArgumentsAndTheInstallation(t *testing.T) {
	yes := true
	in := exampleInstallation(t) // recovery.enabled: false
	if _, _, err := buildLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: &yes} })); err == nil ||
		!strings.Contains(err.Error(), "Recovery.Enabled") {
		t.Errorf("a disagreement about recovery: %v", err)
	}
	in = exampleInstallation(t)
	in.Recovery = nil
	rec, _ := mustLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { a.Recovery = &arp.RecoveryArgs{Enabled: &yes} }))
	if got := layerFiles(t, rec)["sluis/sluis.yaml"]; !strings.Contains(got, "enabled: true") {
		t.Errorf("recovery was not enabled from the arguments:\n%s", got)
	}
}

// An installation never leaves the web identity audience empty, which would
// mean any audience: it is the console's, and a different one is refused.
func TestTheWebIdentityAudienceIsTheConsolesWhenAnInstallationIsGiven(t *testing.T) {
	in := exampleInstallation(t)
	rec, _ := mustLambda(t, withInstallation(in, nil))
	if !strings.Contains(prop(rec.one(t, policyType, "staging-issuer-policy"), "policy").StringValue(), "https://access.example.test/console") {
		t.Error("the role's web identity grant does not carry the console audience")
	}
	if _, _, err := buildLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { a.WebIdentityAudience = "https://other.example.test" })); err == nil ||
		!strings.Contains(err.Error(), "WebIdentityAudience") {
		t.Errorf("a different audience: %v", err)
	}
	if _, _, err := buildLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { a.WebIdentityAudience = "https://access.example.test/console" })); err != nil {
		t.Errorf("the same audience: %v", err)
	}
}

// The library is a consumer of the root module's public surface only. An
// import of its `internal/` would compile while the module is checked out
// beside it (the path prefix permits it) and be a different, older package for
// a consumer on an earlier release: the version trap a public package ends.
func TestTheLibraryImportsNothingInternal(t *testing.T) {
	var files []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return err
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("no sources: %v", err)
	}
	for _, f := range files {
		// Test files count too: a test that reaches into internal/ is the
		// same trap when the module is consumed at another release.
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

// The estate supplies the keys: an installation whose kmsWrapped signer names
// no key renders the library's `keys.sign`, not the renderer's default, and is
// not refused for it. An installation that names another sign key is.
func TestAnInstallationTakesItsKeysFromTheLibrary(t *testing.T) {
	supplied := func(in *sluisconfig.Installation) estate {
		return withInstallation(in, func(a *arp.LambdaArgs) {
			a.WrappedSigning = nil
			a.Keys = &arp.KeysArgs{Sign: "alias/x"}
		})
	}
	in := exampleInstallation(t)
	in.Keys = nil
	in.SigningKey.KMSWrapped.KeyID = ""
	rec, _ := mustLambda(t, supplied(in))
	doc := layerFiles(t, rec)["sluis/sluis.yaml"]
	if !strings.Contains(doc, "sign: alias/x") || strings.Contains(doc, "alias/sluis-example-sign") {
		t.Errorf("the document does not carry the library's keys:\n%s", doc)
	}

	in = exampleInstallation(t)
	in.Keys.Keys["sign"] = keys.Entry{Key: "alias/other"}
	if _, _, err := buildLambda(t, supplied(in)); err == nil || !strings.Contains(err.Error(), "keys.sign") {
		t.Errorf("error %v, want one naming keys.sign", err)
	}
}

func withCloudflare(in *sluisconfig.Installation) {
	in.Cloudflare = &sluisconfig.Cloudflare{
		Cloudflare: sluisconfig.CloudflareSection{
			Accounts: map[string]sluisconfig.CloudflareAccount{"main": {ID: "0123456789abcdef0123456789abcdef", Minter: "internal/cloudflare/main/minter"}},
			Presets: map[string]sluisconfig.CloudflarePreset{"dns": {
				Account: "main", Prototype: "proto-dns-0001", Description: "DNS",
				Lifetime: sluisconfig.Duration(time.Hour), Rotation: sluisconfig.Duration(20 * time.Minute),
			}},
		},
		Grants: []sluisconfig.CloudflareGrant{{Group: "all:audit:security", Presets: []string{"dns"}}},
	}
}

// An R2 preset's endpoint is handed to clients, not called by the function, so
// a document carrying one renders without AllowEndpoints; an endpoint the
// function would call is refused beside it, and so is the same key anywhere
// else in the Cloudflare section.
func TestAnR2PresetEndpointIsNotAnEndpointOfTheFunction(t *testing.T) {
	const r2 = "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
	in := exampleInstallation(t)
	withCloudflare(in)
	in.Cloudflare.Presets["blobs"] = sluisconfig.CloudflarePreset{
		Account: "main", Prototype: "proto-r2-0001", Description: "R2", Endpoint: r2,
		Lifetime: sluisconfig.Duration(time.Hour), Rotation: sluisconfig.Duration(20 * time.Minute),
	}
	rec, _ := mustLambda(t, withInstallation(in, nil))
	if doc := layerFiles(t, rec)["sluis/sluis.yaml"]; !strings.Contains(doc, r2) {
		t.Errorf("the R2 endpoint is not in the document:\n%s", doc)
	}

	cf := "cloudflare: {accounts: {main: {id: 0123456789abcdef0123456789abcdef, minter: internal/cloudflare/main/minter}}, " +
		"presets: {blobs: {account: main, prototype: proto-r2-0001, description: R2, lifetime: 15m, rotation: 5m, endpoint: '" + r2 + "'}}}\n"
	if _, _, err := buildLambda(t, estate{config: "issuerURL: https://x.example\n" + cf}); err != nil {
		t.Errorf("a document with an R2 preset: %v", err)
	}
	for name, c := range map[string]struct{ config, at string }{
		"a dynamodb endpoint beside it": {
			config: "issuerURL: https://x.example\nports: {adapter: dynamodb, dynamodb: {table: t, endpoint: 'https://evil.example'}}\n" + cf,
			at:     "ports.dynamodb.endpoint",
		},
		"a secrets endpoint beside it": {
			config: "issuerURL: https://x.example\nsecrets: {endpoint: 'https://evil.example'}\n" + cf,
			at:     "secrets.endpoint",
		},
	} {
		_, _, err := buildLambda(t, estate{config: c.config})
		if err == nil || !strings.Contains(err.Error(), "("+c.at+")") {
			t.Errorf("%s: error %v, want the refusal naming %s", name, err, c.at)
		}
	}
}

// An installation that declares Cloudflare presets gets the rotation schedule
// and the function's grants on exactly the minter, the record of minted ids and
// the stored credentials; one that does not gets neither.
func TestCloudflarePresetsGetTheRotationScheduleAndTheGrants(t *testing.T) {
	in := exampleInstallation(t)
	withCloudflare(in)
	rec, _ := mustLambda(t, withInstallation(in, nil))

	s := rec.one(t, "aws:scheduler/schedule:Schedule", "staging-cloudflare-rotation")
	tgt := prop(s, "target").ObjectValue()
	if prop(s, "scheduleExpression").StringValue() != "rate(15 minutes)" || tgt["input"].StringValue() != `{"kind":"cloudflare"}` ||
		!strings.HasSuffix(tgt["arn"].StringValue(), ":function:sluis-http:live") {
		t.Errorf("rotation schedule: %v", s.Inputs)
	}
	root := arnp + "ssm:" + region + ":" + account + ":parameter/sluis/example"
	g := rolePolicy(t, rec)
	has := func(action, resource string) bool { return slices.Contains(g[action], resource) }
	if !has("ssm:GetParameter", root+"/internal/cloudflare/*") || has("ssm:PutParameter", root+"/internal/cloudflare/*") {
		t.Errorf("the minter credential must be readable and never writable: %v", g["ssm:GetParameter"])
	}
	if !has("ssm:PutParameter", root+"/internal/cloudflare-minted/*") || !has("ssm:GetParameter", root+"/internal/cloudflare-minted/*") {
		t.Errorf("the record of minted ids must be read and written: %v", g["ssm:PutParameter"])
	}
	if !has("ssm:PutParameter", root+"/external/cloudflare/*") {
		t.Errorf("the stored credentials must be writable: %v", g["ssm:PutParameter"])
	}
	for _, r := range g["ssm:PutParameter"] {
		if strings.HasSuffix(r, "/internal/*") || strings.HasSuffix(r, "/internal/config/*") || r == root+"/internal/cloudflare/*" {
			t.Errorf("a write grant too wide: %s", r)
		}
	}

	// Off when the installation declares none, and when the schedule is left out.
	rec, _ = mustLambda(t, withInstallation(exampleInstallation(t), nil))
	if rec.has("aws:scheduler/schedule:Schedule", "staging-cloudflare-rotation") {
		t.Error("a rotation schedule without presets")
	}
	if g = rolePolicy(t, rec); has("ssm:GetParameter", root+"/internal/cloudflare/*") {
		t.Error("a Cloudflare grant without presets")
	}
	rec, _ = mustLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { a.CloudflareRotation.Disabled = true }))
	if rec.has("aws:scheduler/schedule:Schedule", "staging-cloudflare-rotation") {
		t.Error("a disabled rotation schedule was made")
	}
	rec, _ = mustLambda(t, withInstallation(in, func(a *arp.LambdaArgs) {
		a.CloudflareRotation.Paused, a.CloudflareRotation.Rate = true, "rate(2 minutes)"
	}))
	s = rec.one(t, "aws:scheduler/schedule:Schedule", "staging-cloudflare-rotation")
	if prop(s, "state").StringValue() != "DISABLED" || prop(s, "scheduleExpression").StringValue() != "rate(2 minutes)" {
		t.Errorf("paused schedule: %v", s.Inputs)
	}
	if _, _, err := buildLambda(t, withInstallation(in, func(a *arp.LambdaArgs) {
		a.CloudflareRotation = arp.CloudflareRotationArgs{Disabled: true, Paused: true}
	})); err == nil {
		t.Error("disabled and paused together were accepted")
	}
}

func TestTheCloudflareTickIsConfigurableAndHeldToThePresets(t *testing.T) {
	in := exampleInstallation(t)
	withCloudflare(in) // lifetime 1h, rotation 20m
	expr := func(t *testing.T, more func(*arp.LambdaArgs)) string {
		t.Helper()
		rec, _ := mustLambda(t, withInstallation(in, more))
		return prop(rec.one(t, "aws:scheduler/schedule:Schedule", "staging-cloudflare-rotation"), "scheduleExpression").StringValue()
	}
	if got := expr(t, func(a *arp.LambdaArgs) { a.CloudflareRotation.Interval = 10 * time.Minute }); got != "rate(10 minutes)" {
		t.Errorf("Interval 10m: %s", got)
	}
	if got := expr(t, func(a *arp.LambdaArgs) { a.CloudflareRotation.Interval = time.Minute }); got != "rate(1 minute)" {
		t.Errorf("Interval 1m: %s", got)
	}
	// 20m + 35m + 5m = 60m is not under 1h; 20m + 34m + 5m is.
	for name, set := range map[string]func(*arp.LambdaArgs){
		"interval too long": func(a *arp.LambdaArgs) { a.CloudflareRotation.Interval = 35 * time.Minute },
		"rate too long":     func(a *arp.LambdaArgs) { a.CloudflareRotation.Rate = "rate(1 hour)" },
		"interval and rate": func(a *arp.LambdaArgs) {
			a.CloudflareRotation.Interval, a.CloudflareRotation.Rate = time.Minute, "rate(5 minutes)"
		},
		"interval under 1m":   func(a *arp.LambdaArgs) { a.CloudflareRotation.Interval = 30 * time.Second },
		"interval not minute": func(a *arp.LambdaArgs) { a.CloudflareRotation.Interval = 90 * time.Second },
	} {
		if _, _, err := buildLambda(t, withInstallation(in, set)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if got := expr(t, func(a *arp.LambdaArgs) { a.CloudflareRotation.Interval = 34 * time.Minute }); got != "rate(34 minutes)" {
		t.Errorf("Interval 34m: %s", got)
	}
	// A left-out schedule is not held to the margin.
	if _, _, err := buildLambda(t, withInstallation(in, func(a *arp.LambdaArgs) {
		a.CloudflareRotation = arp.CloudflareRotationArgs{Disabled: true, Interval: time.Hour}
	})); err != nil {
		t.Errorf("a disabled schedule was held to the margin: %v", err)
	}
}

// The console calls a credential overdue only past rotation + tick, so the
// documents carry the tick of the schedule that rotates.
func TestTheRotationTickReachesTheDocuments(t *testing.T) {
	in := exampleInstallation(t)
	withCloudflare(in)
	has := func(rec *recorder, want string) bool {
		for _, doc := range functionDocuments(t, rec) {
			if strings.Contains(doc, want) {
				return true
			}
		}
		return false
	}
	rec, _ := mustLambda(t, withInstallation(in, nil))
	if !has(rec, "tick: 15m") {
		t.Error("the default 15m tick is not in the documents")
	}
	rec, _ = mustLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { a.CloudflareRotation.Interval = 10 * time.Minute }))
	if !has(rec, "tick: 10m") {
		t.Error("Interval 10m is not in the documents")
	}
	if in.Cloudflare.Tick != 0 {
		t.Error("the caller's installation was changed")
	}
	// A tick the installation states is its own; a cron expression states none.
	in.Cloudflare.Tick = sluisconfig.Duration(7 * time.Minute)
	rec, _ = mustLambda(t, withInstallation(in, nil))
	if !has(rec, "tick: 7m") || has(rec, "tick: 15m") {
		t.Error("the installation's own tick was overwritten")
	}
	in.Cloudflare.Tick = 0
	rec, _ = mustLambda(t, withInstallation(in, func(a *arp.LambdaArgs) { a.CloudflareRotation.Rate = "cron(0/20 * * * ? *)" }))
	if has(rec, "tick:") {
		t.Error("a tick was invented for a cron expression")
	}
}

// A preset with `store: none` is minted on demand only: the library gives it no
// rotation schedule and no grant on external/cloudflare, and holds it to no
// rotation margin. Presets beside it that are stored keep all three.
func TestAnOnDemandPresetGetsNoScheduleNoStoredGrantAndNoMargin(t *testing.T) {
	root := arnp + "ssm:" + region + ":" + account + ":parameter/sluis/example"
	onDemand := sluisconfig.CloudflarePreset{
		Account: "main", Prototype: "proto-adhoc-01", Description: "On demand",
		// No rotation: it would not fit the margin below if it counted.
		Lifetime: sluisconfig.Duration(30 * time.Minute), Store: sluisconfig.CloudflareStoreNone,
	}

	only := exampleInstallation(t)
	withCloudflare(only)
	only.Cloudflare.Presets = map[string]sluisconfig.CloudflarePreset{"dns": onDemand}
	rec, _ := mustLambda(t, withInstallation(only, func(a *arp.LambdaArgs) { a.CloudflareRotation.Interval = 29 * time.Minute }))
	if rec.has("aws:scheduler/schedule:Schedule", "staging-cloudflare-rotation") {
		t.Error("a rotation schedule for presets that are all store: none")
	}
	g := rolePolicy(t, rec)
	if slices.Contains(g["ssm:PutParameter"], root+"/external/cloudflare/*") || slices.Contains(g["ssm:GetParameter"], root+"/external/cloudflare/*") {
		t.Errorf("a grant on the stored credentials with nothing stored: %v", g["ssm:PutParameter"])
	}
	// The minter credential and the record of minted ids are still needed: the
	// token exchange mints, and the sweep reads the record.
	if !slices.Contains(g["ssm:GetParameter"], root+"/internal/cloudflare/*") || !slices.Contains(g["ssm:PutParameter"], root+"/internal/cloudflare-minted/*") {
		t.Errorf("the minter grants are missing: %v", g["ssm:GetParameter"])
	}
	if doc := layerFiles(t, rec)["sluis/sluis.yaml"]; !strings.Contains(doc, "store: none") {
		t.Errorf("the document does not carry the store:\n%s", doc)
	}

	mixed := exampleInstallation(t)
	withCloudflare(mixed)
	mixed.Cloudflare.Presets["adhoc"] = onDemand
	rec, _ = mustLambda(t, withInstallation(mixed, nil))
	rec.one(t, "aws:scheduler/schedule:Schedule", "staging-cloudflare-rotation")
	if g = rolePolicy(t, rec); !slices.Contains(g["ssm:PutParameter"], root+"/external/cloudflare/*") {
		t.Errorf("the stored preset lost its grant: %v", g["ssm:PutParameter"])
	}
}

// External blobs can name a Cloudflare preset instead of a secret: the document
// gets ports.blob.s3.credentials.preset, the library declares no read of blob
// credentials, and the preset must be one of the installation's R2 presets,
// whether it is stored or on demand only.
func TestExternalBlobsCanMintTheirCredentialsFromAPreset(t *testing.T) {
	const r2url = "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com"
	blobs := func(preset string) func(*arp.LambdaArgs) {
		return func(a *arp.LambdaArgs) {
			a.Storage = &arp.StorageGrant{External: &arp.ExternalBlobs{Bucket: "acme-blobs", Endpoint: r2url, CredentialsPreset: preset}}
		}
	}
	for _, store := range []string{"", "store: none, "} {
		cf := "cloudflare: {accounts: {main: {id: 0123456789abcdef0123456789abcdef, minter: internal/cloudflare/main/minter}}, " +
			"presets: {blobs: {account: main, prototype: proto-r2-0001, description: R2, lifetime: 15m, " + store + "rotation: 5m, endpoint: '" + r2url + "'}}}\n"
		if store != "" {
			cf = strings.Replace(cf, ", rotation: 5m", "", 1)
		}
		rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) {
			noDomain(a)
			blobs("blobs")(a)
			a.Config += cf
		}})
		doc := layerFiles(t, rec)["sluis/sluis.yaml"]
		if !strings.Contains(doc, "preset: blobs") || strings.Contains(doc, "credentialsRef") {
			t.Errorf("store %q: the document does not name the preset instead of a ref:\n%s", store, doc)
		}
		for _, s := range statements(t, prop(rec.one(t, policyType, "staging-issuer-policy"), "policy").StringValue()) {
			if s["Sid"] == "SluisBlobCredentials" || s["Sid"] == "SluisBlobCredentialsKey" {
				t.Errorf("store %q: a grant for blob credentials: %v", store, s)
			}
		}
	}

	in := exampleInstallation(t)
	withCloudflare(in)
	for name, preset := range map[string]string{"an undeclared preset": "missing", "a preset that is not R2": "dns"} {
		if _, _, err := buildLambda(t, withInstallation(in, blobs(preset))); err == nil || !strings.Contains(err.Error(), "CredentialsPreset") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestExternalBlobsNeedExactlyOneOfARefAndAPreset(t *testing.T) {
	for name, b := range map[string]*arp.ExternalBlobs{
		"both":              {Bucket: "b", Endpoint: "https://x.example.test", CredentialsRef: "internal/blobs/r2", CredentialsPreset: "blobs"},
		"neither":           {Bucket: "b", Endpoint: "https://x.example.test"},
		"a bad preset name": {Bucket: "b", Endpoint: "https://x.example.test", CredentialsPreset: "Not A Label"},
	} {
		_, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
			_, err := arp.NewStorage(ctx, "staging", &arp.StorageArgs{Blobs: b})
			return err
		})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		alone := &arp.ExternalBlobs{Bucket: "b", Endpoint: "https://x.example.test", CredentialsPreset: "blobs"}
		_, err := arp.NewStorage(ctx, "staging", &arp.StorageArgs{Blobs: alone})
		return err
	}); err != nil {
		t.Errorf("a preset alone: %v", err)
	}
}
