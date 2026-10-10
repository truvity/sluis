package issuer

import (
	"testing"

	"github.com/truvity/sluis/internal/port"
)

// Every Index set the issuer writes has an address on layout v5. A set that
// has none is refused by `migrate`, left out of a backup and not restored; the
// set of the per-identity sign-ins was once missing.
func TestEveryIssuerIndexSetHasALayout5Address(t *testing.T) {
	sets := []string{
		ssoAllKey,
		ssoOfKey("Ada@acme.example"),
		ssoClientsKey("s1"),
		sessionOfKey("ada@acme.example"),
		sessionForKey("console"),
		"issuer:sessions",
		"issuer:keyring:index:ES384",
	}
	for _, set := range sets {
		a, err := port.LocateSet5(set)
		if err != nil || a.Kind == port.KindIndexOther || a.Module != port.ModuleOIDC {
			t.Errorf("LocateSet5(%q) = %v, %v: no address on layout v5", set, a, err)
		}
		if !contains(port.SetPrefixes5(port.ModuleOIDC), set) {
			t.Errorf("no prefix of SetPrefixes5(oidc) covers %q: a backup would leave it out", set)
		}
	}
}

func contains(prefixes []string, set string) bool {
	for _, p := range prefixes {
		if len(set) >= len(p) && set[:len(p)] == p {
			return true
		}
	}
	return false
}
