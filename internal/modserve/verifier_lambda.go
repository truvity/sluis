//go:build lambda

package modserve

import (
	"errors"

	"github.com/truvity/sluis/internal/modcall"
)

// NewVerifier: the Lambda build has no cluster to review a token with. A
// function answers module calls as `rpc` events (internal/lambdaapp), and IAM
// decides who may invoke it.
func NewVerifier(string) (modcall.Verifier, error) {
	return nil, errors.New("serve: a listener is for Kubernetes; on Lambda the function answers rpc events")
}
