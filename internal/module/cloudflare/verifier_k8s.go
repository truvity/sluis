//go:build !lambda

package cloudflare

import (
	"context"

	"github.com/truvity/sluis/internal/kube"
	"github.com/truvity/sluis/internal/modcall"
)

// newVerifier checks a caller's projected ServiceAccount token with a
// TokenReview for the audience (docs/decisions/0071, 4): a token minted for
// another audience, such as the pod's default one, is refused.
func newVerifier(audience string) (modcall.Verifier, error) {
	c, err := kube.InCluster("")
	if err != nil {
		return nil, err
	}
	return reviewer(c.ReviewToken, audience), nil
}

// reviewer is the Verifier over a TokenReview call.
func reviewer(review func(ctx context.Context, token string, audiences []string) (string, error), audience string) modcall.Verifier {
	return func(ctx context.Context, bearer string) (string, error) {
		return review(ctx, bearer, []string{audience})
	}
}
