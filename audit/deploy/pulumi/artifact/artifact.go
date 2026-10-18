// Package artifact is what the Pulumi libraries of this repository (sluis's
// and audit's) share to ship a release's files: fetch one from a path or an
// https URL (with a download cache and GITHUB_TOKEN), find its digest in the
// release's checksums.txt, and put the verified bytes, as they are, in the
// estate's versioned artifacts bucket under a content-addressed key, from which
// a function or a layer is created.
//
// It lives in the audit library's module, which sluis's library already
// requires and the release already tags at one version, so it adds no module.
package artifact

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// MaxBytes bounds a file fetched from a URL.
const MaxBytes = 100 << 20

// ReleaseBase is where the project's releases are published; a release's files
// are at <ReleaseBase>/<tag>/<file>.
const ReleaseBase = "https://github.com/truvity/sluis/releases/download"

// Args is where the library puts the files it ships. Left out, a function's
// code is uploaded with the function directly.
type Args struct {
	// Bucket is the estate's artifacts bucket. It must be versioned: the
	// function is created from the object version the upload returns, and an
	// unversioned bucket is refused when the upload is applied.
	Bucket string
	// Prefix starts every key. Default "<product>/" (`sluis/`, `audit/`).
	Prefix string
}

var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Normalize checks the arguments and fills the default prefix for a product.
func (a Args) Normalize(product string) (Args, error) {
	if !bucketRE.MatchString(a.Bucket) {
		return a, fmt.Errorf("Artifacts.Bucket %q is not an S3 bucket name", a.Bucket)
	}
	if a.Prefix == "" {
		a.Prefix = product + "/"
	}
	if !strings.HasSuffix(a.Prefix, "/") {
		a.Prefix += "/"
	}
	if strings.HasPrefix(a.Prefix, "/") || strings.Contains(a.Prefix, "//") || strings.Contains(a.Prefix, "..") {
		return a, fmt.Errorf("Artifacts.Prefix %q is a relative key prefix without empty or .. parts", a.Prefix)
	}
	return a, nil
}

// Key is the object's key: <prefix><version>/<sha256>-<name>. The digest is in
// the key, so a key never holds two contents and a re-run uploads nothing new.
func (a Args) Key(version, sha, name string) string {
	if version == "" {
		version = "unversioned"
	}
	return a.Prefix + strings.TrimPrefix(version, "v") + "/" + strings.ToLower(sha) + "-" + path.Base(name)
}

// Object is an uploaded file as a function or a layer names it.
type Object struct {
	Bucket, Key string
	// VersionID is the object's S3 version; an unversioned bucket fails it.
	VersionID pulumi.StringOutput
	// CodeSHA256 is the file's digest in base64, which is what Lambda reports
	// for the code and so the function's sourceCodeHash.
	CodeSHA256 string
}

// Upload declares the object: the file at path, as it is (an asset, never an
// archive), at key. sha is the file's SHA-256 in hex.
func Upload(ctx *pulumi.Context, resName string, a Args, key, localPath, sha string, opts ...pulumi.ResourceOption) (*Object, error) {
	raw, err := hex.DecodeString(sha)
	if err != nil || len(raw) != sha256.Size {
		return nil, fmt.Errorf("artifact: %q is not a SHA-256", sha)
	}
	obj, err := s3.NewBucketObjectv2(ctx, resName, &s3.BucketObjectv2Args{
		Bucket:      pulumi.String(a.Bucket),
		Key:         pulumi.String(key),
		Source:      pulumi.NewFileAsset(localPath),
		ContentType: pulumi.String("application/zip"),
	}, opts...)
	if err != nil {
		return nil, err
	}
	bucket := a.Bucket
	vid := obj.VersionId.ApplyT(func(v string) (string, error) {
		if v == "" || v == "null" {
			return "", fmt.Errorf("artifact: s3://%s/%s has no version id: the artifacts bucket %s is not versioned, "+
				"and a function created from it could not name the code it was made from; enable versioning on it", bucket, key, bucket)
		}
		return v, nil
	}).(pulumi.StringOutput)
	return &Object{Bucket: a.Bucket, Key: key, VersionID: vid, CodeSHA256: base64.StdEncoding.EncodeToString(raw)}, nil
}

