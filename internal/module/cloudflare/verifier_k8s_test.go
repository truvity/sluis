//go:build !lambda

package cloudflare

import (
	"context"
	"testing"
)

func TestTheReviewIsForTheAudience(t *testing.T) {
	var got []string
	v := reviewer(func(_ context.Context, token string, audiences []string) (string, error) {
		got = audiences
		return "system:serviceaccount:sluis:issuer", nil
	}, "cloudflare")
	if sub, err := v(context.Background(), "tok"); err != nil || sub == "" {
		t.Fatal(sub, err)
	}
	if len(got) != 1 || got[0] != "cloudflare" {
		t.Errorf("%v", got)
	}
}
