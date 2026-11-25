package issuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/storage/logattr"
)

// A client that authenticates with `private_key_jwt` signs a short assertion
// for every request to the token and revocation endpoints (RFC 7523 2.2). The
// library checks its signature against the client's `jwks`
// ([Storage.GetKeyByIDAndClientID]), that `iss` and `sub` are the client,
// that `aud` names this issuer, `exp`, and that `iat` is within the hour. It
// checks nothing that stops the same assertion being presented twice, which
// RFC 7523 3 (item 7) leaves to the server; this does, once the signature has
// verified (the vendored library's one hook for it, a key storage's
// `ClaimJWTAssertion`: third_party/zitadel-oidc/README.md).
//
// Each accepted assertion leaves a marker, `issuer:assertion:<sha256 of the
// client and the jti>`, written with the store's conditional create, until
// the assertion expires and a minute more: of two presentations of one
// assertion, however close, exactly one is accepted. The marker holds
// nothing but the client's id; never the assertion.

// maxAssertionLife is the longest an assertion may still be valid when it is
// presented. Its marker lives as long, so a longer one would cost a longer
// marker for nothing: an assertion is made for one request, and a client
// that signs one per call needs a minute, not an hour.
const maxAssertionLife = 10 * time.Minute

// assertionMarkerSlack outlives the assertion's `exp` by the library's clock
// offset and then some, so a marker never expires while its assertion could
// still verify.
const assertionMarkerSlack = time.Minute

func assertionKey(clientID, jti string) string {
	return "issuer:assertion:" + hashHex(clientID+"\x00"+jti)
}

// ClaimJWTAssertion is the vendored library's hook: it spends a verified
// client assertion, and refuses one with no `jti`, one valid for longer than
// [maxAssertionLife], and one already spent, each as `invalid_client`.
//
// Under maintenance, when the store takes no writes, nothing is written and
// the assertion is accepted, as a redeemed code leaves no tombstone then
// ([Storage.spend]): the assertion's own short life bounds the window.
func (s *Storage) ClaimJWTAssertion(ctx context.Context, request *oidc.JWTTokenRequest) error {
	client := logattr.SafeString("client", request.Issuer)
	if request.JWTID == "" {
		return deny(ctx, oidc.ErrInvalidClient, errors.New("the client assertion has no jti, so it cannot be spent once"), client)
	}

	left := request.ExpiresAt.AsTime().Sub(s.now())
	if left > maxAssertionLife {
		return deny(ctx, oidc.ErrInvalidClient,
			fmt.Errorf("the client assertion is valid for %s more, at most %s", left.Round(time.Second), maxAssertionLife), client)
	}

	if maintenance.ReadOnly(ctx) {
		return nil
	}

	marker, err := json.Marshal(struct {
		Client string `json:"client"`
	}{request.Issuer})
	if err != nil {
		return fault(ctx, fmt.Errorf("issuer: encode a client assertion marker: %w", err), client)
	}

	won, err := s.state.SetIfAbsent(ctx, assertionKey(request.Issuer, request.JWTID),
		marker, max(left, 0)+assertionMarkerSlack)
	if err != nil {
		return fault(ctx, fmt.Errorf("issuer: spend a client assertion: %w", err), client)
	}

	if !won {
		return deny(ctx, oidc.ErrInvalidClient, errors.New("the client assertion was already presented"), client)
	}

	return nil
}
