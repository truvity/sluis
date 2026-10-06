package issuer

import (
	"context"
	"net/http"
)

// ClientSecretsHandlerForTest is the operator endpoint with a stubbed bearer
// verifier, so what is under test is who may manage, not the signature.
func ClientSecretsHandlerForTest(iss *Issuer, verify func(context.Context, string) (string, []string, error)) http.Handler {
	return clientSecretsHandler(iss, verify)
}
