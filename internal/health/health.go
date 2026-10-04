// Package health serves the two probes, and keeps them different.
//
// **Liveness** answers whether the process is alive. It follows nothing
// outside itself, on purpose: a probe that fails when a dependency fails
// restarts every replica at once, and a store that is briefly
// unreachable becomes an outage rather than a degraded minute.
//
// **Readiness** answers whether this replica can do its job. It DOES
// follow the dependencies the process cannot work without. A hub that
// cannot read a snapshot and an issuer that cannot find a session are
// both unable to serve, and reporting ready through that is how a fault
// turns into a long hang at the gateway instead of a fast refusal and a
// red line in `kubectl get pods`.
//
// The distinction is the fix for a real outage: on 2026-09-10 a Valkey
// pod moved to a new address, two services kept dialling the old one for
// half an hour, and both reported ready throughout. A human had to
// notice and restart them.
package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// DefaultTimeout bounds every readiness check together. It is short
// because a readiness probe that waits is a readiness probe that lies
// for as long as it waits: the kubelet's own timeout would fire first
// and report a timeout rather than the reason.
const DefaultTimeout = 2 * time.Second

// Dependency is something readiness follows: what to call it in the
// answer, and how to ask it.
type Dependency struct {
	Name  string
	Check func(ctx context.Context) error
}

// Pinger is anything readiness can ask. The Valkey-backed stores
// implement it; the in-memory ones deliberately do not, because a store
// that lives inside the process is not a dependency outside it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Follow returns a Dependency for a store that can be asked, and a zero
// Dependency for one that cannot. That is how an installation with an
// in-memory store ends up following nothing, without the caller having
// to know which it got.
func Follow(name string, store any) Dependency {
	pinger, ok := store.(Pinger)
	if !ok {
		return Dependency{}
	}

	return Dependency{Name: name, Check: pinger.Ping}
}

// Mux serves `/healthz` and `/readyz`.
//
// Dependencies are checked in order and the first failure answers, with
// its name and its error, so that an operator reading a probe failure
// learns which dependency and why rather than that something is wrong.
// No dependencies is a valid configuration -- an installation with no
// shared store has nothing outside itself to follow -- and readiness
// then means the same as liveness.
func Mux(timeout time.Duration, deps ...Dependency) *http.ServeMux {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		for _, dep := range deps {
			if dep.Check == nil {
				continue
			}

			if err := dep.Check(ctx); err != nil {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprintf(w, "%s does not answer: %v", dep.Name, err)

				return
			}
		}

		_, _ = w.Write([]byte("ok"))
	})

	return mux
}

// Gate is a readiness dependency that is closed until the process says its
// start-up is done. A controller has no store of its own to follow, so the
// one thing its readiness can honestly answer is "did I start": the policy
// loaded, the stores open, the audit catalogue accepted. A pod that crashes
// before then never opens the listener at all, and one that is still
// starting answers 503, so a rolling update does not remove the old pod
// until the new one is past the point the last release died at.
type Gate struct {
	name  string
	ready atomic.Bool
}

// NewGate returns a closed gate; name is what the probe's answer calls it.
func NewGate(name string) *Gate { return &Gate{name: name} }

// Open says start-up succeeded. Readiness answers 200 from then on.
func (g *Gate) Open() { g.ready.Store(true) }

// Dependency is the gate as something [Mux] follows.
func (g *Gate) Dependency() Dependency {
	return Dependency{Name: g.name, Check: func(context.Context) error {
		if !g.ready.Load() {
			return errors.New("has not finished starting")
		}
		return nil
	}}
}

// Serve serves handler on addr until ctx ends, and returns the listener's
// error when it cannot bind. A listener that shuts down on ctx is not an
// error.
func Serve(ctx context.Context, addr string, handler http.Handler, log *slog.Logger) error {
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.InfoContext(ctx, "listening", "listener", "health", "address", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("health listener: %w", err)
	}
	return nil
}
