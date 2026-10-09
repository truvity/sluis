package issuer

import (
	"net/http"
	"strings"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/policy"
)

// requirePKCE refuses, before any login page, an authorization request
// whose PKCE is not what this issuer advertises.
//
// Discovery lists `S256` only, yet the library accepts `plain` and does
// not insist on a challenge from a public client. A `plain` challenge is
// the verifier itself, so an observer of the redirect can replay it, and
// a public client with no challenge holds nothing else that protects its
// code (RFC 7636, OAuth 2.1). Both are refused with `invalid_request`.
// A confidential client may omit PKCE; if it sends a challenge, the
// method must still be S256.
func requirePKCE(policies func() *policy.Set, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != authorizePath || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
			next.ServeHTTP(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			next.ServeHTTP(w, r)
			return
		}

		challenge := r.Form.Get("code_challenge")
		method := r.Form.Get("code_challenge_method")
		if method != "" && method != string(oidc.CodeChallengeMethodS256) {
			refuseRequest(w, r, "`code_challenge_method` must be S256; this issuer does not accept `plain`")
			return
		}
		if challenge == "" {
			if publicClient(policies(), r.Form.Get("client_id")) {
				refuseRequest(w, r, "a public client must send a `code_challenge` (PKCE, method S256)")
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// publicClient reports whether an id names a public client without
// resolving it: a declared one by its kind, and a client that describes
// itself by its URL, which is public always. An id that names nothing
// known is left to the library to refuse.
func publicClient(set *policy.Set, clientID string) bool {
	if declared, ok := set.Client(clientID); ok {
		return declared.Kind == policy.KindPublic
	}
	return strings.HasPrefix(clientID, "https://")
}
