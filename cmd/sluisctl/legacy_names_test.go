package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// An issuer that answers the way the real one does: invalid_client for a
// client it does not know, invalid_grant (the code is wrong) for one it does.
func issuerKnowing(t *testing.T, known ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		for _, id := range known {
			if r.PostForm.Get("client_id") == id {
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
		}
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"unknown client"}`))
	}))
	t.Cleanup(server.Close)
	return server, &probes
}

// The new id is tried first and kept when the issuer knows it.
func TestTheNewClientIsUsedWhenTheIssuerKnowsIt(t *testing.T) {
	server, probes := issuerKnowing(t, "sluisctl", "accessctl")
	var notice bytes.Buffer
	if got := chooseDefaultClient(context.Background(), server.URL, &notice); got != "sluisctl" {
		t.Errorf("client = %q, want sluisctl", got)
	}
	if notice.Len() != 0 || probes.Load() != 1 {
		t.Errorf("notice %q, probes %d: want none and one", notice.String(), probes.Load())
	}
}

// An issuer that does not know sluisctl is asked once, then accessctl is used,
// with a notice on the writer it was given (stderr).
func TestTheLegacyClientIsUsedOnceWhenTheNewOneIsUnknown(t *testing.T) {
	server, probes := issuerKnowing(t, "accessctl")
	var notice bytes.Buffer
	if got := chooseDefaultClient(context.Background(), server.URL, &notice); got != "accessctl" {
		t.Errorf("client = %q, want accessctl", got)
	}
	if probes.Load() != 1 {
		t.Errorf("probes = %d, want exactly one: the fallback is not itself probed", probes.Load())
	}
	if !strings.Contains(notice.String(), "deprecated") || strings.Count(notice.String(), "\n") != 1 {
		t.Errorf("want a one-line deprecation notice, got %q", notice.String())
	}
}

// An issuer that cannot be reached does not change the client: the sign-in
// reports the failure itself.
func TestAnUnreachableIssuerKeepsTheNewClient(t *testing.T) {
	server, _ := issuerKnowing(t)
	server.Close()
	var notice bytes.Buffer
	if got := chooseDefaultClient(context.Background(), server.URL, &notice); got != "sluisctl" || notice.Len() != 0 {
		t.Errorf("client %q notice %q, want sluisctl and none", got, notice.String())
	}
}

// An id named by --client or by config.yaml is final: it is not defaulted, so
// login never probes or falls back.
func TestAnExplicitClientIsNeverDefaulted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv(envIssuer, "")

	cfg, err := loadLoginConfig("https://issuer.example", "mine")
	if err != nil || cfg.ClientID != "mine" || cfg.ClientDefaulted {
		t.Fatalf("flag: %+v, %v", cfg, err)
	}

	dir := filepath.Join(home, "config", "sluisctl")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("clientId: accessctl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadLoginConfig("https://issuer.example", "")
	if err != nil || cfg.ClientID != "accessctl" || cfg.ClientDefaulted {
		t.Fatalf("config: %+v, %v", cfg, err)
	}

	if err = os.Remove(filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadLoginConfig("https://issuer.example", "")
	if err != nil || cfg.ClientID != DefaultClientID || !cfg.ClientDefaulted {
		t.Fatalf("default: %+v, %v", cfg, err)
	}
}

// ---- kubeconfig ----

// fakeKube is a kubeconfig in memory, driven through the kubectl calls the
// code makes.
type fakeKube struct {
	users    map[string][]string // name -> exec args
	contexts map[string]string   // context -> user
	calls    []string
}

func (f *fakeKube) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[1] {
	case "view":
		var users, contexts []map[string]any
		for name, a := range f.users {
			users = append(users, map[string]any{"name": name, "user": map[string]any{"exec": map[string]any{"args": a}}})
		}
		for name, user := range f.contexts {
			contexts = append(contexts, map[string]any{"name": name, "context": map[string]any{"user": user}})
		}
		return json.Marshal(map[string]any{"users": users, "contexts": contexts})
	case "set-credentials":
		f.users[args[2]] = args[3:]
	case "set-context":
		if f.contexts == nil {
			f.contexts = map[string]string{}
		}
		for _, a := range args[3:] {
			if u, ok := strings.CutPrefix(a, "--user="); ok {
				f.contexts[args[2]] = u
			}
		}
	case "unset":
		delete(f.users, strings.TrimPrefix(args[2], "users."))
	}
	return nil, nil
}

func withFakeKube(t *testing.T, f *fakeKube) {
	t.Helper()
	old := runKubectl
	runKubectl = f.run
	t.Cleanup(func() { runKubectl = old })
	t.Setenv("HOME", t.TempDir())
}

func TestTheLegacyKubeUserIsRenamedAndItsContextsFollow(t *testing.T) {
	old := []string{"--exec-command=/old/accessctl", "kube-token", "--audience=k8s:one", "--issuer=https://issuer.example"}
	f := &fakeKube{
		users: map[string][]string{
			"accessctl:one": old,
			"someone-else":  {"--exec-command=other"},
			"accessctl:raw": {"--exec-command=other"}, // not ours: no kube-token
		},
		contexts: map[string]string{"one": "accessctl:one", "one-alias": "accessctl:one", "plain": "someone-else"},
	}
	withFakeKube(t, f)

	if err := renameLegacyKubeUsers("/new/sluisctl", "https://other.example"); err != nil {
		t.Fatal(err)
	}
	if _, still := f.users["accessctl:one"]; still {
		t.Error("the old user is still there")
	}
	args := strings.Join(f.users["sluisctl:one"], " ")
	if !strings.Contains(args, "--exec-command=/new/sluisctl") || !strings.Contains(args, "--audience=k8s:one") ||
		!strings.Contains(args, "--issuer=https://issuer.example") {
		t.Errorf("the new user lost what the old one had: %s", args)
	}
	for ctx, want := range map[string]string{"one": "sluisctl:one", "one-alias": "sluisctl:one", "plain": "someone-else"} {
		if f.contexts[ctx] != want {
			t.Errorf("context %s -> %q, want %q", ctx, f.contexts[ctx], want)
		}
	}
	if _, kept := f.users["someone-else"]; !kept {
		t.Error("another user was touched")
	}
	if _, kept := f.users["accessctl:raw"]; !kept {
		t.Error("an accessctl: user that does not run kube-token was renamed")
	}

	// Again: nothing is left to rename, so nothing is written.
	f.calls = nil
	if err := renameLegacyKubeUsers("/new/sluisctl", "https://other.example"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "view") {
		t.Errorf("a second run wrote something: %v", f.calls)
	}
}

// ---- known_hosts ----

func knownHostsFixture(t *testing.T, old, current string) (dir string) {
	t.Helper()
	home := t.TempDir()
	dir = filepath.Join(home, ".ssh", "known_hosts.d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"accessctl": old, "sluisctl": current} {
		if body != "" {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil { //nolint:gosec // test
				t.Fatal(err)
			}
		}
	}
	return dir
}

func TestKnownHostsMigrationWithOnlyTheOldFile(t *testing.T) {
	dir := knownHostsFixture(t, "@cert-authority *.x ssh-ed25519 AAA\n", "")
	var said bytes.Buffer
	migrateLegacyKnownHosts(filepath.Dir(filepath.Dir(dir)), &said)

	got, err := os.ReadFile(filepath.Join(dir, "sluisctl"))
	if err != nil || string(got) != "@cert-authority *.x ssh-ed25519 AAA\n" {
		t.Fatalf("new file: %q, %v", got, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "accessctl")); err != nil {
		t.Errorf("the old file must stay: %v", err)
	}
	if marker, err := os.ReadFile(filepath.Join(dir, ".migrated-from-accessctl")); err != nil || strings.Count(string(marker), "\n") != 1 {
		t.Errorf("marker: %q, %v", marker, err)
	}
	if said.Len() == 0 {
		t.Error("the copy was silent")
	}

	// The user deletes the new file; the marker keeps the old one from coming back.
	_ = os.Remove(filepath.Join(dir, "sluisctl"))
	said.Reset()
	migrateLegacyKnownHosts(filepath.Dir(filepath.Dir(dir)), &said)
	if _, err = os.Stat(filepath.Join(dir, "sluisctl")); err == nil || said.Len() != 0 {
		t.Errorf("migrated again after the marker: %v %q", err, said.String())
	}
}

func TestKnownHostsMigrationWithBothFilesChangesNothing(t *testing.T) {
	dir := knownHostsFixture(t, "old\n", "new\n")
	var said bytes.Buffer
	migrateLegacyKnownHosts(filepath.Dir(filepath.Dir(dir)), &said)

	got, _ := os.ReadFile(filepath.Join(dir, "sluisctl"))
	if string(got) != "new\n" || said.Len() != 0 {
		t.Errorf("new file %q, output %q", got, said.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".migrated-from-accessctl")); err == nil {
		t.Error("a marker was written though nothing was migrated")
	}
}

func TestKnownHostsMigrationWithNeitherFileDoesNothing(t *testing.T) {
	home := t.TempDir()
	var said bytes.Buffer
	migrateLegacyKnownHosts(home, &said)
	if said.Len() != 0 {
		t.Errorf("output %q", said.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh")); err == nil {
		t.Error("created ~/.ssh for nothing")
	}
}

func TestTheHintSaysWhenSSHConfigStillNamesTheOldFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "Host *\n  UserKnownHostsFile ~/.ssh/known_hosts ~/.ssh/known_hosts.d/accessctl\n"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	hint := sshConfigHint(home, filepath.Join(home, ".ssh", "known_hosts.d", "sluisctl"))
	if !strings.Contains(hint, "still names the old file") || !strings.Contains(hint, "known_hosts.d/sluisctl") {
		t.Errorf("hint: %q", hint)
	}
}
