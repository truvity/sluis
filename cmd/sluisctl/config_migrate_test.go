package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	legacyMigration = sync.Once{}
	got, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdg, "sluisctl"); got != want {
		t.Fatalf("configDir() = %q, want %q", got, want)
	}
}

type legacyDirs struct{ root, old, nw string }

func newLegacyDirs(t *testing.T) legacyDirs {
	t.Helper()
	root := t.TempDir()
	return legacyDirs{root, filepath.Join(root, "accessctl"), filepath.Join(root, "sluisctl")}
}

// stagingDirs lists leftover staging directories beside the new dir.
func stagingDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagingPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		v := info.Mode().String()
		if info.Mode().IsRegular() {
			v += readTestFile(t, p)
		}
		m[p] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func equalSnapshots(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

var legacyWant = map[string]string{
	"config.yaml":                 "issuer: https://x\n",
	"sessions/a-1234.json":        `{"refresh_token":"r"}`,
	"credentials/aws/deep/c.json": "cred",
}

func TestMigrateLegacyConfigDirSuccess(t *testing.T) {
	d := newLegacyDirs(t)
	populateLegacy(t, d.old)
	before := snapshotTree(t, d.old)
	if exists(d.nw) {
		t.Fatal("new dir present before migration")
	}

	var out bytes.Buffer
	migrateLegacyConfigDir(d.old, d.nw, &out)

	for rel, want := range legacyWant {
		p := filepath.Join(d.nw, rel)
		if got := readTestFile(t, p); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
		if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", rel, info.Mode().Perm())
		}
	}
	for _, rel := range []string{".", "sessions", "credentials", "credentials/aws/deep"} {
		info, err := os.Stat(filepath.Join(d.nw, rel))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("dir %s mode = %o, want 700", rel, info.Mode().Perm())
		}
	}
	if !exists(filepath.Join(d.nw, legacyMigratedMarker)) {
		t.Error("marker missing")
	}
	if s := stagingDirs(t, d.root); len(s) > 0 {
		t.Errorf("staging left behind: %v", s)
	}
	if l := lines(out.String()); len(l) != 1 {
		t.Errorf("want exactly one output line, got %q", out.String())
	}
	if !equalSnapshots(before, snapshotTree(t, d.old)) {
		t.Error("old tree changed")
	}
}

func TestMigrateLegacyConfigDirCopyFailureRetries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	d := newLegacyDirs(t)
	populateLegacy(t, d.old)
	bad := filepath.Join(d.old, "sessions", "unreadable.json")
	writeTestFile(t, bad, "x", 0o000)

	var out bytes.Buffer
	migrateLegacyConfigDir(d.old, d.nw, &out)

	if exists(d.nw) {
		t.Error("new dir exists after a failed copy")
	}
	if s := stagingDirs(t, d.root); len(s) > 0 {
		t.Errorf("staging left behind: %v", s)
	}
	if l := lines(out.String()); len(l) != 1 || !strings.Contains(l[0], "will retry") {
		t.Errorf("want one warning containing %q, got %q", "will retry", out.String())
	}

	if err := os.Chmod(bad, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	migrateLegacyConfigDir(d.old, d.nw, &out)
	if got := readTestFile(t, filepath.Join(d.nw, "sessions", "unreadable.json")); got != "x" {
		t.Errorf("retry did not copy: %q", got)
	}
	if !exists(filepath.Join(d.nw, legacyMigratedMarker)) {
		t.Error("marker missing after retry")
	}
	if l := lines(out.String()); len(l) != 1 || strings.Contains(l[0], "will retry") {
		t.Errorf("want the success line only, got %q", out.String())
	}
}

