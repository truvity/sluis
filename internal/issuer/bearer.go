package issuer

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// CLIAudiences are the clients whose sign-ins the console accepts as a bearer:
// sluisctl's (`sluisctl`, and `accessctl`, its older default). A token minted
// for any other client, the console's own or a workload's exchange, is not a
// laptop signed in, and is not read here.
var CLIAudiences = []string{"sluisctl", "accessctl"}

// CLISubject names the person behind a bearer, when it is the access token of a
// live sign-in at one of clients, and says nothing otherwise.
//
// A valid signature is not enough, because most tokens this issuer signs are
// handed to somebody else. It is the rule [Storage.signInProof] applies to a
// sign-in offered for exchange: the token is one this issuer holds a record of,
// issued to one of clients (named by the record and by the token's `client_id`
// and `aud`, never by `azp`), for a person, under a session that is still live
// and is not an agent's. So signing out, or ending the session from the
// console, stops the token at once; an ID token, which has no such record, and
// another client's access token are refused.
func (s *Storage) CLISubject(ctx context.Context, bearer string, clients []string) (string, bool) {
	claims, ok := s.liveSignInClaims(ctx, bearer, clients)
	if !ok {
		return "", false
	}
	return claims.Subject, true
}

// liveSignInClaims is the check [Storage.CLISubject] makes, and answers the
// claims of the access token it accepted. The session service reads the groups
// from them; nobody else needs more than the subject.
func (s *Storage) liveSignInClaims(ctx context.Context, bearer string, clients []string) (*oidc.AccessTokenClaims, bool) {
	if bearer == "" || s == nil {
		return nil, false
	}
	claims, err := op.VerifyAccessToken[*oidc.AccessTokenClaims](ctx, bearer, accessTokenVerifier(s.iss, s))
	if err != nil || strings.TrimSpace(claims.Subject) == "" {
		return nil, false
	}
	if claims.JWTID == "" {
		return nil, false
	}
	issued, err := getJSON[token](ctx, s.state, tokenKey(claims.JWTID))
	if err != nil || issued == nil {
		return nil, false
	}
	if !slices.Contains(clients, issued.ClientID) || claims.ClientID != issued.ClientID ||
		!slices.Contains(claims.Audience, issued.ClientID) || claims.Subject != issued.Subject {
		return nil, false
	}
	if !strings.Contains(issued.Subject, "@") || issued.Session == "" {
		return nil, false
	}
	session, live, err := s.iss.Sessions().ByID(ctx, issued.Session)
	if err != nil || !live || session.Agent() {
		return nil, false
	}
	return claims, true
}

// SessionsAudiences are the clients whose sign-ins may call the session
// service as a bearer: the CLI's and the console's, the same set that manages
// client secrets. Any other token this issuer signed -- a relying party's, an
// ID token, a workload's exchange -- is not a person acting through their
// own console.
func SessionsAudiences() []string { return ClientSecretsAudiences }

// BearerReader returns who a request's bearer token names, by [Storage.CLISubject]
// over clients. It reads nothing else from the token, and in particular not its
// groups: the console asks the directory and the policy who that address is
// now, exactly as it does for a browser session.
//
// This is how `sluisctl backup` reaches the console's BackupService with the
// sign-in `sluisctl login` holds.
func BearerReader(_ *Issuer, storage op.Storage, clients []string) func(*http.Request) (string, bool) {
	concrete, ok := storage.(*Storage)
	return func(r *http.Request) (string, bool) {
		if !ok {
			return "", false
		}
		return concrete.CLISubject(r.Context(), bearerFrom(r), clients)
	}
}