// Matches is an output that is true when what Lambda reports as the code's
// digest is the one of the file the library verified.
func Matches(reported pulumi.StringOutput, want string) pulumi.BoolOutput {
	return reported.ApplyT(func(got string) bool { return got == want }).(pulumi.BoolOutput)
}

// WriteTemp writes raw to a file of its own, in a directory of its own (0700,
// the file 0600), and returns the path. The directory is not removed: the
// engine reads the file after this returns.
func WriteTemp(dirPrefix, name string, raw []byte) (string, error) {
	dir, err := os.MkdirTemp("", dirPrefix)
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return "", err
	}
	return p, nil
}

// Sum is the digest of raw in hex.
func Sum(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

// Zip builds a zip from files with a fixed order, no timestamps and the mode
// 0644, so the same files are the same bytes and the same key on every run.
func Zip(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		h := &zip.FileHeader{Name: n, Method: zip.Deflate}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Fetch reads a file from a path or an https URL (http only to a loopback
// address). A URL's bytes come from the download cache when it holds sha, and
// GITHUB_TOKEN, when set, is sent to github.com. The caller checks the digest
// and then calls Remember.
func Fetch(src, sha string) ([]byte, error) {
	if !strings.Contains(src, "://") {
		return os.ReadFile(src)
	}
	u, err := url.Parse(src)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	loopback := host == "127.0.0.1" || host == "localhost" || host == "::1"
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, fmt.Errorf("%q is not an https URL", src)
	}
	if sha != "" {
		if raw, ok := cached(sha); ok {
			return raw, nil
		}
	}
	req, err := http.NewRequest(http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" && host == "github.com" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", src, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", src, MaxBytes)
	}
	return raw, nil
}

func cacheFile(sha string) string {
	dir, err := os.UserCacheDir()
	if err != nil || !shaRE.MatchString(sha) {
		return ""
	}
	return filepath.Join(dir, "sluis", "artifacts", strings.ToLower(sha))
}

var shaRE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// cached returns the cached bytes of a digest, only when they still have it.
func cached(sha string) ([]byte, bool) {
	f := cacheFile(sha)
	if f == "" {
		return nil, false
	}
	raw, err := os.ReadFile(f)
	if err != nil || !strings.EqualFold(Sum(raw), sha) {
		return nil, false
	}
	return raw, true
}

// Remember keeps verified bytes in the download cache, best effort. Only
// bytes that have the digest are kept.
func Remember(sha string, raw []byte) {
	f := cacheFile(sha)
	if f == "" || !strings.EqualFold(Sum(raw), sha) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), ".dl-")
	if err != nil {
		return
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), f) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// ResolveSHA256 reads a release's checksums.txt (<base>/v<version>/checksums.txt,
// base ReleaseBase when empty) and returns the digest it lists for the file
// asset. version is the release; "" and "(devel)" are no release.
//
// checksums.txt is held to the Sigstore bundle published beside it
// (checksums.txt.sigstore.json) first: see VerifyChecksums. An unsigned
// checksums.txt is the release's own word for itself, and a digest read from
// it protects against a corrupt download and against nothing else. Only
// rel.UnverifiedChecksums reads it without the signature.
func ResolveSHA256(rel *Release, version, asset string) (string, error) {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if v == "" || v == "(devel)" {
		return "", errors.New("the digest is asked of the release's checksums.txt and the release is not known (" +
			"unset or (devel)): name the release, or give the digest")
	}
	var base, repo, trustedRoot string
	unverified := false
	if rel != nil {
		base, repo, unverified = rel.BaseURL, rel.SignerRepository, rel.UnverifiedChecksums
		trustedRoot = rel.TrustedRootJSON
	}
	src := AssetURL(base, v, "checksums.txt")
	raw, err := Fetch(src, "")
	if err != nil {
		return "", fmt.Errorf("checksums.txt of the release v%s: %w", v, err)
	}
	if !unverified {
		bundleJSON, err := Fetch(AssetURL(base, v, ChecksumsBundle), "")
		if err != nil {
			return "", fmt.Errorf("%s of the release v%s: %w (the digest is read from a signed checksums.txt: "+
				"give the package's digest, or set Release.UnverifiedChecksums to accept the unsigned file)", ChecksumsBundle, v, err)
		}
		if err := verifier(raw, bundleJSON, repo, v, trustedRoot); err != nil {
			return "", fmt.Errorf("checksums.txt of the release v%s: %w", v, err)
		}
	}
	return digestOf(raw, v, asset)
}

