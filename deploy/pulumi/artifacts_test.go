package sluispulumi_test

import (
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sluispulumi-cache-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("XDG_CACHE_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

const zipName = "sluis-issuer_1.63.0_linux_arm64.zip"

func withArtifacts(a *arp.LambdaArgs) { a.Artifacts = &arp.ArtifactsArgs{Bucket: "acme-artifacts"} }

func strIn(t *testing.T, d declared, key string) string {
	t.Helper()
	v := prop(d, key)
	if !v.IsString() {
		t.Fatalf("%s %s has no string %s: %v", d.Type, d.Name, key, d.Inputs)
	}
	return v.StringValue()
}

const objType = "aws:s3/bucketObjectv2:BucketObjectv2"

func TestTheArtifactsBucketHoldsTheReleaseZipAsItIsAndTheFunctionIsMadeFromIt(t *testing.T) {
	e := estate{mutate: withArtifacts}
	pkg := zipFile(t, nil)
	e.pkg = pkg
	digest := sha(t, pkg)
	rec, out, err := buildLambda(t, e)
	if err != nil {
		t.Fatal(err)
	}
	obj := rec.one(t, objType, "staging-code")
	if got, want := strIn(t, obj, "key"), "sluis/1.63.0/"+digest+"-"+zipName; got != want {
		t.Errorf("key %q, want %q", got, want)
	}
	if src := prop(obj, "source"); !src.IsAsset() || src.IsArchive() {
		t.Errorf("the source must be a file asset and not an archive: %v", src)
	}
	fn := rec.one(t, "aws:lambda/function:Function", "staging-http")
	if _, has := fn.Inputs["code"]; has {
		t.Errorf("the function carries its code: %v", fn.Inputs["code"])
	}
	raw, _ := hexDecode(digest)
	if strIn(t, fn, "s3Bucket") != "acme-artifacts" || strIn(t, fn, "s3Key") != strIn(t, obj, "key") ||
		strIn(t, fn, "s3ObjectVersion") != "ver-staging-code" || strIn(t, fn, "sourceCodeHash") != raw {
		t.Errorf("function code: %v", fn.Inputs)
	}
	layer := rec.one(t, layerType, "staging-config")
	lobj := rec.one(t, objType, "staging-config-code")
	if _, has := layer.Inputs["code"]; has || strIn(t, layer, "s3Key") != strIn(t, lobj, "key") ||
		strIn(t, layer, "s3ObjectVersion") != "ver-staging-config-code" {
		t.Errorf("layer %v from %v", layer.Inputs, lobj.Inputs)
	}
	if out["codeMatches"] != "true" {
		t.Errorf("codeMatches %q", out["codeMatches"])
	}
}

func TestWithoutArtifactsTheCodeIsUploadedWithTheFunction(t *testing.T) {
	rec, out, err := buildLambda(t, estate{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.has(objType, "staging-code") || rec.has(objType, "staging-config-code") {
		t.Error("an artifact was uploaded without Artifacts")
	}
	fn := rec.one(t, "aws:lambda/function:Function", "staging-http")
	if !fn.Inputs["code"].IsArchive() {
		t.Errorf("fallback code %v", fn.Inputs["code"])
	}
	if _, has := fn.Inputs["s3Bucket"]; has {
		t.Error("the fallback names a bucket")
	}
	if _, has := fn.Inputs["sourceCodeHash"]; has {
		t.Error("the fallback changed: sourceCodeHash is set")
	}
	_ = out
}

func TestAnUnversionedArtifactsBucketIsRefused(t *testing.T) {
	mockSetup = func(r *recorder) { r.unversioned = true }
	defer func() { mockSetup = nil }()
	if _, _, err := buildLambda(t, estate{mutate: withArtifacts}); err == nil || !strings.Contains(err.Error(), "not versioned") {
		t.Errorf("got %v", err)
	}
}

func TestACodeDigestThatIsNotTheFunctionsIsReported(t *testing.T) {
	mockSetup = func(r *recorder) { r.wrongCode = true }
	defer func() { mockSetup = nil }()
	_, out, err := buildLambda(t, estate{mutate: withArtifacts})
	if err != nil {
		t.Fatal(err)
	}
	if out["codeMatches"] != "false" {
		t.Errorf("codeMatches %q", out["codeMatches"])
	}
}

func TestArtifactsAreHeldToTheirRules(t *testing.T) {
	for name, a := range map[string]*arp.ArtifactsArgs{
		"bucket": {Bucket: "Not A Bucket"}, "empty": {}, "prefix": {Bucket: "acme-artifacts", Prefix: "../x"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := buildLambda(t, estate{mutate: func(l *arp.LambdaArgs) { l.Artifacts = a }})
			if err == nil || !strings.Contains(err.Error(), "Artifacts.") {
				t.Errorf("got %v", err)
			}
		})
	}
}

// ---- the release's checksums.txt

// releaseServer serves the zip and the release's checksums.txt at v1.63.0.
func releaseServer(t *testing.T, listed string, hits *int) (base, digest string) {
	t.Helper()
	pkg := zipFile(t, nil)
	raw, err := os.ReadFile(pkg)
	if err != nil {
		t.Fatal(err)
	}
	digest = sha(t, pkg)
	if listed == "" {
		listed = digest
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.63.0/checksums.txt":
			_, _ = w.Write([]byte(listed + "  " + zipName + "\n" + strings.Repeat("0", 64) + "  other.tar.gz\n"))
		case "/v1.63.0/" + zipName:
			if hits != nil {
				*hits++
			}
			_, _ = w.Write(raw)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, digest
}

func resolve(base, version string) func(*arp.LambdaArgs) {
	return func(a *arp.LambdaArgs) {
		a.Package, a.PackageSHA256 = base+"/v1.63.0/"+zipName, ""
		a.Release = &arp.ReleaseArgs{ResolveChecksums: true, BaseURL: base, Version: version}
	}
}

func TestADigestLeftEmptyIsReadFromTheReleasesChecksums(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	base, _ := releaseServer(t, "", nil)
	if _, _, err := buildLambda(t, estate{pkg: base + "/v1.63.0/" + zipName, mutate: resolve(base, "")}); err != nil {
		t.Fatal(err)
	}
}

func TestAResolvedDigestIsStillHeldToTheBytes(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	base, _ := releaseServer(t, strings.Repeat("ab", 32), nil)
	if _, _, err := buildLambda(t, estate{pkg: base + "/v1.63.0/" + zipName, mutate: resolve(base, "")}); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("got %v", err)
	}
}

func TestADigestIsRefusedWithoutTheFlagAndAnUnknownReleaseWithIt(t *testing.T) {
	base, _ := releaseServer(t, "", nil)
	_, _, err := buildLambda(t, estate{pkg: base + "/v1.63.0/" + zipName, mutate: func(a *arp.LambdaArgs) { a.PackageSHA256 = "" }})
	if err == nil || !strings.Contains(err.Error(), "PackageSHA256") {
		t.Errorf("no flag: %v", err)
	}
	local := zipFile(t, nil)
	renamed := filepath.Join(filepath.Dir(local), "renamed.zip")
	if err := os.Rename(local, renamed); err != nil {
		t.Fatal(err)
	}
	_, _, err = buildLambda(t, estate{pkg: renamed, mutate: func(a *arp.LambdaArgs) {
		a.PackageSHA256, a.PackageVersion = "", "1.63.0"
		a.Release = &arp.ReleaseArgs{ResolveChecksums: true, BaseURL: base, Version: "(devel)"}
	}})
	if err == nil || !strings.Contains(err.Error(), "release is not known") {
		t.Errorf("(devel): %v", err)
	}
}

func TestADownloadIsCachedByItsDigest(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	hits := 0
	base, _ := releaseServer(t, "", &hits)
	for i := 0; i < 2; i++ {
		if _, _, err := buildLambda(t, estate{pkg: base + "/v1.63.0/" + zipName, mutate: resolve(base, "")}); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 1 {
		t.Errorf("downloaded %d times, want 1", hits)
	}
}

// ---- the library's own release as the default package

func depAt(v string) *debug.BuildInfo {
	return &debug.BuildInfo{Deps: []*debug.Module{{Path: "github.com/truvity/sluis/deploy/pulumi", Version: v}}}
}

func own(base string) func(*arp.LambdaArgs) {
	return func(a *arp.LambdaArgs) {
		a.Package, a.PackageSHA256 = "", ""
		a.Release = &arp.ReleaseArgs{BaseURL: base}
	}
}

func TestWithNoPackageTheLibrarysOwnReleaseIsFetchedAndHeldToItsChecksums(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(arp.SetBuildInfo(depAt("v1.63.0")))
	base, _ := releaseServer(t, "", nil)
	if _, _, err := buildLambda(t, estate{pkg: "x", mutate: own(base)}); err != nil {
		t.Fatal(err)
	}
}

func TestAPinnedDigestWinsOverTheReleasesChecksums(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(arp.SetBuildInfo(depAt("v1.63.0")))
	base, _ := releaseServer(t, "", nil)
	_, _, err := buildLambda(t, estate{pkg: "x", mutate: func(a *arp.LambdaArgs) {
		own(base)(a)
		a.PackageSHA256 = strings.Repeat("cd", 32)
	}})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("got %v", err)
	}
}

func TestADevelopmentBuildOrAReplacedModuleHasNoReleaseToFetch(t *testing.T) {
	base, _ := releaseServer(t, "", nil)
	for name, bi := range map[string]*debug.BuildInfo{
		"devel": depAt("(devel)"), "empty": depAt(""), "pseudo": depAt("v1.74.1-0.20261008120000-abcdef123456"),
		"replaced": {Deps: []*debug.Module{{Path: "github.com/truvity/sluis/deploy/pulumi", Version: "v1.63.0", Replace: &debug.Module{Path: "../x"}}}},
		"none":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(arp.SetBuildInfo(bi))
			_, _, err := buildLambda(t, estate{pkg: "x", mutate: own(base)})
			if err == nil || !strings.Contains(err.Error(), "LambdaArgs.Package") || !strings.Contains(err.Error(), "Release.Version") {
				t.Errorf("got %v", err)
			}
		})
	}
}

func hexDecode(h string) (string, error) {
	return b64Of(h), nil
}

var _ = resource.PropertyKey("")

func b64Of(h string) string {
	raw, _ := hex.DecodeString(h)
	return base64.StdEncoding.EncodeToString(raw)
}

// ---- the live alias

const aliasType = "aws:lambda/alias:Alias"

func TestTheFunctionPublishesAVersionAndCallersUseTheLiveAlias(t *testing.T) {
	rec, out, err := buildLambda(t, estate{orgs: []string{"acme"}, workspaces: []string{"T1"}})
	if err != nil {
		t.Fatal(err)
	}
	fn := rec.one(t, "aws:lambda/function:Function", "staging-http")
	if !prop(fn, "publish").IsBool() || !prop(fn, "publish").BoolValue() {
		t.Errorf("the function does not publish a version: %v", fn.Inputs)
	}
	al := rec.one(t, aliasType, "staging-live")
	if strIn(t, al, "name") != "live" || strIn(t, al, "functionName") != "sluis" || strIn(t, al, "functionVersion") != "7" {
		t.Errorf("alias %v", al.Inputs)
	}
	live := arnp + "lambda:eu-west-1:" + account + ":function:sluis:live"
	if out["liveAliasArn"] != live || out["liveVersion"] != "7" {
		t.Errorf("outputs %v", out)
	}
	// Every target a caller uses is the alias.
	if got := strIn(t, rec.one(t, "aws:apigatewayv2/integration:Integration", "staging-api-integration"), "integrationUri"); got != live {
		t.Errorf("integration %q", got)
	}
	perm := rec.one(t, "aws:lambda/permission:Permission", "staging-api-invoke")
	if strIn(t, perm, "qualifier") != "live" || strIn(t, perm, "function") != "sluis" {
		t.Errorf("api permission %v", perm.Inputs)
	}
	n := 0
	for _, s := range rec.ofType("aws:scheduler/schedule:Schedule") {
		n++
		if got := prop(s, "target").ObjectValue()["arn"].StringValue(); got != live {
			t.Errorf("%s targets %q", s.Name, got)
		}
	}
	if n != 3 {
		t.Errorf("%d schedules", n)
	}
	sp := grants(statements(t, prop(rec.one(t, policyType, "staging-scheduler-policy"), "policy").StringValue()))
	if got := sp["lambda:InvokeFunction"]; len(got) != 1 || got[0] != live {
		t.Errorf("scheduler grant %v, want exactly the alias", got)
	}
	fp := grants(statements(t, prop(rec.one(t, policyType, "staging-http-policy"), "policy").StringValue()))
	if got := fp["lambda:InvokeFunction"]; len(got) != 1 || got[0] != arnp+"lambda:"+region+":"+account+":function:sluis:live" {
		t.Errorf("self grant %v", got)
	}
	cfg := rec.one(t, "aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig", "staging-http")
	if strIn(t, cfg, "qualifier") != "live" {
		t.Errorf("async config %v", cfg.Inputs)
	}
}

// The alias is the only thing that changed for the API's domain: the API, its
// stage and the front door's inputs name no function, so moving the integration
// to the alias replaces neither the domain nor its mapping.
func TestTheAliasLeavesTheAPIDomainAndMappingAlone(t *testing.T) {
	rec, _, err := buildLambda(t, estate{})
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"aws:apigatewayv2/api:Api", "aws:apigatewayv2/stage:Stage", "aws:apigatewayv2/domainName:DomainName",
		"aws:apigatewayv2/apiMapping:ApiMapping", "aws:apigatewayv2/route:Route"} {
		for _, d := range rec.ofType(typ) {
			for k, v := range d.Inputs {
				if s := v.String(); strings.Contains(s, ":live") || strings.Contains(s, "function:sluis") {
					t.Errorf("%s %s names the function in %s: %s", typ, d.Name, k, s)
				}
			}
		}
	}
	if !rec.has("aws:apigatewayv2/api:Api", "staging-api") || !rec.has("aws:apigatewayv2/stage:Stage", "staging-api-stage") {
		t.Error("the API's logical names moved")
	}
}

func TestARunNowInvokesTheAlias(t *testing.T) {
	rec, _ := mustLambda(t, estate{config: "issuerURL: https://x.example\nadapters: {trigger: {adapter: invoke, settings: {github: sluis:live}}}\n"})
	if doc := layerFiles(t, rec)["sluis/sluis.yaml"]; !strings.Contains(doc, "github: sluis:live\n") || !strings.Contains(doc, "slack: sluis:live\n") {
		t.Errorf("trigger:\n%s", doc)
	}
}