func TestMigrateLegacyConfigDirNoRegularFilesIsNoop(t *testing.T) {
	d := newLegacyDirs(t)
	if err := os.MkdirAll(filepath.Join(d.old, "empty-sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(d.root, "elsewhere")
	writeTestFile(t, target, "t", 0o600)
	if err := os.Symlink(target, filepath.Join(d.old, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var out bytes.Buffer
	migrateLegacyConfigDir(d.old, d.nw, &out)
	if out.Len() != 0 || exists(d.nw) {
		t.Errorf("want no-op; output %q, new exists %v", out.String(), exists(d.nw))
	}
	if s := stagingDirs(t, d.root); len(s) > 0 {
		t.Errorf("staging left behind: %v", s)
	}
}

func TestMigrateLegacyConfigDirNonEmptyNewIsUntouched(t *testing.T) {
	for name, files := range map[string][]string{
		"other file":  {"other"},
		"marker only": {legacyMigratedMarker},
	} {
		t.Run(name, func(t *testing.T) {
			d := newLegacyDirs(t)
			populateLegacy(t, d.old)
			for _, f := range files {
				writeTestFile(t, filepath.Join(d.nw, f), "mine", 0o600)
			}
			before := snapshotTree(t, d.nw)
			var out bytes.Buffer
			migrateLegacyConfigDir(d.old, d.nw, &out)
			if out.Len() != 0 {
				t.Errorf("unexpected output %q", out.String())
			}
			if !equalSnapshots(before, snapshotTree(t, d.nw)) {
				t.Error("new dir changed")
			}
			if s := stagingDirs(t, d.root); len(s) > 0 {
				t.Errorf("staging left behind: %v", s)
			}
		})
	}
}

func TestMigrateLegacyConfigDirEmptyNewMigrates(t *testing.T) {
	d := newLegacyDirs(t)
	populateLegacy(t, d.old)
	if err := os.MkdirAll(d.nw, 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	migrateLegacyConfigDir(d.old, d.nw, &out)
	for rel, want := range legacyWant {
		if got := readTestFile(t, filepath.Join(d.nw, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if l := lines(out.String()); len(l) != 1 || strings.Contains(l[0], "could not") {
		t.Errorf("want one success line, got %q", out.String())
	}
	if s := stagingDirs(t, d.root); len(s) > 0 {
		t.Errorf("staging left behind: %v", s)
	}
}

func TestMigrateLegacyConfigDirNeitherExists(t *testing.T) {
	d := newLegacyDirs(t)
	var out bytes.Buffer
	migrateLegacyConfigDir(d.old, d.nw, &out)
	if out.Len() != 0 || exists(d.nw) {
		t.Errorf("want no-op; output %q, new exists %v", out.String(), exists(d.nw))
	}
}

func TestMigrateLegacyConfigDirEmptyOldIsNoop(t *testing.T) {
	d := newLegacyDirs(t)
	if err := os.MkdirAll(d.old, 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	migrateLegacyConfigDir(d.old, d.nw, &out)
	if out.Len() != 0 || exists(d.nw) {
		t.Errorf("want no-op; output %q, new exists %v", out.String(), exists(d.nw))
	}
}

func TestMigrateLegacyConfigDirFollowsSymlinkedOldDirSkipsInnerLinks(t *testing.T) {
	d := newLegacyDirs(t)
	realOld := filepath.Join(d.root, "realOld-accessctl")
	populateLegacy(t, realOld)
	outside := filepath.Join(d.root, "outside")
	writeTestFile(t, filepath.Join(outside, "secret"), "s", 0o600)
	if err := os.Symlink(realOld, d.old); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(realOld, "link-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(realOld, "link-dir")); err != nil {
		t.Fatal(err)
	}

	migrateLegacyConfigDir(d.old, d.nw, &bytes.Buffer{})

	for rel, want := range legacyWant {
		if got := readTestFile(t, filepath.Join(d.nw, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	for _, name := range []string{"link-file", "link-dir"} {
		if exists(filepath.Join(d.nw, name)) {
			t.Errorf("%s was migrated", name)
		}
	}
}

func TestRemoveStaleStaging(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, stagingPrefix+"stale")
	fresh := filepath.Join(root, stagingPrefix+"fresh")
	unrelated := filepath.Join(root, "keepme")
	for _, p := range []string{stale, fresh, unrelated} {
		writeTestFile(t, filepath.Join(p, "f"), "x", 0o600)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unrelated, old, old); err != nil {
		t.Fatal(err)
	}
	removeStaleStaging(root, time.Hour)
	if exists(stale) {
		t.Error("stale staging dir kept")
	}
	if !exists(fresh) {
		t.Error("fresh staging dir removed")
	}
	if !exists(unrelated) {
		t.Error("unrelated old dir removed")
	}
}

func TestMigrateLegacyConfigDirRemovesStaleStagingOnAttempt(t *testing.T) {
	d := newLegacyDirs(t)
	populateLegacy(t, d.old)
	stale := filepath.Join(d.root, stagingPrefix+"crashed")
	fresh := filepath.Join(d.root, stagingPrefix+"inflight")
	writeTestFile(t, filepath.Join(stale, "f"), "x", 0o600)
	writeTestFile(t, filepath.Join(fresh, "f"), "x", 0o600)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	migrateLegacyConfigDir(d.old, d.nw, &bytes.Buffer{})
	if exists(stale) {
		t.Error("stale staging dir kept")
	}
	if !exists(fresh) {
		t.Error("fresh staging dir removed")
	}
}

func TestMigrateLegacyConfigDirMarkerAndDeletion(t *testing.T) {
	d := newLegacyDirs(t)
	populateLegacy(t, d.old)
	migrateLegacyConfigDir(d.old, d.nw, &bytes.Buffer{})

	var out bytes.Buffer
	migrateLegacyConfigDir(d.old, d.nw, &out)
	if out.Len() != 0 {
		t.Errorf("second call wrote %q", out.String())
	}

	// A sign-out that leaves only the marker is not undone.
	entries, err := os.ReadDir(d.nw)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != legacyMigratedMarker {
			if err := os.RemoveAll(filepath.Join(d.nw, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	migrateLegacyConfigDir(d.old, d.nw, &out)
	if out.Len() != 0 || exists(filepath.Join(d.nw, "config.yaml")) || exists(filepath.Join(d.nw, "sessions")) {
		t.Errorf("state came back with the marker present; output %q", out.String())
	}

	// Deleting the whole directory re-runs the migration.
	if err := os.RemoveAll(d.nw); err != nil {
		t.Fatal(err)
	}
	migrateLegacyConfigDir(d.old, d.nw, &out)
	if got := readTestFile(t, filepath.Join(d.nw, "config.yaml")); got != legacyWant["config.yaml"] {
		t.Errorf("not re-migrated after deleting the dir: %q", got)
	}
}

// captureStderr runs f with os.Stderr redirected and returns what was written.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = tmp
	defer func() { os.Stderr = saved }()
	f()
	_ = tmp.Close()
	return readTestFile(t, tmp.Name())
}

func TestConfigDirMigratesAtMostOncePerProcess(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	populateLegacy(t, filepath.Join(xdg, "accessctl"))
	legacyMigration = sync.Once{}

	got := captureStderr(t, func() {
		for i := 0; i < 2; i++ {
			if _, err := configDir(); err != nil {
				t.Fatal(err)
			}
		}
	})
	if l := lines(got); len(l) != 1 {
		t.Errorf("want one stderr line over two calls, got %q", got)
	}

	// Even with the directory deleted, the Once holds until it is reset.
	if err := os.RemoveAll(filepath.Join(xdg, "sluisctl")); err != nil {
		t.Fatal(err)
	}
	got = captureStderr(t, func() { _, _ = configDir() })
	if got != "" || exists(filepath.Join(xdg, "sluisctl")) {
		t.Errorf("migrated twice in one process; stderr %q", got)
	}

	legacyMigration = sync.Once{}
	got = captureStderr(t, func() { _, _ = configDir() })
	if len(lines(got)) != 1 || !exists(filepath.Join(xdg, "sluisctl", "config.yaml")) {
		t.Errorf("reset Once did not re-run; stderr %q", got)
	}
}

func TestCopyFileRefusesNonRegularSourceAndExistingDest(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeTestFile(t, src, "data", 0o644)

	dst := filepath.Join(root, "dst")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dst); info.Mode().Perm() != 0o600 {
		t.Errorf("dst mode = %o, want 600", info.Mode().Perm())
	}
	if got := readTestFile(t, dst); got != "data" {
		t.Errorf("dst = %q", got)
	}

	writeTestFile(t, dst, "mine", 0o600)
	if err := copyFile(src, dst); err == nil {
		t.Error("copyFile overwrote an existing destination without error")
	}
	if got := readTestFile(t, dst); got != "mine" {
		t.Errorf("existing destination changed: %q", got)
	}

	if err := copyFile(root, filepath.Join(root, "d2")); err == nil {
		t.Error("copyFile accepted a directory")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(src, link); err == nil {
		if err := copyFile(link, filepath.Join(root, "d3")); err == nil {
			t.Error("copyFile accepted a symlink")
		}
	}
}

func TestCopyTreeCopiesFilesAndDirsOnly(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	writeTestFile(t, filepath.Join(src, "a", "b"), "b", 0o644)
	if err := os.Mkdir(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(filepath.Join(src, "a", "b"), filepath.Join(src, "ln"))
	n, err := copyTree(src, dst)
	if err != nil || n != 1 {
		t.Fatalf("copyTree = %d, %v; want 1, nil", n, err)
	}
	if readTestFile(t, filepath.Join(dst, "a", "b")) != "b" || exists(filepath.Join(dst, "ln")) {
		t.Error("unexpected tree content")
	}
}

// A session saved only under the old directory is found by the normal load
// path once configDir() has migrated it.
func TestConfigDirMigrationFeedsLoadSession(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	legacyMigration = sync.Once{}
	const issuer = "https://sluis.example.test"
	oldSession := filepath.Join(xdg, "accessctl", "sessions", sessionFileName(issuer)+".json")
	writeTestFile(t, oldSession,
		`{"refresh_token":"rt-old","email":"a@example.test","issuer":"`+issuer+`"}`, 0o600)

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
