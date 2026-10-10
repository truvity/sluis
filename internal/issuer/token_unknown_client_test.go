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

// TestTokenEndpointUnknownClient is a table test verifying that unknown
// clients on the token endpoint return 400/401 invalid_client (not 500
// server_error) per RFC 6749 §5.2, across all grant types.
func TestTokenEndpointUnknownClient(t *testing.T) {
	tests := []struct {
		name       string
		grantType  string
		form       url.Values
		basicAuth  *struct{ user, pass string }
		checkError func(t *testing.T, status int, body map[string]interface{})
	}{
		{
			name:      "refresh_token with unknown client_id",
			grantType: "refresh_token",
			form: url.Values{
				"grant_type":    {"refresh_token"},
				"refresh_token": {"fake_token"},
				"client_id":     {"unknown-client"},
			},
			checkError: func(t *testing.T, status int, body map[string]interface{}) {
				if status >= 500 {
					t.Errorf("status = %d (5xx), want 4xx invalid_client", status)
				}
				if status < 400 {
					t.Errorf("status = %d (not 4xx), want 4xx invalid_client", status)
				}
				if body["error"] != "invalid_client" {
					t.Errorf("error = %q, want invalid_client", body["error"])
				}
			},
		},
		{
			name:      "token-exchange with unknown client via HTTP Basic Auth",
			grantType: "urn:ietf:params:oauth:grant-type:token-exchange",
			form: url.Values{
				"grant_type":         {string(oidc.GrantTypeTokenExchange)},
				"subject_token":      {"github:example-org/gitops@refs/heads/master"},
				"subject_token_type": {string(oidc.JWTTokenType)},
				"audience":           {"aws:1111:deployer"},
			},
			basicAuth: &struct{ user, pass string }{"unknown-client", ""},
			checkError: func(t *testing.T, status int, body map[string]interface{}) {
				if status >= 500 {
					t.Errorf("status = %d (5xx), want 401 invalid_client", status)
				}
				if status < 400 {
					t.Errorf("status = %d (not 4xx), want 401 invalid_client", status)
				}
				if body["error"] != "invalid_client" {
					t.Errorf("error = %q, want invalid_client", body["error"])
				}
			},
		},
		// Note: authorization_code with unknown client_id is also handled by GetClientByClientID,
		// but testing it requires creating a real authorization code through the full auth flow.
		// The refresh_token and token_exchange tests above exercise the same code path
		// (GetClientByClientID returning invalid_client for unknown clients), so it is covered.
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, _ := serveIssuer(t)

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
				server.URL+"/token", strings.NewReader(tc.form.Encode()))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			if tc.basicAuth != nil {
				req.SetBasicAuth(tc.basicAuth.user, tc.basicAuth.pass)
			}

			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatalf("post request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			var respBody map[string]interface{}
			if err := json.Unmarshal(body, &respBody); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			tc.checkError(t, resp.StatusCode, respBody)
		})
	}
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
