package issuer_test

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/refusal"
)

// Every authorization request leaves one info line saying whether it
// carried a PKCE challenge and with which method, naming its client; the
// challenge itself is never in it. A challenge with no method is `plain`
// (RFC 7636 4.3) and is refused as `plain` is.
func TestAuthorizeLogsPKCE(t *testing.T) {
	t.Parallel()
	server, _, _, _ := newMultiAlgServer(t)

	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	base := "/authorize?client_id=local-dev&response_type=code&state=s&scope=openid" +
		"&redirect_uri=" + url.QueryEscape("http://localhost:8000/callback")

	for name, tc := range map[string]struct {
		query      string
		wantPKCE   string
		wantMethod string
		refused    bool
	}{
		"s256":           {"&code_challenge=" + challenge + "&code_challenge_method=S256", "true", "S256", false},
		"plain":          {"&code_challenge=" + challenge + "&code_challenge_method=plain", "true", "plain", true},
		"no method":      {"&code_challenge=" + challenge, "true", "plain", true},
		"no challenge":   {"", "false", "", true},
		"method, no one": {"&code_challenge_method=S256", "false", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBrowser(t, server)
			status, where, body, header := b.send(http.MethodGet, base+tc.query, nil)
			if tc.refused {
				if status != http.StatusBadRequest || !strings.Contains(body, "invalid_request") {
					t.Fatalf("/authorize = %d %s, want 400 invalid_request", status, body)
				}
			} else if status != http.StatusFound || !strings.HasPrefix(where, "/login") {
				t.Fatalf("/authorize = %d %q %s, want a redirect to /login", status, where, body)
			}

			id := requestIDOf(t, header)
			var lines int
			for _, record := range refusalLog.Records() {
				if record.String(refusal.LogKey) != id || record.Message != "authorization request" {
					continue
				}
				lines++
				if record.Level != slog.LevelInfo {
					t.Errorf("level = %v, want INFO", record.Level)
				}
				if got := record.String("pkce"); got != tc.wantPKCE {
					t.Errorf("pkce = %q, want %q", got, tc.wantPKCE)
				}
				if got := record.String("pkce_method"); got != tc.wantMethod {
					t.Errorf("pkce_method = %q, want %q", got, tc.wantMethod)
				}
				if got := record.String("client_id"); got != "local-dev" {
					t.Errorf("client_id = %q, want local-dev", got)
				}
				for _, attr := range record.Attrs {
					if strings.Contains(attr.Value.String(), challenge) {
						t.Errorf("attribute %s carries the challenge: %q", attr.Key, attr.Value.String())
					}
				}
			}
			if lines != 1 {
				t.Errorf("%d authorization request lines for request %s, want 1\n%s", lines, id, loggedFor(id))
			}
		})
	}
}
