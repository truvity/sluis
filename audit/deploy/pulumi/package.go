package auditpulumi

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
		"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/truvity/sluis/audit/deploy/pulumi/artifact"
)

// What the release publishes and the library takes as a function's code: the
// zip `audit-writer-lambda_<version>_linux_arm64.zip` (and the notary's), with
// `bootstrap` at its root, byte for byte as the release made it. The library
// does not build a package. Nothing is added to it, so what runs is what the
// release's checksums name, and the configuration comes in a layer of its own.

// maxPackageBytes bounds a zip fetched from a URL: a Lambda package is 250 MB
// unzipped at most, and a release's is a few MB.
const maxPackageBytes = 100 << 20

// releasePackage is a release zip that has been read, checked and written where
// Pulumi can read it.
type releasePackage struct {
	// Path is a file holding exactly the verified bytes. Pulumi reads it when it
	// registers the function, which is after this returns, so it is a copy that
	// nothing changes and not the caller's file.
	Path string
	// SHA256 is the zip's digest in hex, and CodeSHA256 the same in base64, which
	// is what Lambda reports for the code and so what the function's
	// sourceCodeHash is.
	SHA256, CodeSHA256 string
	// Version is the release the zip's name says it is: the release names its
	// files `<command>_<version>_linux_arm64.zip` and lists their digests under
	// those names in checksums.txt, which the digest above was checked against.
	// "" when the name is not the release's (a zip renamed, or built by hand).
	//
	// It is the name and not the binary's own stamp because the release builds
	// with `-s -w -trimpath`, and the toolchain then leaves the linker flags, and
	// with them the stamp, out of the build information it records.
	Version string
	// Name is the zip's file name, which the artifact key ends with.
	Name string
}

// loadPackage reads the released zip from a path or an https URL and checks it:
// the SHA-256 against the one given (required: the digest in the release's
// checksums.txt), `bootstrap` at the root, nothing outside the package root, an
// arm64 Linux executable, and the program it was built from, so that the
// notary's zip cannot be given as the writer's. cmd is that program: the
// command's directory name, `audit-writer-lambda`.
func loadPackage(field, src, sha, cmd string, rel *artifact.Release) (*releasePackage, error) {
	if src == "" {
		// No package is named: the release this library is, from its own build
		// information (or Release.Version), and its digest from the release's
		// checksums.txt unless the estate pinned one, which always wins.
		v, err := artifact.ReleaseOf(rel, libraryModule, readBuildInfo, field+".Package")
		if err != nil {
			return nil, fmt.Errorf("auditpulumi: %s.Package: %w", field, err)
		}
		src = artifact.AssetURL(baseOf(rel), v, cmd+"_"+v+"_linux_arm64.zip")
		if sha == "" {
			cp := artifact.Release{ResolveChecksums: true, Version: v}
			if rel != nil {
				cp.BaseURL = rel.BaseURL
			}
			rel = &cp
		}
	}
	if sha == "" && rel != nil && rel.ResolveChecksums {
		version := rel.Version
		if version == "" {
			version = versionFromName(src, cmd)
		}
		var err error
		if sha, err = artifact.ResolveSHA256(rel.BaseURL, version, fileName(src)); err != nil {
			return nil, fmt.Errorf("auditpulumi: %s.PackageSHA256: %w", field, err)
		}
	}
	if !shaRE.MatchString(sha) {
		return nil, fmt.Errorf("auditpulumi: %s.PackageSHA256 is required: the zip's SHA-256 in hex, from the release's checksums.txt "+
			"(64 hex digits); nothing is deployed that was not checked against it", field)
	}
	raw, err := artifact.Fetch(src, sha)
	if err != nil {
		return nil, fmt.Errorf("auditpulumi: %s.Package: %w", field, err)
	}
	sum := sha256.Sum256(raw)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), sha) {
		return nil, fmt.Errorf("auditpulumi: %s.Package %s has the SHA-256 %x, not the %s given", field, src, sum, strings.ToLower(sha))
	}
	if err := readBootstrap(field, raw, cmd); err != nil {
		return nil, err
	}
	artifact.Remember(sha, raw)
	p, err := artifact.WriteTemp("audit-package-", "package.zip", raw)
	if err != nil {
		return nil, fmt.Errorf("auditpulumi: %w", err)
	}
	return &releasePackage{
		Path: p, SHA256: strings.ToLower(sha), CodeSHA256: base64.StdEncoding.EncodeToString(sum[:]),
		Version: versionFromName(src, cmd), Name: fileName(src),
	}, nil
}

