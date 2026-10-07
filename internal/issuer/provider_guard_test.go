package issuer_test

import (
	"net/http"
	"sync"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// libraryEndpoints serialises building a provider against serving one.
//
// zitadel/oidc's op.NewProvider starts every provider from the SAME
// package-level *Endpoints (op.DefaultEndpoints) and its WithCustom*Endpoint
// options assign into it, so each construction writes shared memory that every
// already-running provider's handlers read. A process builds one provider, so
// nothing in production meets this; a test binary builds one per test, in
// parallel, and the race detector reports every pair.
//
// Building takes the write lock and serving takes the read lock, so requests
// run in parallel with each other and a build waits for those in flight.
var libraryEndpoints sync.RWMutex

// guarded serves h holding the read side of [libraryEndpoints].
func guarded(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		libraryEndpoints.RLock()
		defer libraryEndpoints.RUnlock()
		h.ServeHTTP(w, r)
	})
}

// handler is [issuer.Handler] safe to call from parallel tests.
func handler(iss *issuer.Issuer, storage op.Storage) (http.Handler, error) {
	libraryEndpoints.Lock()
	defer libraryEndpoints.Unlock()
	h, err := issuer.Handler(iss, storage)
	if err != nil {
		return nil, err
	}
	return guarded(h), nil
}

// handlerWithSignIn is [issuer.HandlerWithSignIn] safe to call from parallel tests.
func handlerWithSignIn(iss *issuer.Issuer, storage op.Storage, signIn issuer.SignInDeps) (http.Handler, error) {
	libraryEndpoints.Lock()
	defer libraryEndpoints.Unlock()
	h, err := issuer.HandlerWithSignIn(iss, storage, signIn)
	if err != nil {
		return nil, err
	}
	return guarded(h), nil
}
