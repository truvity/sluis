package issuerapp

import (
	"net/http"

	"github.com/truvity/sluis/internal/issuer"
)

// servesInMaintenance says a request is answered while the oidc module is under
// maintenance (docs/decisions/0072, decision 9).
//
// What keeps answering is what writes nothing: discovery and the key set, so
// relying parties keep verifying the tokens they hold; userinfo and
// introspection of a token already issued; the grants listing; the signed-out
// page; and the console, which refuses its own writes and shows the banner. A
// token already issued stays valid until it expires. What stops is everything
// that signs a person in, issues, refreshes, revokes or ends a session:
// /authorize, /oauth/token, /revoke, /end_session, the sign-in and sign-out
// pages and the client-secret operations.
func servesInMaintenance(r *http.Request) bool {
	switch issuer.Route(r) {
	case "discovery", "jwks", "userinfo", "introspect", "grants", "signed_out",
		"console", "console_rpc", "console_assets", "connect_callback":
		return true
	case "other":
		// The bare origin redirects to the console; every other path the
		// protocol library serves is a write or unknown.
		return r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
	}
	return false
}
