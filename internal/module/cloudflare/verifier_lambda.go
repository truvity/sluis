//go:build lambda

package cloudflare

import (
	"errors"

	"github.com/truvity/sluis/internal/modcall"
)

// newVerifier: the Lambda build has no cluster to review a token with. A
// function answers module calls as `rpc` events (internal/lambdaapp), and IAM
// decides who may invoke it.
func newVerifier(string) (modcall.Verifier, error) {
	return nil, errors.New("cloudflare.serve.address: a listener is for Kubernetes; on Lambda the function answers rpc events")
}
