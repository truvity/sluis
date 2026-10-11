package issuerfn_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/issuerfn"
)

// open starts the issuer function over the memory adapter from a document with
// the GitHub controller and extra sections.
func open(t *testing.T, extra string) *lambdaapp.Function {
	t.Helper()
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policy, []byte(`apiVersion: sluis.truvity.github.io/policy/v2
groups:
  all:access-roster:operator: { members: [platform@north.example] }
  all:access-roster:viewer: {}
github: { acme: { members: [all:access-roster:operator] } }
clients:
  console: { kind: public, requires: [all:access-roster:operator], redirects: ["https://access.example/console/callback"] }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sluis.yaml")
	if err := os.WriteFile(file, []byte(`apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: https://access.example
publicURL: https://access.example/console
policy: {file: `+policy+`}
listen: {address: ":0"}
probes: {address: ":0"}
ports: {adapter: memory}
adapters:
  secrets: {adapter: memory}
controllers:
  github: {consoleURL: "http://127.0.0.1:1/console"}
  slack: {consoleURL: "http://127.0.0.1:1/console"}
`+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	fn, err := lambdaapp.Open(context.Background(), func(k string) string {
		if k == config.EnvConfig {
			return file
		}
		return ""
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(fn.Close)
	return fn
}

func handle(fn *lambdaapp.Function, payload string) (any, error) {
	return fn.Handler.Handle(context.Background(), json.RawMessage(payload))
}

const everyRemote = `github: {remote: {url: "http://github.sluis.svc:8080"}}
slack: {remote: {url: "http://slack.sluis.svc:8080"}}
google: {remote: {url: "http://google.sluis.svc:8080"}}
`

// With every module another function's, the issuer function assembles no
// controller: a pass for a target the policy declares has nothing to run here.
func TestWithEveryRemoteSetTheIssuerAssemblesNoController(t *testing.T) {
	fn := open(t, everyRemote)
	_, err := handle(fn, `{"kind":"tick","target":"acme"}`)
	if err == nil || !strings.Contains(err.Error(), "declared by no controller this function runs") {
		t.Fatalf("tick for a declared organisation: %v, want it refused as run by no controller here", err)
	}
	// A run-now (which the console sends to the module's function, never here)
	// ends cleanly without a pass.
	out, err := handle(fn, `{"kind":"run","target":"acme"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res, ok := out.(lambdaapp.Result); !ok || res.Outcome != lambdaapp.OutcomeUnknown {
		t.Fatalf("run answered %#v, want the unknown outcome", out)
	}
}

// Without a remote the module stays in the process, as the demonstration and
// the tests have it: the controller is assembled for the pass.
func TestWithoutARemoteTheIssuerHostsTheController(t *testing.T) {
	fn := open(t, "")
	_, err := handle(fn, `{"kind":"tick","target":"acme"}`)
	if err != nil && strings.Contains(err.Error(), "declared by no controller this function runs") {
		t.Fatalf("the controller was not assembled: %v", err)
	}
}

// "clients" reconciles the client secrets only; "refresh" is accepted for one
// release, and with the directory remote it does not refresh it either.
func TestClientsAndTheDeprecatedRefreshEvent(t *testing.T) {
	fn := open(t, everyRemote)
	for _, kind := range []string{lambdaapp.KindClients, lambdaapp.KindRefresh} { //nolint:staticcheck // both kinds are under test
		out, err := handle(fn, `{"kind":"`+kind+`"}`)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		res, ok := out.(lambdaapp.RefreshResult)
		if !ok || res.Kind != kind || res.Workspaces != 0 || res.Ran != 0 {
			t.Errorf("%s answered %#v, want the reconcile alone", kind, out)
		}
	}
}
