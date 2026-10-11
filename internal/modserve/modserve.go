// Package modserve is the listener a GitHub or Slack module answers the issuer
// on, on Kubernetes (docs/decisions/0071): `POST /rpc` with a projected
// ServiceAccount token that a TokenReview accepts for the module's audience and
// that belongs to the issuer's ServiceAccount. It is what the Cloudflare
// module's listener is, with the caller's class decided by who the subject is
// and not by what its ServiceAccount is called.
package modserve

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/health"
	"github.com/truvity/sluis/internal/modcall"
)

// HealthPath answers 200 on the listener, for the probes.
const HealthPath = "/healthz"

// DefaultAddress is the listener when the document names none: the chart's
// `modules.<m>.rpcPort`.
const DefaultAddress = ":8081"

// The caller classes the issuer's pod holds. The issuer class asks for an
// installation token; the console class runs the console's pages. They are the
// names the methods allow (githubtokens.CallerIssuer, consolecall.Class).
const (
	classIssuer  = "issuer"
	classConsole = "console"
)

// Audience is the audience a caller's token must have been minted for.
func Audience(module string, s *config.ModuleServe) string {
	if s != nil && s.Audience != "" {
		return s.Audience
	}
	return module
}

// Address is where the listener binds.
func Address(s *config.ModuleServe) string {
	if s != nil && s.Address != "" {
		return s.Address
	}
	return DefaultAddress
}

// Mux is the module's listener: the calls at [modcall.RPCPath], which verify
// refuses before they reach a method and which only the issuers may make, and a
// health endpoint that needs no credential and tells nothing.
func Mux(s *modcall.Server, verify modcall.Verifier, issuers []string) http.Handler {
	holds := make(map[string][]string, len(issuers))
	for _, sub := range issuers {
		holds[sub] = []string{classIssuer, classConsole}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+HealthPath, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/", s.Handler(verify, modcall.WithSubjectClasses(holds)))
	return mux
}

// Serve answers the issuer's calls on the document's address until ctx ends.
// verify checks the bearer of a call.
func Serve(ctx context.Context, serve *config.ModuleServe, s *modcall.Server, verify modcall.Verifier, log *slog.Logger) error {
	return health.Serve(ctx, Address(serve), Mux(s, verify, serve.Issuers), log)
}