// verifier is VerifyChecksums; a test replaces it.
var verifier = VerifyChecksums

// digestOf is the digest checksums.txt lists for asset.
func digestOf(raw []byte, v, asset string) (string, error) {
	found := ""
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || strings.TrimPrefix(f[1], "*") != asset {
			continue
		}
		if !shaRE.MatchString(f[0]) {
			return "", fmt.Errorf("checksums.txt of the release v%s lists %s with %q, which is not a SHA-256", v, asset, f[0])
		}
		if found != "" && !strings.EqualFold(found, f[0]) {
			return "", fmt.Errorf("checksums.txt of the release v%s lists %s twice, with two digests", v, asset)
		}
		found = strings.ToLower(f[0])
	}
	if found == "" {
		return "", fmt.Errorf("checksums.txt of the release v%s does not list %s", v, asset)
	}
	return found, nil
}

// Release is how a digest the estate did not give is found: in the release's
// own checksums.txt, which is held to its Sigstore signature.
type Release struct {
	// ResolveChecksums reads the digest of a package whose SHA-256 is left
	// empty from <BaseURL>/v<Version>/checksums.txt, after verifying
	// checksums.txt against checksums.txt.sigstore.json beside it: the
	// signature must be the release workflow of SignerRepository at the tag of
	// the release, issued by GitHub's OIDC issuer, and be in Sigstore's
	// transparency log (what hack/release-verify.sh checks with cosign). A digest
	// that is given is used as it is. Off by default: the library does not take
	// a release's word for itself unless asked to.
	ResolveChecksums bool
	// Version is the release (`1.74.0`) when the package's file name does not
	// say. Empty and `(devel)` are no release and are refused.
	Version string
	// BaseURL is where releases are published, for a mirror of them. Default
	// the project's GitHub releases. A mirror carries checksums.txt.sigstore.json
	// beside checksums.txt, byte for byte.
	BaseURL string
	// SignerRepository is the repository (owner/name) whose release workflow
	// signs the release. Default truvity/sluis; a fork that releases itself names
	// its own.
	SignerRepository string
	// TrustedRootJSON is a Sigstore trusted root (the contents of a
	// `trusted_root.json`: the Fulcio and Rekor roots and the transparency logs'
	// keys) the signature is checked against, for an estate that is mirrored or
	// air-gapped and cannot reach the public-good TUF repository at deploy time.
	// Empty fetches the public-good root through TUF (cached under the user's
	// cache directory). A root you supply is the root of trust: take it from a
	// verified source (`cosign trusted-root create`, or the TUF client of a
	// connected machine) and not from the release's own mirror.
	TrustedRootJSON string
	// UnverifiedChecksums reads checksums.txt without checking its signature, as
	// ResolveChecksums did before it verified: the digest then proves only that
	// the download is what the release page says, and a release page that is
	// replaced (a stolen token, a mirror) is replaced with its checksums. It is the
	// way out of an estate that cannot reach Sigstore's trusted root (TUF) or has
	// no mirror of the bundle; give PackageSHA256 from a verified release instead
	// where you can (hack/release-verify.sh).
	UnverifiedChecksums bool
}

