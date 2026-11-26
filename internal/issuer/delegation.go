package issuer

import (
	"context"
	"fmt"
	"strings"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/audit"
)

// A delegated exchange (RFC 8693 1.1, 4.1) is a token exchange with two
// parties: the subject, whose token is minted, and the actor, who presents it
// and is named in its `act`. It serves one case: a resource server, such as an
// MCP server's sidecar, that holds a person's access token for itself and
// calls a backend as that person, rather than as itself, so that the backend
// sees the person's own groups.
//
// The subject token is the person's access token this issuer signed, under a
// sign-in that still stands; the actor token is the workload's own proof, a
// kind the exchange already accepts as a subject. Whether this actor may act
// for this subject at the requested audience is the target client's
// `delegation` ([Issuer.delegationAllowed]); what the token may open is still
// its `requires`, read against the person's groups as the directory answers
// them now, never copied from the subject token.

// delegated reports whether an exchange names an actor.
func delegated(request op.TokenExchangeRequest) bool {
	return request.GetExchangeActorTokenIDOrToken() != ""
}

// delegatedProof is the proof of a delegated exchange: the person the subject
// token is the access token of, with the verified actor beside them. A refusal
// here is about the tokens themselves, so it is `invalid_grant` and recorded
// here; the target's own rule is [Issuer.Exchange]'s.
func (s *Storage) delegatedProof(ctx context.Context, request op.TokenExchangeRequest, audience string) (Proof, error) {
	refuse := func(who audit.Actor, by []audit.ActedBy, code, format string, args ...any) (Proof, error) {
		err := deny(ctx, oidc.ErrInvalidGrant, &codedReason{code: code, err: fmt.Errorf(format, args...)})
		s.iss.record(ctx, audit.TokenExchangeRefused(who, audience, proofKindOf(who), audit.Denied(reasonOfOIDC(err)),
			audit.Refusal{Code: code, Audience: audience, Directory: "not_applicable"}, by...))
		return Proof{}, err
	}

	carried, ok := request.GetExchangeActorTokenClaims()[verifiedActorKey].(verified)
	if !ok {
		return refuse(audit.Anonymous(), nil, "actor_token_refused",
			"actor_token: an actor proves itself with its own platform's token (a ServiceAccount, a CI job, an AWS role), never with one this issuer signed")
	}
	actor := carried.proof
	by := []audit.ActedBy{{ID: actor.Subject(), Proof: actor.kind()}}

	// A subject a verifier proved is a machine's own token, which has no
	// directory answer to read groups from again; it goes to the target's
	// rule as what it is, and the rule refuses any kind but a person.
	if subject, ok := request.GetExchangeSubjectTokenClaims()[verifiedKey].(verified); ok {
		proof := subject.proof
		proof.Acting = &Acting{Actor: actor}
		return proof, nil
	}

	if request.GetExchangeSubjectTokenType() != oidc.AccessTokenType {
		return refuse(audit.Anonymous(), by, "wrong_token_type",
			"subject_token: a delegation acts for the access token of a person's sign-in, never for %s", request.GetExchangeSubjectTokenType())
	}

	issued, err := getJSON[token](ctx, s.state, tokenKey(request.GetExchangeSubjectTokenIDOrToken()))
	if err != nil {
		return Proof{}, fault(ctx, err)
	}
	if issued == nil {
		return refuse(audit.Anonymous(), by, "no_record_of_token", "subject_token: this issuer holds no record of that access token")
	}
	if !strings.Contains(issued.Subject, "@") {
		return refuse(audit.Anonymous(), by, "not_a_person", "subject_token: a delegation acts for a person, and %q is not one", issued.Subject)
	}
	person := audit.Person(issued.Subject)

	live, err := s.recordLive(ctx, issued)
	if err != nil {
		return Proof{}, fault(ctx, err)
	}
	if !live {
		return refuse(person, by, "session_ended", "subject_token: the sign-in behind that access token has ended")
	}

	// A sign-in proved without the directory is never acted for, as it is
	// never a bearer ([Storage.liveSignInAtAnyClient]): the token minted would
	// carry groups no directory vouched for.
	method := issued.Method
	if issued.Session != "" {
		session, _, err := s.iss.Sessions().ByID(ctx, issued.Session)
		if err != nil {
			return Proof{}, fault(ctx, err)
		}
		method = session.Method
	}
	switch {
	case isRecovery(method):
		return refuse(person, by, "recovery_session", "subject_token: a sign-in made by recovery is not acted for")
	case method == ConformanceHow:
		return refuse(person, by, "conformance_session", "subject_token: a sign-in of the conformance account is not acted for")
	}

	prior, _ := issued.Claims["act"].(map[string]any)

	return Proof{Email: issued.Subject, Acting: &Acting{
		Actor:   actor,
		Prior:   prior,
		Session: issued.Session,
		SignIn:  issued.SignIn,
		Expires: issued.Expires,
	}}, nil
}

// actingOf is the [Acting] of a delegated exchange's grant, nil for any other
// request.
func actingOf(s *Storage, request op.TokenRequest) *Acting {
	exchange, ok := request.(op.TokenExchangeRequest)
	if !ok {
		return nil
	}
	grant, ok := s.grantFor(exchange)
	if !ok {
		return nil
	}
	return grant.Acting
}

// proofKindOf is the audit's proof kind for a record whose actor is who.
func proofKindOf(who audit.Actor) string {
	if who.Kind == "person" {
		return "person"
	}
	return ""
}
