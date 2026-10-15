package issuer

import (
	"net/http"
	"slices"
	"strings"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// CLIAudiences are the clients whose tokens the console accepts as a bearer:
// sluisctl's (`sluisctl`, and `accessctl`, its older default). A token minted
// for any other client, the console's own or a workload's exchange, is not a
// laptop signed in, and is not read here.
var CLIAudiences = []string{"sluisctl", "accessctl"}

// BearerReader returns who a request's bearer token names, when it is an access
// token this issuer signed for one of audiences: the subject, which for a
// person is their address. It reads nothing else from the token, and in
// particular not its groups: the console asks the directory and the policy who
// that address is now, exactly as it does for a browser session, so a token
// cannot carry rights its holder has since lost.
//
// This is how `sluisctl backup` reaches the console's BackupService with the
// sign-in `sluisctl login` holds.
func BearerReader(iss *Issuer, storage op.Storage, audiences []string) func(*http.Request) (string, bool) {
	verifier := accessTokenVerifier(iss, storage)
	return func(r *http.Request) (string, bool) {
		bearer := bearerFrom(r)
		if bearer == "" {
			return "", false
		}
		claims, err := op.VerifyAccessToken[*oidc.AccessTokenClaims](r.Context(), bearer, verifier)
		if err != nil || strings.TrimSpace(claims.Subject) == "" {
			return "", false
		}
		held := append([]string{}, claims.Audience...)
		if claims.AuthorizedParty != "" {
			held = append(held, claims.AuthorizedParty)
		}
		if !slices.ContainsFunc(held, func(a string) bool { return slices.Contains(audiences, a) }) {
			return "", false
		}
		return claims.Subject, true
	}
}
