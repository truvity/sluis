package issuer

import (
	"context"
	"net/http"
)

// ClientSecretsHandlerForTest is the operator endpoint with a stubbed bearer
// verifier, so what is under test is who may manage, not the signature.
func ClientSecretsHandlerForTest(iss *Issuer, verify func(context.Context, string) (string, []string, error)) http.Handler {
	return clientSecretsHandler(iss, func(ctx context.Context, bearer string) (string, []string, []string, error) {
		who, groups, err := verify(ctx, bearer)
		return who, groups, []string{"accessctl"}, err
	})
}
