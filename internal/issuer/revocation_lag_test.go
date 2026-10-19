package issuer_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// userinfoAt calls /userinfo with the bearer and returns the status and the
// challenge.
func userinfoAt(t *testing.T, base, token string) (status int, challenge string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, base+"/userinfo", nil) //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode, response.Header.Get("WWW-Authenticate")
}

// revokeAt posts a token to /revoke as local-dev and returns the status.
func revokeAt(t *testing.T, base, token, hint string) int {
	t.Helper()
	form := url.Values{"token": {token}, "client_id": {"local-dev"}}
	if hint != "" {
		form.Set("token_type_hint", hint)
	}
	response, err := http.Post(base+"/revoke", "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode
}

// Revocation lag: whatever ends a grant -- revoking its access
// token, revoking its refresh token, or ending the session -- is refused by the
// issuer itself on the very next request, with no grace and no cache: /userinfo
// answers 401 invalid_token for the access token and /token invalid_grant for
// the refresh token (revoking the access token alone ends that token only).
// What lags is outside the issuer: the access token is a JWT
// signed by a key that stays in the key set, so a relying party that verifies
// it offline accepts it until its `exp`. The test pins that bound too.
func TestRevocationIsImmediateAtTheIssuer(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		end func(t *testing.T, b *browser, tokens map[string]any)
		// refreshEnds is whether the refresh token is refused afterwards.
		// Revoking only the access token leaves its refresh token: RFC 7009
		// 2.1 lets a server revoke it too, and this one does not; a client
		// that wants the grant gone revokes the refresh token, which takes
		// the access token with it.
		refreshEnds bool
	}{
		"the access token revoked": {end: func(t *testing.T, b *browser, tokens map[string]any) {
			if status := revokeAt(t, b.server.URL, tokens["access_token"].(string), "access_token"); status != http.StatusOK {
				t.Fatalf("/revoke = %d", status)
			}
		}},
		"the refresh token revoked": {refreshEnds: true, end: func(t *testing.T, b *browser, tokens map[string]any) {
			if status := revokeAt(t, b.server.URL, tokens["refresh_token"].(string), "refresh_token"); status != http.StatusOK {
				t.Fatalf("/revoke = %d", status)
			}
		}},
		"the session ended": {refreshEnds: true, end: func(t *testing.T, b *browser, tokens map[string]any) {
			hint := url.QueryEscape(tokens["id_token"].(string))
			if status, _, body := b.do(http.MethodGet, "/end_session?id_token_hint="+hint); status >= http.StatusBadRequest {
				t.Fatalf("/end_session = %d %s", status, body)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, _ := signInServerWith(t, "ada@north.example", backChannelPolicy("http://127.0.0.1:1"))
			b := newBrowser(t, server)
			b.signIn()
			tokens := redeem(t, b, b.authorizeWith(map[string]string{"scope": "openid offline_access"}, ""))
			access, _ := tokens["access_token"].(string)
			refresh, _ := tokens["refresh_token"].(string)
			if access == "" || refresh == "" {
				t.Fatalf("tokens = %v, want an access and a refresh token", tokens)
			}
			if status, _ := userinfoAt(t, server.URL, access); status != http.StatusOK {
				t.Fatalf("/userinfo before the end = %d", status)
			}

			tc.end(t, b, tokens)
			if status, challenge := userinfoAt(t, server.URL, access); status != http.StatusUnauthorized || !strings.Contains(challenge, `error="invalid_token"`) {
				t.Errorf("/userinfo after the end = %d %q, want 401 invalid_token at once", status, challenge)
			}
			if tc.refreshEnds {
				if status, errName, _ := refreshAt(t, server.URL, refresh); status != http.StatusBadRequest || errName != "invalid_grant" {
					t.Errorf("refresh after the end = %d %q, want 400 invalid_grant at once", status, errName)
				}
			}

			// The lag a relying party verifying offline has: the token's own
			// expiry, which the end does not move.
			exp, _ := jwtPart(t, access, 1)["exp"].(float64)
			if left := time.Until(time.Unix(int64(exp), 0)); left <= 0 || left > time.Hour {
				t.Errorf("the revoked access token expires in %s, want within the policy's 1h lifetime", left)
			}
		})
	}
}

// A sanity check on the helper: a token the issuer never issued is refused
// the same way, so the refusal above is not an artefact of the request.
func TestUserinfoRefusesAnUnknownBearer(t *testing.T) {
	t.Parallel()
	server, _ := signInServerWith(t, "ada@north.example", backChannelPolicy("http://127.0.0.1:1"))
	if status, challenge := userinfoAt(t, server.URL, "not-a-token"); status != http.StatusUnauthorized || !strings.Contains(challenge, "invalid_token") {
		t.Errorf("/userinfo with an unknown token = %d %q", status, challenge)
	}
}
