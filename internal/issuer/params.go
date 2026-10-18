package issuer

import (
	"net/http"
	"slices"
)

// singleValuedParameters are the request parameters a protocol endpoint
// takes once. RFC 6749 section 3.1: "Request and response parameters MUST NOT
// be included more than once."
//
// They are the ones this issuer decides on. The layers in front of the
// library read a parameter with `Form.Get`, which is the FIRST value; the
// library decodes the form and keeps the LAST. So a request that repeats one
// is judged on one value by the checks here (the PKCE method, the
// registered redirect for an error, the client a refusal is counted
// against) and acted on with another, and an attacker picks which is which.
// Refusing the repeat closes the difference instead of defining a side of it.
var singleValuedParameters = []string{
	"client_id", "redirect_uri", "response_type", "scope",
	"code_challenge", "code_challenge_method", "resource",
	"state", "nonce", "post_logout_redirect_uri", "id_token_hint",
}

// singleValuedPaths are the endpoints that read a form: the authorization,
// token, revocation, introspection and end-session endpoints.
var singleValuedPaths = []string{
	authorizePath, endSessionPath, "/token", "/oauth/token", "/revoke", "/introspect", "/oauth/introspect",
}

// singleValued refuses, before the library parses anything, a request to a
// protocol endpoint that repeats one of [singleValuedParameters], whether in
// the query, in the body or across both, with `invalid_request`. The reason
// is for the log; the caller is told it sent a bad request.
//
// No redirect is made for the refusal: a repeated `redirect_uri` is the
// request that cannot say where to go.
func singleValued(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(singleValuedPaths, r.URL.Path) || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
			next.ServeHTTP(w, r)
			return
		}
		// A form that does not parse is the library's to refuse, as it was.
		if err := r.ParseForm(); err != nil {
			next.ServeHTTP(w, r)
			return
		}

		for _, name := range singleValuedParameters {
			if len(r.Form[name]) > 1 {
				why := "`" + name + "` is repeated; RFC 6749 section 3.1 allows each request parameter once"
				if r.URL.Path == endSessionPath {
					endSessionRefusal(w, r, http.StatusBadRequest, why)
				} else {
					refuseRequest(w, r, why)
				}
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}
