package auditpulumi_test

import (
	"crypto/sha256"
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

	auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"
)

// The tests keep the download cache in a directory of their own.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "auditpulumi-cache-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("XDG_CACHE_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func withArtifacts(a *auditpulumi.Args) {
	a.Artifacts = &auditpulumi.ArtifactsArgs{Bucket: "acme-artifacts"}
}

func b64(hexSum string) string {
	raw, _ := hex.DecodeString(hexSum)
	return base64.StdEncoding.EncodeToString(raw)
}

func str(t *testing.T, d declared, key string) string {
	t.Helper()
	v, ok := d.Inputs[resource.PropertyKey(key)]
	if !ok || !v.IsString() {
		t.Fatalf("%s %s has no string %s: %v", d.Type, d.Name, key, d.Inputs)
	}
	return v.StringValue()
}

func TestTheArtifactsBucketHoldsTheReleaseZipAsItIsAndTheFunctionIsMadeFromIt(t *testing.T) {
	var writerSHA string
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		withArtifacts(a)
		writerSHA = a.Writer.PackageSHA256
	})
	if err != nil {
		t.Fatal(err)
	}
	obj := rec.one(t, "aws:s3/bucketObjectv2:BucketObjectv2", "audit-writer-code")
	if got, want := str(t, obj, "key"), "audit/"+releaseVersion+"/"+writerSHA+"-audit-writer-lambda_"+releaseVersion+"_linux_arm64.zip"; got != want {
		t.Errorf("object key %q, want %q", got, want)
	}
	if str(t, obj, "bucket") != "acme-artifacts" {
		t.Errorf("bucket %v", obj.Inputs["bucket"])
	}
	if src := obj.Inputs["source"]; !src.IsAsset() || src.IsArchive() {
		t.Errorf("the object's source must be a file asset and not an archive: %v", src)
	}
	if _, ok := obj.Inputs["source"].AssetValue().GetPath(); !ok {
		t.Errorf("the source is not a file asset: %v", obj.Inputs["source"])
	}
	fn := rec.one(t, "aws:lambda/function:Function", "audit-writer")
	if _, has := fn.Inputs["code"]; has {
		t.Errorf("the function must not carry the code with it: %v", fn.Inputs["code"])
	}
	if str(t, fn, "s3Bucket") != "acme-artifacts" || str(t, fn, "s3Key") != str(t, obj, "key") ||
		str(t, fn, "s3ObjectVersion") != "ver-audit-writer-code" || str(t, fn, "sourceCodeHash") != b64(writerSHA) {
		t.Errorf("function code is not the uploaded object: %v", fn.Inputs)
	}
	layer := rec.one(t, "aws:lambda/layerVersion:LayerVersion", "audit-writer-config")
	if _, has := layer.Inputs["code"]; has {
		t.Errorf("the layer must come from the bucket: %v", layer.Inputs)
	}
	lobj := rec.one(t, "aws:s3/bucketObjectv2:BucketObjectv2", "audit-writer-config-code")
	if !strings.HasPrefix(str(t, lobj, "key"), "audit/"+releaseVersion+"/") || !strings.HasSuffix(str(t, lobj, "key"), "-audit-writer-config.zip") ||
		str(t, layer, "s3Key") != str(t, lobj, "key") || str(t, layer, "s3ObjectVersion") != "ver-audit-writer-config-code" {
		t.Errorf("layer %v from object %v", layer.Inputs, lobj.Inputs)
	}
	if out["writerCodeMatches"] != "true" || out["notaryCodeMatches"] != "true" {
		t.Errorf("code matches: %v", out)
	}
}

func TestTheLayerKeyMovesOnlyWithItsContent(t *testing.T) {
	key := func(edit func(*auditpulumi.Args)) string {
		rec, _, err := build(t, func(a *auditpulumi.Args) { withArtifacts(a); edit(a) })
		if err != nil {
			t.Fatal(err)
		}
		return str(t, rec.one(t, "aws:s3/bucketObjectv2:BucketObjectv2", "audit-notary-config-code"), "key")
	}
	same := func(*auditpulumi.Args) {}
	if a, b := key(same), key(same); a != b {
		t.Errorf("the same configuration has two keys: %s, %s", a, b)
	}
	if a, b := key(same), key(func(a *auditpulumi.Args) { a.Writer.DeploymentYAML += "# changed\n" }); a != b {
		t.Errorf("the notary's layer moved with the writer's profile document: %s, %s", a, b)
	}
}

