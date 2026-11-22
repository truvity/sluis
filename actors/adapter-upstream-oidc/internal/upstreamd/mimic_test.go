//go:build sluis_test

package upstreamd_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	configv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/config/v2alpha1"
	integrationv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/integration/v2alpha1"
	"github.com/truvity/sluis/v2/tools/mimic/mimictest"
)

// The adapter federates to a mimic issuer (ADR 0054) as it does to a company's Google Workspace or Entra ID tenant:
// the whole code flow with PKCE, from Begin through mimic's authorization endpoint to Complete, with no Keycloak
// realm. A CI job's hand-off gives the browser its mimic session, as on development.
func TestTheAdapterFederatesToAMimicIssuer(t *testing.T) {
	for _, c := range []struct {
		profile, tenant, org string
	}{
		{"entra", "example-a", ""},
		{"google", "example-b", "example-b.test"},
	} {
		t.Run(c.profile, func(t *testing.T) {
			m := mimictest.Start(t, nil)
			issuer := m.Issuer(c.profile, c.tenant)
			up := &configv2alpha1.Upstream{
				Name: "workforce", Kind: "oidc", Issuer: issuer, ClientId: "rp-" + c.tenant,
				SubjectClaim: "sub", Scopes: []string{"profile", "email"}, Secret: "upstream-workforce",
			}
			if c.org != "" {
				up.OrgClaim = "hd" // Google's hosted domain, the test domain on mimic
			}
			e := newEnv(t, envOptions{environment: "development", upstream: up, secret: mimictest.ClientSecret})
			verifier, challenge := pkcePair(t)
			res, err := e.begin(context.Background(), &integrationv2alpha1.BeginRequest{
				Upstream: "workforce", RedirectUri: mimictest.Callback, State: state, Nonce: nonce, CodeChallenge: challenge,
			})
			if err != nil {
				t.Fatal(err)
			}

			// The browser of a CI job: the hand-off of its stable-sluis token, then the authorization URL of Begin with
			// the identity named, as a realm passes login_hint on.
			b := m.HandOff("spiffe://example.com/ci/github/acme/app", []string{"devel:shared:keycloak:" + c.tenant + ":act-as"})
			r, err := b.Get(res.GetAuthorizationUrl() + "&" + url.Values{"login_hint": {"alice"}}.Encode())
			if err != nil {
				t.Fatal(err)
			}
			_ = r.Body.Close()
			if r.StatusCode != http.StatusSeeOther {
				t.Fatalf("mimic answered %d", r.StatusCode)
			}
			loc, err := url.Parse(r.Header.Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			q := loc.Query()
			if q.Get("state") != state || q.Get("code") == "" {
				t.Fatalf("mimic did not answer with a code: %v", q.Get("error"))
			}

			a, err := e.complete(context.Background(), &integrationv2alpha1.CompleteRequest{
				Upstream: "workforce", Code: q.Get("code"), RedirectUri: mimictest.Callback, CodeVerifier: verifier, Nonce: nonce,
				Iss: q.Get("iss"),
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if a.GetIssuer() != issuer || a.GetSubjectClaim() != "sub" || len(a.GetSubject()) != 43 {
				t.Errorf("identity = %q %q %q", a.GetIssuer(), a.GetSubjectClaim(), a.GetSubject())
			}
			if a.GetOrgId() != c.org || a.GetAuthTime() == nil {
				t.Errorf("org = %q, auth_time = %v", a.GetOrgId(), a.GetAuthTime())
			}
		})
	}
}
