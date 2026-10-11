package issuer

import (
	"context"
	"net/http"
)

// PolicyExplainHandlerForTest is the explain endpoint with a stubbed bearer
// verifier, so what is under test is who may explain, not the signature.
func PolicyExplainHandlerForTest(iss *Issuer, verify func(context.Context, string) (string, []string, error)) http.Handler {
	return policyExplainHandler(iss, func(ctx context.Context, bearer string) (string, []string, []string, error) {
		who, groups, err := verify(ctx, bearer)
		return who, groups, []string{"sluisctl"}, err
	})
}

// PolicyExplainHandlerForAudienceTest is the same with the audiences the
// token was issued to chosen by the test.
func PolicyExplainHandlerForAudienceTest(
	iss *Issuer, verify func(context.Context, string) (string, []string, error), audiences ...string,
) http.Handler {
	return policyExplainHandler(iss, func(ctx context.Context, bearer string) (string, []string, []string, error) {
		who, groups, err := verify(ctx, bearer)
		return who, groups, audiences, err
	})
}
