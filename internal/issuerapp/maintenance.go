package issuerapp

import (
	"net/http"

	"github.com/truvity/sluis/internal/issuer"
)

// The grants a migration freeze still serves on /oauth/token.
const (
	grantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	grantRefreshToken  = "refresh_token"
)

// maxTokenForm bounds the form read to find the grant type.
const maxTokenForm = 1 << 20

// servesInFreeze says a request is answered, read-only, while the oidc module is
// under a migration freeze (`sluis maintenance on`) and not under any other
// maintenance. It is the token endpoint for the two grants that need not write:
// a token exchange (a CI job, a workload or an AWS role trading its proof, or a
// person trading a sign-in already recorded) and a refresh, which is not
// rotated. A code redemption, a device code and every other grant stay refused:
// they open or end a session.
//
// The request context is marked read-only by the middleware: nothing the
// request answers is recorded in the State that is being copied.
func servesInFreeze(r *http.Request) bool {
	if issuer.Route(r) != "token" || r.Method != http.MethodPost {
		return false
	}
	// The form is parsed once here and cached on the request, where the
	// protocol library reads it again.
	r.Body = http.MaxBytesReader(nil, r.Body, maxTokenForm)
	if err := r.ParseForm(); err != nil {
		return false
	}
	switch r.PostForm.Get("grant_type") {
	case grantTokenExchange, grantRefreshToken:
		return true
	}
	return false
}

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
