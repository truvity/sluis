package policy_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
)

// A redirect or a signed-out landing page over plain HTTP is accepted only on
// this machine's loopback, where a native application listens; anywhere else
// it is refused at load, for every kind of client.
func TestPlainHTTPRedirectsOnlyOnLoopback(t *testing.T) {
	t.Parallel()
	doc := func(kind, field, uri string) string {
		secret := ""
		if kind == policy.KindConfidential {
			secret = "\n    secret: app-secret"
		}
		other := "redirects"
		if field == "redirects" {
			other = "signed_out"
		}
		return `
version: 1
groups:
  team: { members: [team@example.com] }
clients:
  app:
    kind: ` + kind + secret + `
    requires: [team]
    ` + field + `: ["` + uri + `"]
    ` + other + `: ["https://app.example/other"]
`
	}
	load := func(src string) error {
		parsed, err := policy.Parse([]byte(src))
		if err != nil {
			return err
		}
		_, err = policy.NewSet(parsed)
		return err
	}

	for _, kind := range []string{policy.KindPublic, policy.KindConfidential} {
		for _, field := range []string{"redirects", "signed_out"} {
			for _, uri := range []string{
				"http://app.example/cb",
				"HTTP://app.example/cb",
				"http://10.0.0.1:8080/cb",
				"http://localhost.example/cb",
				"http://127.0.0.1.example/cb",
				"http://app.example/%zz",
			} {
				err := load(doc(kind, field, uri))
				if err == nil || !strings.Contains(err.Error(), "plain http") {
					t.Errorf("%s %s %s: err = %v, want a refusal naming plain http", kind, field, uri, err)
				}
			}
			for _, uri := range []string{
				"http://127.0.0.1/callback",
				"http://127.0.0.1:8000/callback",
				"http://[::1]:8000/callback",
				"http://localhost/auth/callback",
				"http://LOCALHOST:3000/login-callback",
				"https://app.example/cb",
			} {
				if err := load(doc(kind, field, uri)); err != nil {
					t.Errorf("%s %s %s: %v, want it accepted", kind, field, uri, err)
				}
			}
		}
	}
}