func TestWithoutArtifactsTheCodeIsUploadedWithTheFunction(t *testing.T) {
	rec, out, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.ofType("aws:s3/bucketObjectv2:BucketObjectv2")); n != 0 {
		for _, d := range rec.ofType("aws:s3/bucketObjectv2:BucketObjectv2") {
			if strings.HasSuffix(d.Name, "-code") {
				t.Errorf("an artifact was uploaded without Artifacts: %s", d.Name)
			}
		}
	}
	fn := rec.one(t, "aws:lambda/function:Function", "audit-writer")
	if c := fn.Inputs["code"]; !c.IsArchive() {
		t.Errorf("the fallback code is not an archive: %v", fn.Inputs)
	}
	if _, has := fn.Inputs["s3Bucket"]; has {
		t.Errorf("the fallback names a bucket: %v", fn.Inputs)
	}
	if out["writerCodeMatches"] != "true" {
		t.Errorf("code matches: %v", out)
	}
}

func TestAnUnversionedArtifactsBucketIsRefused(t *testing.T) {
	mockSetup = func(r *recorder) { r.unversioned = true }
	defer func() { mockSetup = nil }()
	_, _, err := build(t, withArtifacts)
	if err == nil || !strings.Contains(err.Error(), "not versioned") {
		t.Errorf("an unversioned bucket: %v", err)
	}
}

func TestArtifactsAreHeldToTheirRules(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*auditpulumi.Args)
		want string
	}{
		"bucket": {func(a *auditpulumi.Args) { a.Artifacts = &auditpulumi.ArtifactsArgs{Bucket: "Not A Bucket"} }, "Artifacts.Bucket"},
		"empty":  {func(a *auditpulumi.Args) { a.Artifacts = &auditpulumi.ArtifactsArgs{} }, "Artifacts.Bucket"},
		"prefix up": {func(a *auditpulumi.Args) {
			a.Artifacts = &auditpulumi.ArtifactsArgs{Bucket: "acme-artifacts", Prefix: "../x"}
		}, "Artifacts.Prefix"},
		"prefix root": {func(a *auditpulumi.Args) {
			a.Artifacts = &auditpulumi.ArtifactsArgs{Bucket: "acme-artifacts", Prefix: "/x"}
		}, "Artifacts.Prefix"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := build(t, tc.edit); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestACustomPrefixStartsTheKey(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Artifacts = &auditpulumi.ArtifactsArgs{Bucket: "acme-artifacts", Prefix: "releases/audit"}
	})
	if err != nil {
		t.Fatal(err)
	}
	if k := str(t, rec.one(t, "aws:s3/bucketObjectv2:BucketObjectv2", "audit-writer-code"), "key"); !strings.HasPrefix(k, "releases/audit/"+releaseVersion+"/") {
		t.Errorf("key %q", k)
	}
}

