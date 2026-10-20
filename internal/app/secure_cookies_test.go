package app_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/app"
	"github.com/truvity/sluis/internal/config"
)

// The console's session cookie is Secure when the public root URL is https,
// and secureCookies: false beside it is refused: the cookie would go out
// without the flag. A plain-http local run may still turn it off, and true
// stays allowed on either scheme.
func TestSecureCookiesFalseIsRefusedOnHTTPS(t *testing.T) {
	off, on := false, true
	for name, tc := range map[string]struct {
		root, issuer string
		override     *bool
		wantErr      string
	}{
		"https root, off":     {"https://access.example", "http://localhost:8080", &off, "publicRootURL is https"},
		"https issuer, off":   {"http://localhost:8081", "https://access.example", &off, "issuerURL is https"},
		"https root, on":      {"https://access.example", "https://access.example", &on, ""},
		"https root, unset":   {"https://access.example", "https://access.example", nil, ""},
		"http local, off":     {"http://localhost:8081", "http://localhost:8080", &off, ""},
		"http in-cluster, on": {"http://in-cluster:8081", "http://in-cluster:8080", &on, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := issuerFile(t, func(f *config.Serve) {
				f.Demo = false
				f.IssuerURL, f.PublicRootURL, f.SecureCookies = tc.issuer, tc.root, tc.override
			})
			_, err := app.FromConfig(f, nil)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want one mentioning %q", err, tc.wantErr)
			}
		})
	}
}
