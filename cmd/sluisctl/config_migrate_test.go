package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// populateLegacy builds an old directory with nested trees and mixed modes.
func populateLegacy(t *testing.T, old string) {
	t.Helper()
	writeTestFile(t, filepath.Join(old, "config.yaml"), "issuer: https://x\n", 0o600)
	writeTestFile(t, filepath.Join(old, "sessions", "a-1234.json"), `{"refresh_token":"r"}`, 0o644)
	writeTestFile(t, filepath.Join(old, "credentials", "aws", "deep", "c.json"), "cred", 0o600)
}

func lines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestConfigDirIsSluisctlUnderUserConfigDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdg, "sluisctl"); got != want {
		t.Fatalf("configDir() = %q, want %q", got, want)
	}
}

func TestMigrateLegacyConfigDirCopiesTree(t *testing.T) {
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	populateLegacy(t, old)

	var out bytes.Buffer
	migrateLegacyConfigDir(old, nw, &out)

	for rel, want := range map[string]string{
		"config.yaml":                 "issuer: https://x\n",
		"sessions/a-1234.json":        `{"refresh_token":"r"}`,
		"credentials/aws/deep/c.json": "cred",
	} {
		if got := readTestFile(t, filepath.Join(nw, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if !exists(filepath.Join(nw, legacyMigratedMarker)) {
		t.Error("marker missing")
	}
	if l := lines(out.String()); len(l) != 1 {
		t.Errorf("want exactly one output line, got %q", out.String())
	}
}

func TestMigrateLegacyConfigDirLeavesOldUnchanged(t *testing.T) {
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	populateLegacy(t, old)
	snapshot := func() map[string]string {
		m := map[string]string{}
		_ = filepath.Walk(old, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				t.Fatal(err)
			}
			v := info.Mode().String()
			if info.Mode().IsRegular() {
				v += readTestFile(t, p)
			}
			m[p] = v
			return nil
		})
		return m
	}
	before := snapshot()
	migrateLegacyConfigDir(old, nw, &bytes.Buffer{})
	after := snapshot()
	if len(before) != len(after) {
		t.Fatalf("old tree changed: %v -> %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed: %q -> %q", k, v, after[k])
		}
	}
}

func TestMigrateLegacyConfigDirModes(t *testing.T) {
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	populateLegacy(t, old)
	migrateLegacyConfigDir(old, nw, &bytes.Buffer{})

	for rel, want := range map[string]os.FileMode{
		"config.yaml":                 0o600,
		"sessions/a-1234.json":        0o600, // was 0644
		"credentials/aws/deep/c.json": 0o600,
		"sessions":                    0o700,
		"credentials/aws/deep":        0o700,
		".":                           0o700,
	} {
		info, err := os.Stat(filepath.Join(nw, rel))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", rel, got, want)
		}
	}
}

func TestMigrateLegacyConfigDirNonEmptyNewIsUntouched(t *testing.T) {
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	populateLegacy(t, old)
	writeTestFile(t, filepath.Join(nw, "other"), "mine", 0o600)

	var out bytes.Buffer
	migrateLegacyConfigDir(old, nw, &out)

	if out.Len() != 0 {
		t.Errorf("unexpected output %q", out.String())
	}
	if exists(filepath.Join(nw, legacyMigratedMarker)) || exists(filepath.Join(nw, "config.yaml")) || exists(filepath.Join(nw, "sessions")) {
		t.Error("migration ran into a non-empty directory")
	}
}

func TestMigrateLegacyConfigDirEmptyNewMigrates(t *testing.T) {
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	populateLegacy(t, old)
	if err := os.MkdirAll(nw, 0o700); err != nil {
		t.Fatal(err)
	}
	migrateLegacyConfigDir(old, nw, &bytes.Buffer{})
	if got := readTestFile(t, filepath.Join(nw, "config.yaml")); got != "issuer: https://x\n" {
		t.Errorf("config.yaml = %q", got)
	}
}

func TestCopyFileNoClobberKeepsExisting(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	writeTestFile(t, src, "old", 0o600)
	writeTestFile(t, dst, "new", 0o600)
	ok, err := copyFileNoClobber(src, dst)
	if err != nil || ok {
		t.Fatalf("copyFileNoClobber = %v, %v; want false, nil", ok, err)
	}
	if got := readTestFile(t, dst); got != "new" {
		t.Errorf("dst overwritten: %q", got)
	}
}

func TestMigrateLegacyConfigDirDoesNotOverwriteWithinTree(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	writeTestFile(t, filepath.Join(src, "a"), "old-a", 0o600)
	writeTestFile(t, filepath.Join(src, "b"), "old-b", 0o600)
	writeTestFile(t, filepath.Join(dst, "a"), "new-a", 0o600)
	n, err := copyTreeNoClobber(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("copied %d, want 1", n)
	}
	if readTestFile(t, filepath.Join(dst, "a")) != "new-a" || readTestFile(t, filepath.Join(dst, "b")) != "old-b" {
		t.Error("unexpected content after no-clobber copy")
	}
}

