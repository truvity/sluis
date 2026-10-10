package rosterapp_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/rosterapp"
)

func bootWithPolicy(t *testing.T, clients string) *rosterapp.App {
	t.Helper()
	app, err := tryBootWithPolicy(t, clients, "ports: {adapter: memory}\nadapters:\n  secrets: {adapter: memory}\n")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(app.Close)
	return app
}

func tryBootWithPolicy(t *testing.T, clients, extra string) (*rosterapp.App, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(`
version: 1
groups:
  all:access-roster:operator: { members: [platform@north.example] }
  all:access-roster:viewer: {}
lifetimes: { default: 12h }
clients:
  console: { kind: public, requires: [all:access-roster:operator], redirects: ["https://access.example/console/callback"] }
`+clients), 0o600); err != nil {
		t.Fatalf("write the policy: %v", err)
	}
	cfg := load(t, `
issuerURL: https://access.example
publicURL: https://access.example/console
policyDir: `+dir+`
listen: {address: ":0"}
probes: {address: ":0"}
`+extra)
	return rosterapp.New(context.Background(), cfg, slog.New(slog.DiscardHandler))
}

// The function that has no loop runs the same pass on its schedule: it is the
// issuer's, reached through the service that joins the issuer to the
// directory.
func TestReconcileClientSecretsIsTheIssuersPass(t *testing.T) {
	app := bootWithPolicy(t, `  grafana: { kind: confidential, secret: { generate: true }, requires: [all:access-roster:operator] }
  argocd:  { kind: confidential, secret: argocd-oidc, requires: [all:access-roster:operator] }
`)
	// New makes no record; the first scheduled pass makes it and the next finds
	// it, for the generated client only.
	if res := app.ReconcileClientSecrets(context.Background()); res.Outcomes["grafana"] != clientcreds.OutcomeCreated {
		t.Fatalf("first pass = %+v", res)
	}
	res := app.ReconcileClientSecrets(context.Background())
	if len(res.Outcomes) != 1 || res.Outcomes["grafana"] != clientcreds.OutcomeExisting || res.Failed() != 0 {
		t.Errorf("res = %+v", res)
	}
}

func TestReconcileClientSecretsWithNoGeneratedClientDoesNothing(t *testing.T) {
	app := bootWithPolicy(t, "")
	if res := app.ReconcileClientSecrets(context.Background()); len(res.Outcomes) != 0 {
		t.Errorf("res = %+v", res)
	}
}

// Without a secrets adapter that can create only if absent the roster does not
// start with a generated client: the same refusal the issuer alone gives.
func TestAGeneratedClientIsRefusedWithNoSecretsAdapter(t *testing.T) {
	app, err := tryBootWithPolicy(t, `  grafana: { kind: confidential, secret: { generate: true }, requires: [all:access-roster:operator] }
`, "")
	if err == nil {
		app.Close()
		t.Fatal("started")
	}
	if !strings.Contains(err.Error(), "generate: true") {
		t.Errorf("the refusal is for another reason: %v", err)
	}
}

// A rotation and an orphan mark are serialised by a lease on the State; held in
// this process only it would not keep a second replica off, so start is refused.
func TestAGeneratedClientIsRefusedWhereTheStateIsNotShared(t *testing.T) {
	app, err := tryBootWithPolicy(t, `  grafana: { kind: confidential, secret: { generate: true }, requires: [all:access-roster:operator] }
`, "adapters:\n  secrets: {adapter: ssm, settings: {root: /sluis/test}}\n")
	if err == nil {
		app.Close()
		t.Fatal("started with a State that is only this process's")
	}
	if !strings.Contains(err.Error(), "the State is not shared between replicas") || !strings.Contains(err.Error(), `"grafana"`) {
		t.Errorf("refusal = %v", err)
	}
}

// Memory secrets are this process's too, so a State that is not shared has no
// second replica to exclude: the roster starts, and the pass finds the record.
func TestAGeneratedClientStartsWithMemorySecretsBesideANonSharedState(t *testing.T) {
	app, err := tryBootWithPolicy(t, `  grafana: { kind: confidential, secret: { generate: true }, requires: [all:access-roster:operator] }
`, "adapters:\n  secrets: {adapter: memory}\n")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(app.Close)
	if res := app.ReconcileClientSecrets(context.Background()); res.Outcomes["grafana"] != clientcreds.OutcomeCreated {
		t.Fatalf("first pass = %+v", res)
	}
	res := app.ReconcileClientSecrets(context.Background())
	if len(res.Outcomes) != 1 || res.Outcomes["grafana"] != clientcreds.OutcomeExisting {
		t.Errorf("res = %+v", res)
	}
}