// fileName is the last element of a path or of a URL's path.
func fileName(src string) string {
	if u, err := url.Parse(src); err == nil && strings.Contains(src, "://") {
		src = u.Path
	}
	return path.Base(filepath.ToSlash(src))
}

var shaRE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// readBootstrap checks the zip: `bootstrap` at its root, an arm64 Linux
// executable, built from the command it is wanted as.
func readBootstrap(field string, raw []byte, cmd string) error {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return fmt.Errorf("auditpulumi: %s.Package is not a zip: %w", field, err)
	}
	var boot []byte
	for _, f := range zr.File {
		name := path.Clean(f.Name)
		if path.IsAbs(name) || strings.Contains(f.Name, "..") || strings.Contains(f.Name, "\\") {
			return fmt.Errorf("auditpulumi: %s.Package holds %q, which is outside the package root", field, f.Name)
		}
		if f.FileInfo().IsDir() || name != "bootstrap" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("auditpulumi: %s.Package: %w", field, err)
		}
		boot, err = io.ReadAll(io.LimitReader(rc, maxPackageBytes+1))
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("auditpulumi: %s.Package: %w", field, err)
		}
		if len(boot) > maxPackageBytes {
			return fmt.Errorf("auditpulumi: %s.Package: bootstrap is larger than %d bytes unzipped", field, maxPackageBytes)
		}
	}
	if len(boot) == 0 {
		return fmt.Errorf("auditpulumi: %s.Package has no `bootstrap` at its root: pass the release's %s_<version>_linux_arm64.zip", field, cmd)
	}
	ef, err := elf.NewFile(bytes.NewReader(boot))
	if err != nil {
		return fmt.Errorf("auditpulumi: %s.Package: bootstrap is not an executable Linux binary: %w", field, err)
	}
	if ef.Machine != elf.EM_AARCH64 {
		return fmt.Errorf("auditpulumi: %s.Package: bootstrap is built for %s, and the function is arm64: "+
			"pass the release's linux_arm64 zip", field, ef.Machine)
	}
	// The program is in the build information whatever the linker flags were. A
	// binary with none is not one the release made, and is left to the guards
	// that know whether they were asked.
	if bi, err := buildinfo.Read(bytes.NewReader(boot)); err == nil && !strings.HasSuffix(bi.Path, "/cmd/"+cmd) {
		return fmt.Errorf("auditpulumi: %s.Package holds %q and this field wants %s: the writer's and the notary's zips are not interchangeable",
			field, bi.Path, cmd)
	}
	return nil
}

var releaseNameRE = regexp.MustCompile(`^(audit-[a-z]+-lambda)_(.+)_linux_arm64\.zip$`)

// versionFromName is the release in a zip's file name, "" when the name is not
// the release's or is the other command's.
func versionFromName(src, cmd string) string {
	if u, err := url.Parse(src); err == nil && strings.Contains(src, "://") {
		src = u.Path
	}
	m := releaseNameRE.FindStringSubmatch(path.Base(filepath.ToSlash(src)))
	if m == nil || m[1] != cmd {
		return ""
	}
	return strings.TrimPrefix(m[2], "v")
}

// readBuildInfo is where the library reads its own release; a test replaces it.
var readBuildInfo = debug.ReadBuildInfo

func baseOf(rel *artifact.Release) string {
	if rel == nil {
		return ""
	}
	return rel.BaseURL
}
