package issuerapp

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
)

func ptr[T any](v T) *T { return &v }

// A cookie's Secure flag has to match the scheme the browser will use,
// and the service is told its own public URL, so that is where the
// default comes from. It used to default to false: an installation that
// simply did not set secureCookies served its session cookie without
// the flag, and a proxy could then carry it over a plain-http hop.
//
// The override stays for true: a TLS terminator that is not in the URL
// needs it. false is for a plain-http local run only; beside an https
// issuer URL it is refused (TestSecureCookiesFalseIsRefusedOnHTTPS).
//
// There is no row for an unset URL: issuerURL is required, so the
// default is always decided by a scheme somebody wrote down.
func TestSecureCookiesFollowsTheIssuerScheme(t *testing.T) {
	for _, tc := range []struct {
		name      string
		issuerURL string
		override  *bool
		want      bool
	}{
		{"https is the deployed case", "https://access.example", nil, true},
		{"http is the laptop", "http://localhost:8080", nil, false},
		{"an override turns it on", "http://in-cluster:8080", ptr(true), true},
		{"an override keeps it on over https", "https://access.example", ptr(true), true},
		{"an override turns it off on a local http run", "http://localhost:8080", ptr(false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := FromConfig(withPolicy(t, &config.Serve{IssuerURL: tc.issuerURL, SecureCookies: tc.override}))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.secureCookies != tc.want {
				t.Errorf("secureCookies = %v, want %v", cfg.secureCookies, tc.want)
			}
		})
	}
}

// secureCookies: false on an https issuer sends the session cookie without
// the Secure flag. It is refused, in every shape, and the error names the
// field and the reason.
func TestSecureCookiesFalseIsRefusedOnHTTPS(t *testing.T) {
	f, p := withPolicy(t, &config.Serve{IssuerURL: "https://access.example", SecureCookies: ptr(false)})
	_, err := FromConfig(f, p)
	if err == nil {
		t.Fatal("secureCookies: false with an https issuerURL loaded")
	}
	for _, want := range []string{"secureCookies: false", "issuerURL is https", "Secure flag"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
