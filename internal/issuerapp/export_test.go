package issuerapp

import (
	"context"
	"log/slog"
	"time"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/secrets"
)

// StartPassForTest is the pass [App.Run] makes before it serves.
func (a *App) StartPassForTest(ctx context.Context) { a.reconcileGenerated(ctx) }

// OpenSignInForTest is openSignIn over a secrets source, without the rest of
// the service.
func OpenSignInForTest(ctx context.Context, src secrets.Source, issuerURL, provider string) ([]issuer.SignIn, error) {
	return openSignIn(ctx, Config{secrets: src, issuerURL: issuerURL, oauthProvider: provider}, slog.New(slog.DiscardHandler))
}

// SetSignInClockForTest moves the clock the Google sign-in's client is cached
// by.
func SetSignInClockForTest(p issuer.SignIn, now func() time.Time) {
	p.(*googleSignIn).client.SetClock(now)
}

// GateSignInsForTest puts the state-secret gate, over state, seed and a lease
// on leaseState, in front of providers, and returns the clock to move.
func GateSignInsForTest(
	providers []issuer.SignIn, state issuer.State, seed []byte, leaseState port.State,
) ([]issuer.SignIn, func(func() time.Time)) {
	gate := newStateSecretGate(state, seed, &rails.Leases{State: leaseState, Holder: "test"})
	return gateSignIns(providers, gate), gate.passed.SetClock
}
