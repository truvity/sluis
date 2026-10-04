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
	"sort"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Where the package puts what the functions read. /var/task is the root of a
// function's package.
const (
	packageRoot = "/var/task"
	configDir   = "config"
	// The name is joined so that a scan for emitted action names does not take it
	// for one.
	configName = "sluis" + ".yaml"
)

// ConfigFilePath is where the package holds the estate's configuration, which is
// what SLUIS_CONFIG_FILE names.
const ConfigFilePath = packageRoot + "/" + configDir + "/" + configName

// maxPackageBytes bounds a zip fetched from a URL: a Lambda package is 250 MB
// unzipped at most, and a sluis release is a few MB.
const maxPackageBytes = 100 << 20

// loadPackage reads the released zip, from a path or an https URL, and returns
// its entries by path with the bytes of each. It checks the digest when one is
// given, refuses a zip with a path that leaves the package root, and requires
// `bootstrap` at the root.
func loadPackage(src, sha string) (map[string]zipEntry, error) {
	raw, err := fetchPackage(src)
	if err != nil {
		return nil, err
	}
	if sha != "" {
		sum := sha256.Sum256(raw)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), sha) {
			return nil, fmt.Errorf("sluispulumi: the package %s has the SHA-256 %x, not the %s asked for", src, sum, sha)
		}
	}
	return readZip(raw)
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

// buildArchive is the function package: every entry of the released zip, then
// the estate's configuration at config/sluis.yaml and the catalogue files at
// config/<name>. The added files are part of the package, so a change to any of
// them changes the archive's hash and redeploys the functions on the next
// `pulumi up`: a configuration change reaches the functions deliberately, as a
// deploy.
//
// The entries are written under a directory of the system's temporary files
// that Pulumi reads when it registers the function (a Pulumi asset is a path or a
// string, and a string loses the executable bit `bootstrap` needs); the
// directory is not removed, because the engine reads it after this returns.
func buildArchive(entries map[string]zipEntry, added map[string]string) (pulumi.Archive, error) {
	dir, err := os.MkdirTemp("", "sluis-package-")
	if err != nil {
		return nil, fmt.Errorf("sluispulumi: %w", err)
	}
	assets := map[string]any{}
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, over := added[n]; over {
			continue
		}
		e := entries[n]
		mode := e.mode
		if mode&0o700 == 0 {
			mode = 0o644
		}
		if n == "bootstrap" {
			mode = 0o755
		}
		p := filepath.Join(dir, filepath.FromSlash(n))
		if !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return nil, fmt.Errorf("sluispulumi: Package holds %q, which is outside the package root", n)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, fmt.Errorf("sluispulumi: %w", err)
		}
		if err := os.WriteFile(p, e.body, mode); err != nil {
			return nil, fmt.Errorf("sluispulumi: %w", err)
		}
		assets[n] = pulumi.NewFileAsset(p)
	}
	for n, body := range added {
		assets[n] = pulumi.NewStringAsset(body)
	}
	return pulumi.NewAssetArchive(assets), nil
}
