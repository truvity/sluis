//go:build !lambda

package modserve

import (
	"context"

	"github.com/truvity/sluis/internal/kube"
	"github.com/truvity/sluis/internal/modcall"
)

// NewVerifier checks a caller's projected ServiceAccount token with a
// TokenReview for the audience: a token minted for another audience, such as
// the pod's default one, is refused.
func NewVerifier(audience string) (modcall.Verifier, error) {
	c, err := kube.InCluster("")
	if err != nil {
		return nil, err
	}
	return Reviewer(c.ReviewToken, audience), nil
}

// Reviewer is the Verifier over a TokenReview call.
func Reviewer(review func(ctx context.Context, token string, audiences []string) (string, error), audience string) modcall.Verifier {
	return func(ctx context.Context, bearer string) (string, error) {
		return review(ctx, bearer, []string{audience})
	}
}