func TestMigrateLegacyConfigDirNeitherExists(t *testing.T) {
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	var out bytes.Buffer
	migrateLegacyConfigDir(old, nw, &out)
	if out.Len() != 0 || exists(nw) {
		t.Errorf("want no-op; output %q, new exists %v", out.String(), exists(nw))
	}
}

func TestMigrateLegacyConfigDirEmptyOldIsNoop(t *testing.T) {
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	migrateLegacyConfigDir(old, nw, &out)
	if out.Len() != 0 || exists(nw) {
		t.Errorf("want no-op; output %q, new exists %v", out.String(), exists(nw))
	}
}

func TestMigrateLegacyConfigDirSecondCallIsNoop(t *testing.T) {
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	populateLegacy(t, old)
	migrateLegacyConfigDir(old, nw, &bytes.Buffer{})

	var out bytes.Buffer
	migrateLegacyConfigDir(old, nw, &out)
	if out.Len() != 0 {
		t.Errorf("second call wrote %q", out.String())
	}

	// A sign-out that empties the directory except for the marker must not
	// bring the old state back.
	entries, err := os.ReadDir(nw)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != legacyMigratedMarker {
			if err := os.RemoveAll(filepath.Join(nw, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	migrateLegacyConfigDir(old, nw, &out)
	if out.Len() != 0 || exists(filepath.Join(nw, "config.yaml")) || exists(filepath.Join(nw, "sessions")) {
		t.Errorf("state came back after sign-out; output %q", out.String())
	}
}

func TestMigrateLegacyConfigDirSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	populateLegacy(t, old)
	outside := filepath.Join(root, "outside")
	writeTestFile(t, filepath.Join(outside, "secret"), "s", 0o600)
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(old, "link-file")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(old, "link-dir")); err != nil {
		t.Fatal(err)
	}
	migrateLegacyConfigDir(old, nw, &bytes.Buffer{})

	for _, name := range []string{"link-file", "link-dir"} {
		if exists(filepath.Join(nw, name)) {
			t.Errorf("%s was migrated", name)
		}
	}
	if !exists(filepath.Join(nw, "config.yaml")) {
		t.Error("regular files not copied")
	}
}

func TestMigrateLegacyConfigDirUnreadableSourceWarns(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	populateLegacy(t, old)
	bad := filepath.Join(old, "sessions", "unreadable.json")
	writeTestFile(t, bad, "x", 0o000)

	var out bytes.Buffer
	migrateLegacyConfigDir(old, nw, &out)

	if !strings.Contains(out.String(), "incomplete") {
		t.Errorf("want an incomplete-migration warning, got %q", out.String())
	}
	if exists(filepath.Join(nw, "sessions", "unreadable.json")) {
		t.Error("unreadable file appeared in the new directory")
	}
	if got := readTestFile(t, filepath.Join(nw, "config.yaml")); got != "issuer: https://x\n" {
		t.Errorf("readable files not copied: %q", got)
	}
}

func TestMigrateLegacyConfigDirLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	old, nw := filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")
	populateLegacy(t, old)
	migrateLegacyConfigDir(old, nw, &bytes.Buffer{})

	var stray []string
	_ = filepath.Walk(nw, func(p string, _ os.FileInfo, _ error) error {
		if strings.HasPrefix(filepath.Base(p), ".migrate-") {
			stray = append(stray, p)
		}
		return nil
	})
	if len(stray) > 0 {
		t.Errorf("temp files left behind: %v", stray)
	}
}

// A session saved only under the old directory is found by the normal load
// path once configDir() has migrated it.
func TestConfigDirMigrationFeedsLoadSession(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	const issuer = "https://sluis.example.test"
	oldSession := filepath.Join(xdg, "accessctl", "sessions", sessionFileName(issuer)+".json")
	writeTestFile(t, oldSession,
		`{"refresh_token":"rt-old","email":"a@example.test","issuer":"`+issuer+`","expires":"`+time.Now().Add(time.Hour).Format(time.RFC3339)+`"}`,
		0o600)

	got, err := loadSession(issuer)
	if err != nil {
		t.Fatalf("loadSession: %v", err)
	}
	if got.RefreshToken != "rt-old" || got.Email != "a@example.test" {
		t.Errorf("session = %+v", got)
	}
	if !exists(filepath.Join(xdg, "sluisctl", "sessions", sessionFileName(issuer)+".json")) {
		t.Error("session not present under the new directory")
	}
}
