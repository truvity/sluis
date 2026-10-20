package config_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
)

// secureCookies: false beside an https URL is refused when the file loads,
// for the issuer's document and the one service document alike. Plain http
// keeps working with allowInsecure, and true is allowed on either scheme.
const rootOnly = "issuerURL: http://localhost:8080\nallowInsecure: true\n" +
	"publicRootURL: https://access.example\nsecureCookies: false\n"

func TestSecureCookiesFalseIsRefusedOnHTTPS(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"https issuer, off":         {"issuerURL: https://access.example\nsecureCookies: false\n", "issuerURL is https"},
		"https root, off":           {rootOnly, "publicRootURL is https"},
		"https issuer, on":          {"issuerURL: https://access.example\nsecureCookies: true\n", ""},
		"https issuer, unset":       {"issuerURL: https://access.example\n", ""},
		"plain http, allowInsecure": {"issuerURL: http://localhost:8080\nallowInsecure: true\nsecureCookies: false\n", ""},
		"plain http, on":            {"issuerURL: http://localhost:8080\nallowInsecure: true\nsecureCookies: true\n", ""},
		"plain http, unset":         {"issuerURL: http://localhost:8080\nallowInsecure: true\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			check := func(err error) {
				t.Helper()
				switch {
				case tc.want == "" && err != nil:
					t.Errorf("refused: %v", err)
				case tc.want != "":
					if err == nil {
						t.Fatal("loaded")
					}
					for _, w := range []string{"secureCookies: false", tc.want, "Secure flag"} {
						if !strings.Contains(err.Error(), w) {
							t.Errorf("error %q does not mention %q", err, w)
						}
					}
				}
			}
			_, err := config.Load[config.Serve](write(t, tc.body))
			check(err)
			_, err = config.Load[config.Sluis](write(t, tc.body))
			check(err)
		})
	}
}
