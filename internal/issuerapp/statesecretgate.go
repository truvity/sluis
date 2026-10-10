package issuerapp

import (
	"context"
	"time"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/lazy"
	"github.com/truvity/sluis/internal/rails"
)

// stateSecretCheckTTL is how long a passed fingerprint check is reused. The
// check is a state read, not a secret read, and a replica started with another
// secret is found within one period of its first sign-in.
const stateSecretCheckTTL = 5 * time.Minute

// stateSecretGate runs the state-secret fingerprint check at the first
// sign-in, and again after its time to live, and not as the process starts: a
// start that checked it would write and read the shared state in every new
// environment of a herd. The check runs under the "state-secret-check" lease
// when it is free, so a herd of first sign-ins does one; if another replica
// holds it, the check runs here without the lease, which only reads.
type stateSecretGate struct {
	passed *lazy.Value[struct{}]
}

func newStateSecretGate(state issuer.State, seed []byte, leases *rails.Leases) *stateSecretGate {
	return newLazyStateSecretGate(state, func(context.Context) ([]byte, error) { return seed, nil }, leases)
}

// newLazyStateSecretGate is [newStateSecretGate] over a secret that is read
// when the check first runs, so that building the gate reads nothing.
func newLazyStateSecretGate(state issuer.State, secret func(context.Context) ([]byte, error), leases *rails.Leases) *stateSecretGate {
	return &stateSecretGate{passed: lazy.New(stateSecretCheckTTL, func(ctx context.Context) (struct{}, error) {
		seed, err := secret(ctx)
		if err != nil {
			return struct{}{}, err
		}
		var inner error
		if leases != nil {
			ran, err := leases.Do(ctx, "state-secret-check", "fingerprint", func(lctx context.Context) {
				inner = checkStateSecret(lctx, state, seed)
			})
			if err != nil {
				return struct{}{}, err
			}
			if ran {
				return struct{}{}, inner
			}
		}
		return struct{}{}, checkStateSecret(ctx, state, seed)
	})}
}

// ensure is nil when the secret is the one the other replicas use.
func (g *stateSecretGate) ensure(ctx context.Context) error {
	_, err := g.passed.Get(ctx)
	return err
}

// gatedSignIn runs the gate before it sends a browser to a provider or accepts
// one coming back.
type gatedSignIn struct {
	issuer.ContextSignIn
	gate *stateSecretGate
}

func (g gatedSignIn) URLContext(ctx context.Context, state string) (string, error) {
	if err := g.gate.ensure(ctx); err != nil {
		return "", err
	}
	return g.ContextSignIn.URLContext(ctx, state)
}

func (g gatedSignIn) Identify(ctx context.Context, code string) (string, error) {
	if err := g.gate.ensure(ctx); err != nil {
		return "", err
	}
	return g.ContextSignIn.Identify(ctx, code)
}

// gateSignIns puts the gate in front of every provider.
func gateSignIns(providers []issuer.SignIn, gate *stateSecretGate) []issuer.SignIn {
	if gate == nil {
		return providers
	}
	out := make([]issuer.SignIn, 0, len(providers))
	for _, p := range providers {
		if c, ok := p.(issuer.ContextSignIn); ok {
			out = append(out, gatedSignIn{ContextSignIn: c, gate: gate})
			continue
		}
		out = append(out, p)
	}
	return out
}
