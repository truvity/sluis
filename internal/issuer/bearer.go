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
	claims, ok := s.liveSignInAtAnyClient(ctx, bearer)
	if !ok || !slices.Contains(clients, claims.ClientID) {
		return nil, false
	}
	return claims, true
}

// liveSignInAtAnyClient is [Storage.liveSignInClaims] before it asks which
// client: the access token of a person's live, non-agent session that is not
// recovery's, at whichever client it was issued to (named by the record, and
// by the token's `client_id` and `aud`). The client secrets endpoint asks the
// question about the client itself, so that a token of the wrong client is a
// refusal it audits rather than one it cannot tell from a forged token.
func (s *Storage) liveSignInAtAnyClient(ctx context.Context, bearer string) (*oidc.AccessTokenClaims, bool) {
	claims, issued, ok := s.recordedAccessClaims(ctx, bearer)
	if !ok {
		return nil, false
	}
	if !slices.Contains(claims.Audience, issued.ClientID) {
		return nil, false
	}
	if !strings.Contains(issued.Subject, "@") || issued.Session == "" {
		return nil, false
	}
	session, live, err := s.iss.Sessions().ByID(ctx, issued.Session)
	if err != nil || !live || session.Agent() {
		return nil, false
	}
	// A sign-in made by recovery is never a laptop's or a console person's
	// bearer here, whether or not recovery is on: the subject test above already
	// excludes its ServiceAccount subject, and this keeps it excluded if that ever
	// changes (docs/decisions/0075).
	if provedWithoutDirectory(session.Method) {
		return nil, false
	}
	return claims, true
}

// recordedAccessClaims accepts a bearer only as an ACCESS token this issuer
// holds the record of, and only while what it was issued under still stands
// ([Storage.recordLive]); it answers the token's claims and the record.
//
// A valid signature, the issuer and an unexpired `exp` are not enough here.
// Every ID token this issuer signs passes those three, and it is handed to
// relying parties and sits in their logs; no ID token has a record. And an
// access token outlives a sign-out by up to its whole lifetime unless
// something asks its session: the record is what can be asked, and a
// revoked token's record is gone.
//
// The record must agree with the token: the same client (`client_id`) and
// the same subject. A token minted while the State was frozen for a
// migration has no record and is refused until it is renewed.
func (s *Storage) recordedAccessClaims(ctx context.Context, bearer string) (*oidc.AccessTokenClaims, *token, bool) {
	if bearer == "" || s == nil {
		return nil, nil, false
	}
	claims, err := op.VerifyAccessToken[*oidc.AccessTokenClaims](ctx, bearer, accessTokenVerifier(s.iss, s))
	if err != nil || strings.TrimSpace(claims.Subject) == "" || claims.JWTID == "" {
		return nil, nil, false
	}
	issued, err := getJSON[token](ctx, s.state, tokenKey(claims.JWTID))
	if err != nil || issued == nil {
		return nil, nil, false
	}
	if claims.ClientID == "" || claims.ClientID != issued.ClientID || claims.Subject != issued.Subject {
		return nil, nil, false
	}
	live, err := s.recordLive(ctx, issued)
	if err != nil || !live {
		return nil, nil, false
	}
	return claims, issued, true
}

// recordLive reports whether what an access token was issued under still
// stands: its session, when it has one; otherwise the browser sign-in its code
// was completed under, when there was one ([token.SignIn]). A token with
// neither -- an exchange, which proves no sign-in -- has nothing to end it but
// its own expiry and the deletion of its record by revocation.
func (s *Storage) recordLive(ctx context.Context, issued *token) (bool, error) {
	if issued.Session != "" {
		_, live, err := s.iss.Sessions().ByID(ctx, issued.Session)
		return live, err
	}
	if issued.SignIn != "" && s.iss.SSO() != nil {
		_, live, err := s.iss.SSO().Get(ctx, issued.SignIn)
		return live, err
	}
	return true, nil
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
