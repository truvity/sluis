package issuer

import (
	"context"
	"net/http"
)

// ClientSecretsHandlerWithAudiencesForTest is the operator endpoint with a
// stubbed verifier that also says which clients the token was issued to (its
// audience and authorized party).
func ClientSecretsHandlerWithAudiencesForTest(
	iss *Issuer, verify func(context.Context, string) (string, []string, []string, error),
) http.Handler {
	return clientSecretsHandler(iss, verify)
}
