package issuer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/policy"
)

// RFC 8252 7.3: of a native client's loopback redirect only the port is free.
func TestOnlyThePortOfALoopbackRedirectIsFree(t *testing.T) {
	t.Parallel()

	clients := func(_ context.Context, id string) (op.Client, error) {
		switch id {
		case "cli":
			return &client{id: id, declared: policy.Client{
				Kind: policy.KindPublic,
				Redirects: []string{
					"http://127.0.0.1/callback", "http://localhost/other?x=1", "myapp://cb", "https://app.example/cb",
				},
			}}, nil
		case "web":
			return &client{id: id, declared: policy.Client{
				Kind: policy.KindConfidential, Redirects: []string{"http://127.0.0.1/callback"},
			}}, nil
		}

		return nil, errors.New("unknown client")
	}

	for name, tc := range map[string]struct {
		client, redirect string
		allowed          bool
	}{
		"as registered":               {"cli", "http://127.0.0.1/callback", true},
		"another port":                {"cli", "http://127.0.0.1:54321/callback", true},
		"the highest port":            {"cli", "http://127.0.0.1:65535/callback", true},
		"localhost, another port":     {"cli", "http://localhost:8000/other?x=1", true},
		"another loopback host":       {"cli", "http://[::1]:9000/callback", true},
		"localhost for a 127 entry":   {"cli", "http://localhost:9000/callback", true},
		"a custom scheme as it is":    {"cli", "myapp://cb", true},
		"a registered https":          {"cli", "https://app.example/cb", true},
		"another scheme":              {"cli", "https://localhost:1/callback", false},
		"another scheme, 127":         {"cli", "https://127.0.0.1:8443/callback", false},
		"another path":                {"cli", "http://127.0.0.1:1/elsewhere", false},
		"another query":               {"cli", "http://localhost:1/other?x=2", false},
		"no query":                    {"cli", "http://localhost:1/other", false},
		"a port that is not one":      {"cli", "http://127.0.0.1:x/callback", true}, // does not parse; the library refuses it
		"port zero":                   {"cli", "http://127.0.0.1:0/callback", false},
		"a port out of range":         {"cli", "http://127.0.0.1:70000/callback", false},
		"an upper-case host":          {"cli", "http://LOCALHOST:1/other?x=1", true},  // not loopback to the library; it refuses
		"a trailing dot":              {"cli", "http://localhost.:1/other?x=1", true}, // likewise
		"a suffix of localhost":       {"cli", "http://localhost.evil.com:1/other?x=1", true},
		"a name that embeds 127":      {"cli", "http://127.0.0.1.nip.io:1/callback", true},
		"an IPv4-mapped IPv6 address": {"cli", "http://[::ffff:127.0.0.1]:1/callback", false},
		"another 127 address":         {"cli", "http://127.0.0.2:1/callback", false},
		"a fragment":                  {"cli", "http://127.0.0.1:1/callback#f", false},
		"userinfo":                    {"cli", "http://u@127.0.0.1:1/callback", false},
		"a confidential client":       {"web", "http://127.0.0.1:1/callback", true}, // the library's exact match refuses it
		"an unknown client":           {"nobody", "https://localhost:1/callback", true},
		"a host that is not loopback": {"cli", "http://evil.example:1/callback", true}, // the library refuses it
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := &reached{}
			rec := httptest.NewRecorder()
			loopbackRedirects(clients, got.handler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				authorizePath+"?"+url.Values{"client_id": {tc.client}, "redirect_uri": {tc.redirect}}.Encode(), nil))

			if got.called != tc.allowed {
				t.Errorf("reached the library = %v, want %v (status %d)", got.called, tc.allowed, rec.Code)
			}

			if !tc.allowed && (rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "") {
				t.Errorf("status = %d, Location = %q; want the 400 page and no redirect", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}
