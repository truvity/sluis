package rosterapp_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/rosterapp"
)

// The merged service wires ONE switch: the console's turn-off reaches the
// console's own recovery door and the issuer's, and it is a Connect call under
// the console's mount that a person signed in through the identity provider
// (here, a gateway's forwarded address) makes.

func bootWithForwardedOperator(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(`
version: 1
groups:
  all:access-roster:operator: { matchers: [{ email: platform@north.example }] }
  all:access-roster:viewer: { matchers: [{ email: viewer@north.example }] }
lifetimes: { default: 12h }
clients:
  console: { kind: public, requires: [all:access-roster:operator], redirects: ["https://access.example/console/callback"] }
`), 0o600); err != nil {
		t.Fatalf("write the policy: %v", err)
	}
	cfg := load(t, `apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: https://access.example
publicURL: https://access.example/console
policy: {file: `+filepath.Join(dir, "policy.yaml")+`}
login: { forwarded: { emailHeader: X-Forwarded-Email } }
ports: { adapter: memory }
recovery: { enabled: true, generation: "2026-10" }
listen: {address: ":0"}
probes: {address: ":0"}
`)
	app, err := rosterapp.New(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(app.Close)
	return app.Handler()
}

func callAs(t *testing.T, handler http.Handler, email, method string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/console/sluis.v1.AccessService/"+method, strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	if email != "" {
		request.Header.Set("X-Forwarded-Email", email)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return response.Code, body
}

func TestTheConsoleTurnsRecoveryOffThroughTheMergedService(t *testing.T) {
	t.Parallel()
	handler := bootWithForwardedOperator(t)

	if code, body := callAs(t, handler, "platform@north.example", "GetRecovery"); code != http.StatusOK ||
		body["configured"] != true || body["effective"] != true || body["canDisable"] != true || body["generation"] != "2026-10" {
		t.Fatalf("GetRecovery before = %d %v", code, body)
	}
	// A viewer is not an operator; nobody is not signed in.
	if code, _ := callAs(t, handler, "viewer@north.example", "DisableRecovery"); code != http.StatusForbidden {
		t.Errorf("a viewer's DisableRecovery = %d, want 403", code)
	}
	if code, _ := callAs(t, handler, "", "DisableRecovery"); code != http.StatusUnauthorized {
		t.Errorf("an anonymous DisableRecovery = %d, want 401", code)
	}

	code, body := callAs(t, handler, "platform@north.example", "DisableRecovery")
	if code != http.StatusOK || body["disabledBy"] != "platform@north.example" || body["alreadyDisabled"] == true {
		t.Fatalf("DisableRecovery = %d %v", code, body)
	}
	if code, body = callAs(t, handler, "platform@north.example", "GetRecovery"); code != http.StatusOK ||
		body["effective"] == true || body["canDisable"] == true || body["disabledBy"] != "platform@north.example" {
		t.Fatalf("GetRecovery after = %d %v", code, body)
	}
	// And the console's own door is shut.
	post := httptest.NewRequest(http.MethodPost, "/console/login/recovery", nil)
	post.Header.Set("Authorization", "Bearer whatever")
	refused := httptest.NewRecorder()
	handler.ServeHTTP(refused, post)
	if refused.Code != http.StatusForbidden {
		t.Errorf("recovery after the turn-off = %d, want 403", refused.Code)
	}
}