func TestACodeDigestThatIsNotTheFunctionsIsReported(t *testing.T) {
	mockSetup = func(r *recorder) { r.wrongCode = true }
	defer func() { mockSetup = nil }()
	_, out, err := build(t, withArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	if out["writerCodeMatches"] != "false" || out["notaryCodeMatches"] != "false" {
		t.Errorf("code matches: %v", out)
	}
}

// ---- the release's checksums.txt

// release serves a release's checksums.txt and the zips, as the project's
// release page does, and returns its URL.
func release(t *testing.T, version string, files map[string][]byte, listed map[string]string) string {
	t.Helper()
	var sums strings.Builder
	for name, body := range files {
		sum := sha256.Sum256(body)
		d := hex.EncodeToString(sum[:])
		if l, ok := listed[name]; ok {
			d = l
		}
		sums.WriteString(d + "  " + name + "\n")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v"+version+"/checksums.txt":
			_, _ = w.Write([]byte(sums.String()))
		case strings.HasPrefix(r.URL.Path, "/v"+version+"/"):
			if b, ok := files[strings.TrimPrefix(r.URL.Path, "/v"+version+"/")]; ok {
				_, _ = w.Write(b)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func zipFiles(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, cmd := range []string{"audit-writer-lambda", "audit-notary-lambda"} {
		p, _ := releaseZip(t, t.TempDir(), cmd, releaseVersion)
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = raw
	}
	return out
}

func resolving(base string, version string) func(*auditpulumi.Args) {
	return func(a *auditpulumi.Args) {
		a.Release = &auditpulumi.ReleaseArgs{ResolveChecksums: true, BaseURL: base, Version: version}
		a.Writer.Package = base + "/v" + releaseVersion + "/audit-writer-lambda_" + releaseVersion + "_linux_arm64.zip"
		a.Notary.Package = base + "/v" + releaseVersion + "/audit-notary-lambda_" + releaseVersion + "_linux_arm64.zip"
		a.Writer.PackageSHA256, a.Notary.PackageSHA256 = "", ""
	}
}

func TestADigestLeftEmptyIsReadFromTheReleasesChecksums(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	base := release(t, releaseVersion, zipFiles(t), nil)
	if _, _, err := build(t, resolving(base, "")); err != nil {
		t.Fatalf("digests from checksums.txt: %v", err)
	}
}

func TestResolvedDigestsAreStillHeldToTheBytes(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	files := zipFiles(t)
	base := release(t, releaseVersion, files, map[string]string{
		"audit-writer-lambda_" + releaseVersion + "_linux_arm64.zip": strings.Repeat("ab", 32),
	})
	if _, _, err := build(t, resolving(base, "")); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("a zip that is not the one checksums.txt lists: %v", err)
	}
}

func TestChecksumsWithoutTheFileOrWithoutAReleaseAreRefused(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	base := release(t, releaseVersion, map[string][]byte{"other.zip": []byte("x")}, nil)
	if _, _, err := build(t, resolving(base, "")); err == nil || !strings.Contains(err.Error(), "does not list") {
		t.Errorf("a file checksums.txt does not list: %v", err)
	}
	// A package whose name does not say the release, and no release given.
	dir := t.TempDir()
	p, _ := zipOf(t, dir, "renamed.zip", map[string][]byte{"bootstrap": fixtureBinary(t, "audit-writer-lambda")})
	_, _, err := build(t, func(a *auditpulumi.Args) {
		resolving(base, "")(a)
		a.Writer.Package = p
	})
	if err == nil || !strings.Contains(err.Error(), "release is not known") {
		t.Errorf("no release: %v", err)
	}
	_, _, err = build(t, func(a *auditpulumi.Args) {
		resolving(base, "(devel)")(a)
		a.Writer.Package = p
	})
	if err == nil || !strings.Contains(err.Error(), "release is not known") {
		t.Errorf("(devel): %v", err)
	}
}

func TestAnEmptyDigestIsRefusedWithoutTheFlag(t *testing.T) {
	_, _, err := build(t, func(a *auditpulumi.Args) { a.Writer.PackageSHA256 = "" })
	if err == nil || !strings.Contains(err.Error(), "PackageSHA256 is required") {
		t.Errorf("%v", err)
	}
}

func TestADownloadIsCachedByItsDigest(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	hits := 0
	files := zipFiles(t)
	base := release(t, releaseVersion, files, nil)
	counted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".zip") {
			hits++
		}
		resp, err := http.Get(base + r.URL.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 1<<20)
		for {
			n, err := resp.Body.Read(buf)
			_, _ = w.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}))
	defer counted.Close()
	for i := 0; i < 2; i++ {
		if _, _, err := build(t, resolving(counted.URL, "")); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 2 { // the writer's and the notary's, once each
		t.Errorf("the zips were downloaded %d times, want 2", hits)
	}
}

// ---- the library's own release as the default package

func depAt(version string) *debug.BuildInfo {
	return &debug.BuildInfo{Deps: []*debug.Module{{Path: "github.com/truvity/sluis/audit/deploy/pulumi", Version: version}}}
}

func ownRelease(a *auditpulumi.Args, base string) {
	a.Release = &auditpulumi.ReleaseArgs{BaseURL: base}
	a.Writer.Package, a.Notary.Package = "", ""
	a.Writer.PackageSHA256, a.Notary.PackageSHA256 = "", ""
}

func TestWithNoPackageTheLibrarysOwnReleaseIsFetchedAndHeldToItsChecksums(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(auditpulumi.SetBuildInfo(depAt("v" + releaseVersion)))
	base := release(t, releaseVersion, zipFiles(t), nil)
	if _, _, err := build(t, func(a *auditpulumi.Args) { ownRelease(a, base) }); err != nil {
		t.Fatalf("the library's own release: %v", err)
	}
}

func TestAPinnedDigestWinsOverTheReleasesChecksums(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(auditpulumi.SetBuildInfo(depAt("v" + releaseVersion)))
	base := release(t, releaseVersion, zipFiles(t), nil)
	_, _, err := build(t, func(a *auditpulumi.Args) {
		ownRelease(a, base)
		a.Writer.PackageSHA256 = strings.Repeat("cd", 32) // not the release's
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("a pinned digest that is not the release's: %v", err)
	}
}

func TestADevelopmentBuildOrAReplacedModuleHasNoReleaseToFetch(t *testing.T) {
	base := release(t, releaseVersion, zipFiles(t), nil)
	for name, bi := range map[string]*debug.BuildInfo{
		"devel":  depAt("(devel)"),
		"empty":  depAt(""),
		"pseudo": depAt("v1.74.1-0.20261008120000-abcdef123456"),
		"replaced": {Deps: []*debug.Module{{Path: "github.com/truvity/sluis/audit/deploy/pulumi", Version: "v" + releaseVersion,
			Replace: &debug.Module{Path: "../audit"}}}},
		"none": nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(auditpulumi.SetBuildInfo(bi))
			_, _, err := build(t, func(a *auditpulumi.Args) { ownRelease(a, base) })
			if err == nil || !strings.Contains(err.Error(), "Writer.Package") || !strings.Contains(err.Error(), "Release.Version") {
				t.Errorf("got %v", err)
			}
		})
	}
}

