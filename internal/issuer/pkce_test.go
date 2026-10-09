package issuer_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// `plain` is refused, and a public client with no challenge is refused,
// both before the login page; S256 goes on to the sign-in.
func TestAuthorizeRefusesPlainAndMissingPKCE(t *testing.T) {
	t.Parallel()
	server, _, _, _ := newMultiAlgServer(t)
	b := newBrowser(t, server)

	base := "/authorize?client_id=local-dev&response_type=code&state=s&scope=openid" +
		"&redirect_uri=" + url.QueryEscape("http://localhost:8000/callback")
	const challenge = "&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	for name, tc := range map[string]struct {
		path    string
		refused bool
	}{
		"plain":            {base + challenge + "&code_challenge_method=plain", true},
		"upper-case plain": {base + challenge + "&code_challenge_method=PLAIN", true},
		"no challenge":     {base, true},
		"s256":             {base + challenge + "&code_challenge_method=S256", false},
	} {
		status, where, body := b.do(http.MethodGet, tc.path)
		if tc.refused {
			if status != http.StatusBadRequest || !strings.Contains(body, "invalid_request") {
				t.Errorf("%s: /authorize = %d %s, want 400 invalid_request", name, status, body)
			}
			continue
		}
		if status != http.StatusFound || !strings.HasPrefix(where, "/login") {
			t.Errorf("%s: /authorize = %d %q %s, want a redirect to /login", name, status, where, body)
		}
	}
}
