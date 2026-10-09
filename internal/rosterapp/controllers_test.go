package rosterapp_test

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/rosterapp"
)

// oneProcess is the one service document with both controllers, over the
// memory adapter, which needs no network.
func oneProcess(t *testing.T, controllers string) rosterapp.Config {
	t.Helper()
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policy, []byte(`apiVersion: sluis.truvity.github.io/policy/v2
groups:
  all:access-roster:operator: { members: [platform@north.example] }
  all:access-roster:viewer: {}
clients:
  console: { kind: public, requires: [all:access-roster:operator], redirects: ["https://access.example/console/callback"] }
`), 0o600); err != nil {
		t.Fatalf("write the policy: %v", err)
	}
	return load(t, `apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: https://access.example
publicURL: https://access.example/console
policy: {file: `+policy+`}
listen: {address: ":0"}
probes: {address: ":0"}
ports: {adapter: memory}
`+controllers)
}

// The controllers a document names are assembled with the service, and the
// service's readiness answers for them: nothing is ready until each controller
// has begun, and a deployment that names none has no such wait.
func TestReadinessAnswersForTheControllersToo(t *testing.T) {
	cfg := oneProcess(t, `controllers:
  github: {consoleURL: "http://127.0.0.1:1/console"}
  slack: {consoleURL: "http://127.0.0.1:1/console"}
`)
	if cfg.GitHub == nil || cfg.Slack == nil {
		t.Fatalf("the controllers were not read: %+v %+v", cfg.GitHub, cfg.Slack)
	}
	app, err := rosterapp.New(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(app.Close)

	if code, body := get(t, app.HealthHandler(), "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before the controllers began = %d, %q: it must wait for them", code, body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if code, _ := get(t, app.HealthHandler(), "/readyz"); code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("/readyz never answered ready after the controllers began")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the process did not stop when its context ended: a controller's loop outlived it")
	}
}

func TestNoControllersIsNoWait(t *testing.T) {
	cfg := oneProcess(t, "")
	if cfg.GitHub != nil || cfg.Slack != nil {
		t.Fatal("a controller the document does not name is on")
	}
	app, err := rosterapp.New(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(app.Close)
	if code, body := get(t, app.HealthHandler(), "/readyz"); code != http.StatusOK {
		t.Errorf("/readyz = %d, %q", code, body)
	}
}

// A controller refuses to start on what it must not act on, and so does the
// process: an enabled organisation the policy does not bind.
func TestAControllerThatCannotStartStopsTheProcess(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	body := "apiVersion: sluis.truvity.github.io/policy/v2\ncontrollers:\n  github: {enabledOrgs: [nobody]}\n"
	if err := os.WriteFile(policy, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sluis.yaml")
	doc := "apiVersion: sluis.truvity.github.io/sluis/v3\nissuerURL: https://access.example\npolicy: {file: " + policy + "}\n" +
		"ports: {adapter: memory}\ncontrollers: {github: {consoleURL: 'http://c:1'}}\n"
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := rosterapp.Load(file)
	if err == nil {
		_, err = rosterapp.New(context.Background(), cfg, slog.New(slog.DiscardHandler))
	}
	// Either the policy document or the controller refuses it, and says why.
	if err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Fatalf("the process started with a controller that refuses to, or the refusal does not name it: %v", err)
	}
}

// Controllers act on the policy the document names: with none there is
// nothing to act on, and the document is refused.
func TestControllersNeedThePolicyFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sluis.yaml")
	doc := "apiVersion: sluis.truvity.github.io/sluis/v3\nissuerURL: https://access.example\ncontrollers: {github: {consoleURL: 'http://c:1'}}\n"
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rosterapp.Load(file); err == nil {
		t.Fatal("controllers with no policy.file were accepted")
	}
}
