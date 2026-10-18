package rosterapp_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/truvity/sluis/internal/rosterapp"
)

// gateway is an OpenID provider just real enough for the console to verify a
// forwarded bearer against: discovery, a key set and a signer.
type gateway struct {
	*httptest.Server
	key *rsa.PrivateKey
}

func newGateway(t *testing.T) *gateway {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := &gateway{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": g.URL, "jwks_uri": g.URL + "/keys", "authorization_endpoint": g.URL + "/authorize", "token_endpoint": g.URL + "/token",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: "k", Algorithm: "RS256", Use: "sig"}}})
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

func (g *gateway) token(t *testing.T, email string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: g.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(map[string]any{
		"iss": g.URL, "sub": email, "email": email, "aud": []string{"console"},
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The merged service wires ONE switch: the console's turn-off reaches the
// console's own recovery door and the issuer's, and it is a Connect call under
// the console's mount that a person signed in through the identity provider
// (here, a gateway's forwarded address) makes.

func bootWithForwardedOperator(t *testing.T, gw *gateway) http.Handler {
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
login: { forwarded: { issuer: `+gw.URL+`, audience: console, emailHeader: X-Forwarded-Email } }
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

// callAs calls the console as a verified forwarded bearer (a gateway's token),
// or as an address in the gateway's header when header is set.
func callAs(t *testing.T, handler http.Handler, bearer, header, method string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/console/sluis.v1.AccessService/"+method, strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if header != "" {
		request.Header.Set("X-Forwarded-Email", header)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return response.Code, body
}

func TestTheConsoleTurnsRecoveryOffThroughTheMergedService(t *testing.T) {
	t.Parallel()
	gw := newGateway(t)
	handler := bootWithForwardedOperator(t, gw)
	operator := gw.token(t, "platform@north.example")
	viewer := gw.token(t, "viewer@north.example")

	if code, body := callAs(t, handler, operator, "", "GetRecovery"); code != http.StatusOK ||
		body["configured"] != true || body["effective"] != true || body["canDisable"] != true || body["generation"] != "2026-10" {
		t.Fatalf("GetRecovery before = %d %v", code, body)
	}
	// A viewer is not an operator, nobody is not signed in, and an address in a
	// header is a person the console cannot verify.
	if code, _ := callAs(t, handler, viewer, "", "DisableRecovery"); code != http.StatusForbidden {
		t.Errorf("a viewer's DisableRecovery = %d, want 403", code)
	}
	if code, _ := callAs(t, handler, "", "", "DisableRecovery"); code != http.StatusUnauthorized {
		t.Errorf("an anonymous DisableRecovery = %d, want 401", code)
	}
	if code, _ := callAs(t, handler, "", "platform@north.example", "DisableRecovery"); code != http.StatusForbidden {
		t.Errorf("an operator's header address calling DisableRecovery = %d, want 403", code)
	}

	code, body := callAs(t, handler, operator, "", "DisableRecovery")
	if code != http.StatusOK || body["disabledBy"] != "platform@north.example" || body["alreadyDisabled"] == true {
		t.Fatalf("DisableRecovery = %d %v", code, body)
	}
	if code, body = callAs(t, handler, operator, "", "GetRecovery"); code != http.StatusOK ||
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
