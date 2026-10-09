package issuer_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// RFC 8707 2.2: a token request's `resource` must be within what the grant
// was authorized for. The same resource, or none, is fine; another is
// `invalid_target`, at the code and at the refresh.
func TestATokenRequestResourceMustBeWithinTheGrant(t *testing.T) {
	t.Parallel()
	server, _, _, _ := newMultiAlgServer(t)
	b := newBrowser(t, server)
	b.signIn()

	const granted = "https://resource.example/rs256"
	const other = "https://resource.example/default"

	callback := func() string {
		sentTo := b.authorizeWith(map[string]string{"scope": "openid offline_access"}, "&resource="+url.QueryEscape(granted))
		for strings.HasPrefix(sentTo, "/") {
			_, sentTo, _ = b.do(http.MethodGet, sentTo)
		}
		back, err := url.Parse(sentTo)
		if err != nil || back.Query().Get("code") == "" {
			t.Fatalf("the browser was sent to %q, want the callback with a code", sentTo)
		}
		return back.Query().Get("code")
	}
	redeemWith := func(code string, extra url.Values) (int, map[string]any) {
		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"redirect_uri":  {"http://localhost:8000/callback"},
			"client_id":     {"local-dev"},
			"code_verifier": {pkceVerifier},
		}
		for k, v := range extra {
			form[k] = v
		}
		return tokenAnswer(t, server.URL, form)
	}

	if status, body := redeemWith(callback(), url.Values{"resource": {other}}); status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Errorf("code with another resource = %d %v, want 400 invalid_target", status, body)
	}

	status, tokens := redeemWith(callback(), url.Values{"resource": {granted}})
	if status != http.StatusOK {
		t.Fatalf("code with the granted resource = %d %v, want 200", status, tokens)
	}
	refreshToken, _ := tokens["refresh_token"].(string)
	if refreshToken == "" {
		t.Fatal("no refresh token")
	}

	refresh := func(token, resource string) (int, map[string]any) {
		return tokenAnswer(t, server.URL, url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {token},
			"client_id": {"local-dev"}, "resource": {resource},
		})
	}
	if status, body := refresh(refreshToken, other); status != http.StatusBadRequest || body["error"] != "invalid_target" {
		t.Errorf("refresh with another resource = %d %v, want 400 invalid_target", status, body)
	}
	if status, body := refresh(refreshToken, granted); status != http.StatusOK {
		t.Errorf("refresh with the granted resource = %d %v, want 200 (a refused request must not spend the token)", status, body)
	}
}
