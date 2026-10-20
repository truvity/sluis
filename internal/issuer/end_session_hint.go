package issuer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/refusal"
)

// hintCheck says why an `/end_session` request's `id_token_hint` is not one
// this issuer signed for one of its clients, or nil when it is.
type hintCheck func(ctx context.Context, hint string) error

// endSessionHints verifies a hint as the library will, before anything else
// reads it: the issuer, a signature by one of this issuer's own keys in an
// algorithm it signs with, and then that every client the hint names is one
// this installation has.
//
// The order is the point. The hint's `aud` and `azp` are read only from a
// token that verified, so a hint somebody wrote never reaches a client lookup:
// no client-document fetch, and no `invalid_client` refusal for a name the
// request made up. A token this issuer signed names a client it already
// admitted, and looking that one up is what the library does next anyway.
//
// An expired hint the issuer signed is still a hint, as RP-Initiated Logout
// 1.0 2 asks and the library allows. It is checked for its clients like any
// other.
func endSessionHints(issuerURL string, verifier func(context.Context) *op.IDTokenHintVerifier,
	known func(ctx context.Context, clientID string) bool) hintCheck {
	return func(ctx context.Context, hint string) error {
		// The library reads its issuer from the context it sets on its own
		// routes, and this runs in front of them.
		ctx = op.ContextWithIssuer(ctx, issuerURL)

		claims, err := op.VerifyIDTokenHint[*oidc.IDTokenClaims](ctx, hint, verifier(ctx))
		if err != nil && !errors.As(err, &op.IDTokenHintExpiredError{}) {
			return err
		}

		names := slices.Clone([]string(claims.Audience))
		if claims.AuthorizedParty != "" && !slices.Contains(names, claims.AuthorizedParty) {
			names = append(names, claims.AuthorizedParty)
		}

		if len(names) == 0 {
			return errors.New("it names no client")
		}

		for _, name := range names {
			if !known(ctx, name) {
				return fmt.Errorf("it names %q, which is not a client of this installation", name)
			}
		}

		return nil
	}
}

// knownClient reports whether a client id is one this installation has: a
// declared client, or a client document the allow-list admits and that
// resolves. It logs no refusal of its own; the caller's refusal carries the
// status.
func knownClient(iss *Issuer, storage op.Storage) func(context.Context, string) bool {
	return func(ctx context.Context, clientID string) bool {
		if _, ok := iss.Policy().Client(clientID); ok {
			return true
		}

		concrete, ok := storage.(*Storage)
		if !ok || concrete.documents == nil {
			return false
		}

		_, err := concrete.documents.Resolve(ctx, clientID)

		return err == nil
	}
}

// refuseHint is the answer to an `/end_session` request whose hint failed
// [hintCheck]: the invalid-request page, or the OAuth error for a caller that
// is not a browser, with 400 and the reason in the log.
func refuseHint(w http.ResponseWriter, r *http.Request, cause error) {
	logRefusal(r, http.StatusBadRequest, "id_token_hint invalid", cause, nil)
	endSessionPage(w, r, http.StatusBadRequest, string(oidc.InvalidRequest), refusal.InvalidRequest)
}