// ReleaseOf is the release a library ships when the estate names no package:
// Release.Version when given, else the version the library itself was built at,
// read from the build information of the program that links it (module is the
// library's module path). A development build, a pseudo-version and a module
// replaced by a local copy are no release, and are refused with what to set.
// read is debug.ReadBuildInfo.
func ReleaseOf(rel *Release, module string, read func() (*debug.BuildInfo, bool), setPackage string) (string, error) {
	if rel != nil && strings.TrimSpace(rel.Version) != "" && strings.TrimSpace(rel.Version) != "(devel)" {
		return strings.TrimPrefix(strings.TrimSpace(rel.Version), "v"), nil
	}
	hint := " Set " + setPackage + " (a path or an https URL) or Release.Version."
	bi, ok := read()
	if !ok || bi == nil {
		return "", errors.New("no package is named and the library's own release cannot be read (no build information)." + hint)
	}
	var v string
	replaced := false
	if bi.Main.Path == module {
		v = bi.Main.Version
	}
	for _, d := range bi.Deps {
		if d.Path == module {
			v, replaced = d.Version, d.Replace != nil
		}
	}
	switch {
	case replaced:
		return "", fmt.Errorf("no package is named and the module %s is replaced by a local copy, which is no release."+hint, module)
	case v == "" || v == "(devel)":
		return "", fmt.Errorf("no package is named and %s is a development build, which is no release."+hint, module)
	case !releaseRE.MatchString(v) || pseudoRE.MatchString(v):
		return "", fmt.Errorf("no package is named and %s is at %s, which is not a release version."+hint, module, v)
	}
	return strings.TrimPrefix(v, "v"), nil
}

var pseudoRE = regexp.MustCompile(`[-.][0-9]{14}-[0-9a-f]{12}$`)

var releaseRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// AssetURL is where a release publishes a file.
func AssetURL(base, version, asset string) string {
	if base == "" {
		base = ReleaseBase
	}
	return strings.TrimSuffix(base, "/") + "/v" + strings.TrimPrefix(version, "v") + "/" + asset
}

// CodeSigningArgs is a Lambda code signing configuration: the AWS Signer
// signing profiles whose signature a function's package must carry.
type CodeSigningArgs struct {
	// SigningProfileVersionArns are the AWS Signer profile versions
	// (arn:aws:signer:<region>:<account>:/signing-profiles/<name>/<version>) that
	// may sign a package. At least one, at most 20.
	SigningProfileVersionArns []string
	// Warn lets a deployment of a package that is not signed (or signed by
	// another profile) through with a CloudTrail record, instead of refusing it.
	// Default false: Lambda refuses it. The release zips are not Signer-signed,
	// so an enforcing configuration needs the estate to sign them first.
	Warn bool
	// Description is the configuration's.
	Description string
}

// NewCodeSigningConfig declares the Lambda code signing configuration, for
// LambdaArgs.CodeSigningConfigArn and its siblings. It is the estate's to make
// once and pass to every library's functions.
func NewCodeSigningConfig(ctx *pulumi.Context, name string, a CodeSigningArgs, opts ...pulumi.ResourceOption) (*lambda.CodeSigningConfig, error) {
	if n := len(a.SigningProfileVersionArns); n == 0 || n > 20 {
		return nil, fmt.Errorf("artifact: a code signing configuration names 1 to 20 signing profile versions, got %d", n)
	}
	for _, p := range a.SigningProfileVersionArns {
		if !signerProfileRE.MatchString(p) {
			return nil, fmt.Errorf("artifact: %q is not an AWS Signer profile version ARN (arn:aws:signer:<region>:<account>:/signing-profiles/<name>/<version>)", p)
		}
	}
	policy := "Enforce"
	if a.Warn {
		policy = "Warn"
	}
	return lambda.NewCodeSigningConfig(ctx, name, &lambda.CodeSigningConfigArgs{
		Description: pulumi.String(a.Description),
		AllowedPublishers: &lambda.CodeSigningConfigAllowedPublishersArgs{
			SigningProfileVersionArns: pulumi.ToStringArray(a.SigningProfileVersionArns),
		},
		Policies: &lambda.CodeSigningConfigPoliciesArgs{UntrustedArtifactOnDeployment: pulumi.String(policy)},
	}, opts...)
}

var signerProfileRE = regexp.MustCompile(`^arn:` + `aws[a-z-]*:signer:[a-z0-9-]+:[0-9]{12}:/signing-profiles/[A-Za-z0-9_]{2,64}/[A-Za-z0-9]{10}$`)
