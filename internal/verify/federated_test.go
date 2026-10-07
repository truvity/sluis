package verify_test

import (
	"testing"

	"github.com/truvity/sluis/internal/verify"
)

// The rows become one verifier each, for the exchange's audience. The rows'
// rules (a name, an issuer, one row per issuer) are the policy document's,
// checked when it is loaded (internal/config).
func TestTheClustersBecomeVerifiers(t *testing.T) {
	t.Parallel()
	federation := verify.Federation{Clusters: []verify.FederatedCluster{
		{Name: "mgmt", Issuer: "https://oidc.eks.example/id/STAGING"},
		{Name: "devel", Issuer: "https://api.devel.example", JWKSURI: "https://api.devel.example/openid/v1/jwks"},
	}}
	verifiers := federation.Verifiers("access-issuer", nil)
	if len(verifiers) != 2 || verifiers[0].Audience != "access-issuer" || verifiers[1].JWKSURI == "" {
		t.Errorf("verifiers = %+v", verifiers)
	}
	if names := federation.Names(); len(names) != 2 || names[1] != "devel" {
		t.Errorf("names = %v", names)
	}
}
