package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/truvity/sluis/internal/cloudflare/rpc"
	"github.com/truvity/sluis/internal/health"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modserve"
)

// HealthPath answers 200 on the module's listener, for the probes.
const HealthPath = modserve.HealthPath

// Serves says whether the document makes the module answer other modules.
func (c Config) Serves() bool { return c.cloudflare != nil && c.cloudflare.Serve != nil }

// Audience is the audience a caller's token must have been minted for.
func (c Config) Audience() string {
	if c.Serves() && c.cloudflare.Serve.Audience != "" {
		return c.cloudflare.Serve.Audience
	}
	return rpc.Module
}

// Mux is the module's listener (internal/modserve): the calls at
// [modcall.RPCPath], which verify refuses before they reach a method and which
// only the issuers' subjects may make, and a health endpoint that needs no
// credential and tells nothing.
func Mux(s *modcall.Server, verify modcall.Verifier, issuers []string) http.Handler {
	return modserve.Mux(s, verify, issuers)
}

// ServeRPC answers other modules' calls on the document's address until ctx
// ends. verify checks the bearer of a call; the document's issuers are the
// only subjects let in.
func (a *App) ServeRPC(ctx context.Context, cfg Config, verify modcall.Verifier) error {
	if !cfg.Serves() || cfg.cloudflare.Serve.Address == "" {
		return errors.New("cloudflare.serve.address: the module has no address to listen on")
	}
	if len(cfg.cloudflare.Serve.Issuers) == 0 {
		return errors.New("cloudflare.serve.issuers: name the issuer's ServiceAccount (system:serviceaccount:<ns>:<name>); without it no caller is let in")
	}
	return health.Serve(ctx, cfg.cloudflare.Serve.Address, Mux(a.rpc, verify, cfg.cloudflare.Serve.Issuers), a.log)
}

// Flush delivers the audit records still queued: a Lambda function calls it
// before an invocation returns, since the process is frozen after it.
func (a *App) Flush(ctx context.Context) error { return a.trail.Flush(ctx) }