func TestReleaseVersionNamesTheReleaseWhenBuildInfoDoesNot(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(auditpulumi.SetBuildInfo(depAt("(devel)")))
	base := release(t, releaseVersion, zipFiles(t), nil)
	if _, _, err := build(t, func(a *auditpulumi.Args) {
		ownRelease(a, base)
		a.Release.Version = releaseVersion
	}); err != nil {
		t.Fatal(err)
	}
}

// ---- the live alias

func TestEachFunctionPublishesAVersionAndItsCallersUseTheLiveAlias(t *testing.T) {
	rec, out, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audit-writer", "audit-notary"} {
		fn := rec.one(t, "aws:lambda/function:Function", name)
		if v := fn.Inputs["publish"]; !v.IsBool() || !v.BoolValue() {
			t.Errorf("%s does not publish a version: %v", name, fn.Inputs)
		}
		al := rec.one(t, "aws:lambda/alias:Alias", name+"-live")
		if str(t, al, "name") != "live" || str(t, al, "functionName") != name || str(t, al, "functionVersion") != "7" {
			t.Errorf("%s alias %v", name, al.Inputs)
		}
	}
	if out["writerLiveAliasArn"] != out["writerFn"]+":live" || out["notaryLiveAliasArn"] != out["notaryFn"]+":live" ||
		out["writerLiveVersion"] != "7" || out["notaryLiveVersion"] != "7" {
		t.Errorf("outputs %v", out)
	}
	esm := rec.one(t, "aws:lambda/eventSourceMapping:EventSourceMapping", "audit-writer")
	if got := str(t, esm, "functionName"); got != out["writerFn"]+":live" {
		t.Errorf("the writer's event source invokes %q", got)
	}
	s := rec.one(t, "aws:scheduler/schedule:Schedule", "audit-notary")
	if got := s.Inputs["target"].ObjectValue()["arn"].StringValue(); got != out["notaryFn"]+":live" {
		t.Errorf("the notary's schedule invokes %q", got)
	}
	cfg := rec.one(t, "aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig", "audit-notary")
	if str(t, cfg, "qualifier") != "live" {
		t.Errorf("async config %v", cfg.Inputs)
	}
}
