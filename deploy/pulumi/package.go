package sluispulumi

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/sluis/audit/deploy/pulumi/artifact"
)

// LayerRoot is where the configuration layer is mounted in a function: Lambda
// extracts a layer under /opt, and the layer holds sluis/.
const LayerRoot = "/opt/sluis"

// DocumentPath is where a function finds a document of the configuration
// layer: its own service document (`http`, `github`, `slack`) or `policy`.
// SLUIS_CONFIG names the first; every service document's policy.file names
// the second.
func DocumentPath(name string) string { return LayerRoot + "/" + name + ".yaml" }

const maxPackageBytes = artifact.MaxBytes

// releasePackage is the release zip once it has been held to its digest.
type releasePackage struct {
	// Path is a file holding exactly the verified bytes.
	Path string
	// SHA256 is the digest in hex, CodeSHA256 the same in base64 (what Lambda
	// reports for the code).
	SHA256, CodeSHA256 string
	// Name is the zip's file name and Version the release it is ("" when
	// neither the name nor LambdaArgs.PackageVersion says).
	Name, Version string
}

var releaseName = regexp.MustCompile(`^sluis-lambda_v?(.+)_linux_[a-z0-9]+\.zip$`)

// packageRelease is the release a package is: the explicit version, else the one
// in the file's name.
func packageRelease(src, explicit string) string {
	if explicit != "" {
		return strings.TrimPrefix(explicit, "v")
	}
	if m := releaseName.FindStringSubmatch(fileName(src)); m != nil {
		return m[1]
	}
	return ""
}

// fileName is the last element of a path or of a URL's path.
func fileName(src string) string {
	if u, err := url.Parse(src); err == nil && strings.Contains(src, "://") {
		src = u.Path
	}
	return path.Base(filepath.ToSlash(src))
}

// loadPackage reads the release zip, from a path or an https URL, holds it to
// the SHA-256 it must have (given, or, when the arguments ask for it, the one in
// the release's checksums.txt), checks it is a function package (`bootstrap` at
// its root, no entry outside it), and returns a copy of exactly the bytes it held to
// the digest: a file of its own, named by the digest, in a directory of its own
// (0700, the file 0600). The function's code is that copy, never the caller's
// path, which could change between the check and the upload, and never a zip
// rebuilt here. The directory is not removed: the engine reads the file after
// this returns.
func loadPackage(src, sha, version string, rel *ReleaseArgs) (*releasePackage, error) {
	if strings.TrimSpace(sha) == "" && rel != nil && rel.ResolveChecksums {
		v := rel.Version
		if v == "" {
			v = version
		}
		var err error
		if sha, err = artifact.ResolveSHA256(rel.BaseURL, v, fileName(src)); err != nil {
			return nil, fmt.Errorf("sluispulumi: PackageSHA256: %w", err)
		}
	}
	sha = strings.TrimSpace(sha)
	raw, err := artifact.Fetch(src, sha)
	if err != nil {
		return nil, fmt.Errorf("sluispulumi: Package: %w", err)
	}
	sum := sha256.Sum256(raw)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), sha) {
		return nil, fmt.Errorf("sluispulumi: the package %s has the SHA-256 %x, not the %s asked for", src, sum, sha)
	}
	if _, err = readZip(raw); err != nil {
		return nil, err
	}
	artifact.Remember(sha, raw)
	local, err := artifact.WriteTemp("sluis-package-", hex.EncodeToString(sum[:])+".zip", raw)
	if err != nil {
		return nil, fmt.Errorf("sluispulumi: %w", err)
	}
	return &releasePackage{
		Path: local, SHA256: hex.EncodeToString(sum[:]), CodeSHA256: base64.StdEncoding.EncodeToString(sum[:]),
		Name: fileName(src), Version: version,
	}, nil
}

type zipEntry struct {
	mode os.FileMode
	body []byte
}

func readZip(raw []byte) (map[string]zipEntry, error) {
	zr, err := zip.NewReader(strings.NewReader(string(raw)), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("sluispulumi: Package is not a zip: %w", err)
	}
	out := map[string]zipEntry{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Clean(f.Name)
		if path.IsAbs(name) || strings.Contains(f.Name, "..") || strings.Contains(name, "..") || strings.Contains(f.Name, "\\") {
			return nil, fmt.Errorf("sluispulumi: Package holds %q, which is outside the package root", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("sluispulumi: Package: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(rc, maxPackageBytes+1))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("sluispulumi: Package: %w", err)
		}
		if len(body) > maxPackageBytes {
			return nil, fmt.Errorf("sluispulumi: Package: %s is larger than %d bytes unzipped", f.Name, maxPackageBytes)
		}
		out[name] = zipEntry{mode: f.Mode().Perm(), body: body}
	}
	if e, ok := out["bootstrap"]; !ok || len(e.body) == 0 {
		return nil, errors.New("sluispulumi: Package has no `bootstrap` at its root: pass the release's sluis-lambda_<version>_linux_arm64.zip")
	}
	return out, nil
}

// ArtifactsArgs is where the library puts the files it ships (see artifact.Args).
type ArtifactsArgs = artifact.Args

// ReleaseArgs is how a missing digest or package is found (see artifact.Release).
type ReleaseArgs = artifact.Release

// uploadArtifact is the verified release zip in the artifacts bucket.
func uploadArtifact(ctx *pulumi.Context, resName string, art *ArtifactsArgs, p *releasePackage, opts ...pulumi.ResourceOption) (*artifact.Object, error) {
	return artifact.Upload(ctx, resName, *art, art.Key(p.Version, p.SHA256, p.Name), p.Path, p.SHA256, opts...)
}

// resolvedDigest is what the required-argument check reads as PackageSHA256:
// the argument, or a placeholder when the release's checksums.txt will give it.
func resolvedDigest(a LambdaArgs) string {
	if a.PackageSHA256 == "" && a.Release != nil && a.Release.ResolveChecksums {
		return "from checksums.txt"
	}
	return a.PackageSHA256
}

const libraryModule = "github.com/truvity/sluis/deploy/pulumi"

// readBuildInfo is where the library reads its own release; a test replaces it.
var readBuildInfo = debug.ReadBuildInfo
