package sluispulumi

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// LayerRoot is where the configuration layer is mounted in a function: Lambda
// extracts a layer under /opt, and the layer holds sluis/.
const LayerRoot = "/opt/sluis"

// DocumentPath is where a function finds a document of the configuration
// layer: its own service document (`http`, `github`, `slack`) or `policy`.
// SLUIS_CONFIG names the first; every service document's policy.file names
// the second.
func DocumentPath(name string) string { return LayerRoot + "/" + name + ".yaml" }

const maxPackageBytes = 100 << 20

// loadPackage reads the release zip, from a path or an https URL, holds it to
// the SHA-256 it must have, checks it is a function package (`bootstrap` at its
// root, no entry outside it), and returns a copy of exactly the bytes it held to
// the digest: a file of its own, named by the digest, in a directory of its own
// (0700, the file 0600). The function's code is that copy, never the caller's
// path, which could change between the check and the upload, and never a zip
// rebuilt here. The directory is not removed: the engine reads the file after
// this returns.
func loadPackage(src, sha string) (string, error) {
	raw, err := fetchPackage(src)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), strings.TrimSpace(sha)) {
		return "", fmt.Errorf("sluispulumi: the package %s has the SHA-256 %x, not the %s asked for", src, sum, sha)
	}
	if _, err = readZip(raw); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "sluis-package-")
	if err != nil {
		return "", fmt.Errorf("sluispulumi: %w", err)
	}
	local := filepath.Join(dir, hex.EncodeToString(sum[:])+".zip")
	if err = os.WriteFile(local, raw, 0o600); err != nil {
		return "", fmt.Errorf("sluispulumi: %w", err)
	}
	return local, nil
}

func fetchPackage(src string) ([]byte, error) {
	if !strings.Contains(src, "://") {
		raw, err := os.ReadFile(src)
		if err != nil {
			return nil, fmt.Errorf("sluispulumi: Package: %w", err)
		}
		return raw, nil
	}
	u, err := url.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("sluispulumi: Package: %w", err)
	}
	host := u.Hostname()
	loopback := host == "127.0.0.1" || host == "localhost" || host == "::1"
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, fmt.Errorf("sluispulumi: Package: %q is not an https URL", src)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(src)
	if err != nil {
		return nil, fmt.Errorf("sluispulumi: Package: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sluispulumi: Package: %s answered %s", src, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPackageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("sluispulumi: Package: %w", err)
	}
	if len(raw) > maxPackageBytes {
		return nil, fmt.Errorf("sluispulumi: Package: %s is larger than %d bytes", src, maxPackageBytes)
	}
	return raw, nil
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
