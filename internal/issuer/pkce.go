package issuer

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// requirePKCE refuses, before any login page, an authorization request
// whose PKCE is not what this issuer advertises.
//
// Discovery lists `S256` only, yet the library accepts `plain` and does
// not insist on a challenge from a public client. A `plain` challenge is
// the verifier itself, so an observer of the redirect can replay it, and
// a public client with no challenge holds nothing else that protects its
// code (RFC 7636, OAuth 2.1). Both are refused with `invalid_request`.
// A confidential client may omit PKCE unless its policy says `pkce:
// required` (RFC 9700 2.1.1, OAuth 2.1); if it sends a challenge, the
// method must still be S256.
//
// A challenge with no `code_challenge_method` is `plain` (RFC 7636 4.3), and
// the library verifies it as such, so it is refused as `plain` is.
//
// Every authorization request it sees leaves one line at info level saying
// whether it carried a challenge, and with which method ([logAuthorizePKCE]).
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
		logAuthorizePKCE(r, challenge, method)
		if method != "" && method != string(oidc.CodeChallengeMethodS256) {
			refuseRequest(w, r, "`code_challenge_method` must be S256; this issuer does not accept `plain`")
			return
		}
		if challenge != "" && method == "" {
			refuseRequest(w, r, "a `code_challenge` needs `code_challenge_method=S256`; without it the challenge is `plain`, which this issuer does not accept")
			return
		}
		if challenge == "" {
			if pkceRequired(policies(), r.Form.Get("client_id")) {
				refuseRequest(w, r, "this client must send a `code_challenge` (PKCE, method S256)")
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// logAuthorizePKCE is the line an authorization request leaves about its
// PKCE: `pkce` says whether it carried a `code_challenge`, and `pkce_method`
// the method it named, `plain` when it named none (RFC 7636 4.3). The client
// it named is `client_id`; the request's method, path and user agent follow
// as on a refusal. Neither the challenge nor any verifier is logged.
func logAuthorizePKCE(r *http.Request, challenge, method string) {
	ctx := r.Context()
	attrs := withID(ctx, slog.Bool("pkce", challenge != ""))
	if challenge != "" {
		if method == "" {
			method = string(oidc.CodeChallengeMethodPlain)
		}
		attrs = append(attrs, logattr.SafeString("pkce_method", truncate(method, maxPKCEMethod)))
	}
	if id := r.Form.Get("client_id"); id != "" {
		attrs = append(attrs, logattr.SafeString("client_id", id))
	}
	slog.Default().Log(ctx, slog.LevelInfo, "authorization request", withRequest(ctx, r, attrs)...)
}

// maxPKCEMethod bounds the method as logged: it is the caller's text, and
// the longest one there is, `plain`, has five characters.
const maxPKCEMethod = 16

// pkceRequired reports whether an id names a client that must use PKCE,
// without resolving it: a declared one by its policy (a public client
// always, a confidential one when it says `pkce: required`), and a client
// that describes itself by its URL, which is public always. An id that
// names nothing known is left to the library to refuse.
func pkceRequired(set *policy.Set, clientID string) bool {
	if declared, ok := set.Client(clientID); ok {
		return declared.PKCERequired()
	}
	return strings.HasPrefix(clientID, "https://")
}

// errVerifierMissing is why a code is not redeemed: its request carried a
// PKCE challenge, or its client must use PKCE, and the token request
// carries no `code_verifier`.
var errVerifierMissing = errors.New("the token request has no code_verifier for a code bound to a PKCE challenge")

// checkVerifier refuses, for the library, a code redeemed with no
// `code_verifier` when it was issued against a challenge or its client must
// use PKCE (RFC 7636 4.6, RFC 9700 2.1.1). The library asks for the
// verifier only of a client that authenticates with nothing, or when one is
// sent, so a confidential client that sent a challenge could drop the
// verifier and redeem the code with its secret alone. The answer is
// `invalid_grant`. A verifier that is present is judged by the library.
//
// A call outside a token request (no [presentedFrom] on the context) is left
// alone.
func (s *Storage) checkVerifier(ctx context.Context, request *authRequest) error {
	asked := presentedFrom(ctx)
	if !asked.token || asked.verifier {
		return nil
	}
	required := request.Req.CodeChallenge != ""
	if declared, ok := s.iss.Policy().Client(request.Req.ClientID); ok && declared.PKCERequired() {
		required = true
	}
	if !required {
		return nil
	}
	return deny(ctx, oidc.ErrInvalidGrant, errVerifierMissing, logattr.SafeString("client_id", request.Req.ClientID))
}
