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
// Building takes the exclusive side and serving the shared side, so requests
// run in parallel with each other and a build waits for those in flight.
//
// The lock is deliberately reader-preferring and re-entrant for readers. Some
// tests make a request from inside another request (a code redeemed from
// within a sign-out's Announce, a second request while the first is filed), so
// a goroutine holding the shared side takes it again. sync.RWMutex would block
// that second RLock whenever a parallel test is waiting in Lock, and the outer
// request would then never finish: a deadlock. Here a reader is turned away
// only while a build is ACTIVE, and a build cannot be active while any reader
// holds the lock, so a nested read always proceeds. The price is that a steady
// stream of requests can delay a build, which a finite test run does not have.
var libraryEndpoints = newEndpointsLock()

type endpointsLock struct {
	mu      sync.Mutex
	cond    *sync.Cond
	readers int
	writing bool
}

func newEndpointsLock() *endpointsLock {
	l := &endpointsLock{}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *endpointsLock) RLock() {
	l.mu.Lock()
	for l.writing {
		l.cond.Wait()
	}
	l.readers++
	l.mu.Unlock()
}

func (l *endpointsLock) RUnlock() {
	l.mu.Lock()
	l.readers--
	if l.readers == 0 {
		l.cond.Broadcast()
	}
	l.mu.Unlock()
}

func (l *endpointsLock) Lock() {
	l.mu.Lock()
	for l.writing || l.readers > 0 {
		l.cond.Wait()
	}
	l.writing = true
	l.mu.Unlock()
}

func (l *endpointsLock) Unlock() {
	l.mu.Lock()
	l.writing = false
	l.cond.Broadcast()
	l.mu.Unlock()
}

// guarded serves h holding the shared side of [libraryEndpoints].
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
