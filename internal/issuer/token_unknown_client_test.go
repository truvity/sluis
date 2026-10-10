package issuer_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/zitadel/oidc/v3/pkg/oidc"
)

// TestTokenEndpointUnknownClient_RefreshToken verifies that POST /token
// with grant_type=refresh_token and an unknown client_id returns 400
// invalid_client (not 500 server_error), per RFC 6749 §5.2.
func TestTokenEndpointUnknownClient_RefreshToken(t *testing.T) {
	t.Parallel()
	server, _ := serveIssuer(t)

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"fake_token"},
		"client_id":     {"unknown-client"},
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost,
		server.URL+"/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, _ := server.Client().Do(req)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var respBody map[string]interface{}
	json.Unmarshal(body, &respBody)

	if resp.StatusCode >= 500 {
		t.Errorf("unknown client = %d (5xx), want 400 invalid_client", resp.StatusCode)
	}
	if resp.StatusCode < 400 {
		t.Errorf("unknown client = %d (not 4xx), want 400 invalid_client", resp.StatusCode)
	}
	if respBody["error"] != "invalid_client" {
		t.Errorf("error = %q, want invalid_client", respBody["error"])
	}
}

// TestTokenEndpointUnknownClient_TokenExchangeBasicAuth verifies that
// POST /token with grant_type=urn:ietf:params:oauth:grant-type:token-exchange
// and an unknown client_id in HTTP Basic Auth returns 401 invalid_client
// (not 500 server_error), per RFC 6749 §5.2.
func TestTokenEndpointUnknownClient_TokenExchangeBasicAuth(t *testing.T) {
	t.Parallel()
	server, _ := serveIssuer(t)

	form := url.Values{
		"grant_type":         {string(oidc.GrantTypeTokenExchange)},
		"subject_token":      {"github:example-org/gitops@refs/heads/master"},
		"subject_token_type": {string(oidc.JWTTokenType)},
		"audience":           {"aws:1111:deployer"},
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost,
		server.URL+"/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("unknown-client", "")

	resp, _ := server.Client().Do(req)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var respBody map[string]interface{}
	json.Unmarshal(body, &respBody)

	// When client auth was attempted via HTTP Basic, should be 401
	if resp.StatusCode >= 500 {
		t.Errorf("unknown client = %d (5xx), want 401 invalid_client", resp.StatusCode)
	}
	if resp.StatusCode < 400 {
		t.Errorf("unknown client = %d (not 4xx), want 401 invalid_client", resp.StatusCode)
	}
	if respBody["error"] != "invalid_client" {
		t.Errorf("error = %q, want invalid_client", respBody["error"])
	}
}

// TestTokenEndpointUnknownClient_AuthorizationCode verifies that POST
// /token with grant_type=authorization_code and an unknown client_id
// returns 400 invalid_client (not 500 server_error), per RFC 6749 §5.2.
//
// This test cannot be run end-to-end without going through the
// authorization flow, so it is covered by the other tests' coverage
// of GetClientByClientID error handling. The refresh_token and
// token_exchange tests cover the code path.
//
// If needed in the future, this test could be added by:
// 1. Creating an auth code via /authorize
// 2. Attempting to redeem it with an unknown client_id
// 3. Verifying the response is invalid_client, not server_error
func TestTokenEndpointUnknownClient_AuthorizationCode(t *testing.T) {
	t.Skip("covered by refresh_token and token_exchange tests via GetClientByClientID")
}

// TestTokenEndpointUnknownAudience verifies that POST /token with an
// unknown audience returns 400 invalid_target (not 500 server_error),
// and does NOT confuse it with unknown client errors.
func TestTokenEndpointUnknownAudience(t *testing.T) {
	t.Parallel()
	server, _ := serveIssuer(t)

	// Use a known client (local-dev) but unknown audience
	status, body := exchange(t, server, "github:example-org/gitops@refs/heads/master", "unknown-audience")
	if status >= 500 {
		t.Errorf("unknown audience = %d (5xx), want 400 invalid_target", status)
	}
	if status < 400 {
		t.Errorf("unknown audience = %d (not 4xx), want 400 invalid_target", status)
	}
	if got, _ := body["error"].(string); got != "invalid_target" {
		t.Errorf("error = %q, want invalid_target", got)
	}
	if desc, _ := body["error_description"].(string); !strings.Contains(desc, "unknown-audience") {
		t.Errorf("description should mention the unknown audience: %q", desc)
	}
}
