package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/refusal"
	"github.com/truvity/atoom/socket"
	"github.com/truvity/sluis/v2/kits/go/registry"
)

// The reasons of the two refusals the guard makes (docs/reference/contracts.md, "Errors").
const (
	reasonCaller    = "caller"     // no peer, or a uid the registry does not know, or another boundary's uid block
	reasonCallGraph = "call_graph" // a known caller that the call graph does not allow
)

// guard is the default-deny check of one server: who is the caller, and may it make this call. It is the rule of
// atoom's socket guard (socket.AllowFunc), which asks it before the message is read.
type guard struct {
	actor      string
	block      uint32
	blockKnown bool // false: this process's own uid is not in the registry, and every call is refused
	allow      func(Peer, string) bool
	resolve    NameResolver
	refusals   *Limiter
}

func newGuard(cfg ServerConfig) guard {
	g := guard{actor: cfg.Actor, allow: cfg.Allow, resolve: cfg.resolver(), refusals: newRefusalLimiter(cfg)}
	g.block, g.blockKnown = cfg.ownBlock()
	if g.allow == nil {
		g.allow = func(p Peer, _ string) bool { return registry.MayCall(p.Name, cfg.Actor) }
	}
	return g
}

// name maps the peer the kernel reported to the registry: known when the registry names its uid.
func (g guard) name(p socket.Peer) connPeer {
	cp := connPeer{Peer: Peer{UID: p.UID, GID: p.GID, PID: p.PID}}
	if name, block, ok := g.resolve(p.UID); ok && name != "" {
		cp.Name, cp.UIDBlock, cp.known = name, block, true
	}
	return cp
}

// decide returns "" when the caller may call procedure, and the reason of the refusal otherwise.
func (g guard) decide(cp connPeer, procedure string) string {
	if !g.blockKnown || !cp.known || cp.UIDBlock != g.block {
		return reasonCaller
	}
	if !g.allow(cp.Peer, procedure) {
		return reasonCallGraph
	}
	return ""
}

// rule is the host rule of atoom's guard: the peer of the transport, named and checked.
func (g guard) rule(p socket.Peer, procedure string) string {
	return g.decide(g.name(p), procedure)
}

// onRefused is told by atoom's guard of every call it refused, on the goroutine of the request; the limiter it
// hands the refusal to does not block.
func (g guard) onRefused(r socket.Refused) {
	var cp connPeer
	if r.Known {
		cp = g.name(r.Peer)
	}
	g.logRefused(r.Reason, cp, r.Procedure)
}

// named puts the named caller in the context of the handler, for PeerFromContext. It runs behind atoom's guard, so
// only for an admitted call.
func (g guard) named(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := socket.PeerFromContext(r.Context()); ok {
			r = r.WithContext(withPeer(r.Context(), g.name(p)))
		}
		next.ServeHTTP(w, r)
	})
}

// check returns nil when the caller of ctx may call procedure, and the refusal otherwise. It looks at the
// transport only: ctx carries the caller that named put there.
func (g guard) check(ctx context.Context, procedure string) *connect.Error {
	cp, _ := ctx.Value(peerKey{}).(connPeer)
	if reason := g.decide(cp, procedure); reason != "" {
		g.logRefused(reason, cp, procedure)
		return refusal.New(connect.CodePermissionDenied, reason, "")
	}
	return nil
}

// logRefused hands the refusal to the limiter, which passes it to the holder's hook and to the journal at a bounded rate:
// the first calls of a peer and reason in a window one by one, the rest as a count.
func (g guard) logRefused(reason string, cp connPeer, procedure string) {
	g.refusals.Note(fmt.Sprintf("%d/%t/%s", cp.UID, cp.known, reason), Refused{Reason: reason, Peer: cp.Peer, Known: cp.known, Procedure: procedure})
}

// newRefusalLimiter makes the limiter of a guard: the journal line of each refusal, and the holder's hook, run off the
// goroutine of the request.
func newRefusalLimiter(cfg ServerConfig) *Limiter {
	actor := cfg.Actor
	event := func(v any) {
		r := v.(Refused)
		slog.Info("call refused", "reason", r.Reason, "actor", actor, "procedure", r.Procedure,
			"caller", r.Peer.Name, "uid", r.Peer.UID, "pid", r.Peer.PID)
		if cfg.OnRefused != nil {
			cfg.OnRefused(r)
		}
	}
	// A request that is still running when the server stops reaches a closed limiter: its refusal is reported on its own
	// (Late), as the holder's hook does for any fact, and is not dropped.
	return NewLimiter(LimiterConfig{Burst: cfg.RefusalBurst, Window: cfg.RefusalWindow, Hourly: cfg.RefusalHourly, Total: cfg.RefusalTotal, Async: true, Late: event},
		event,
		func(v any, f Fold) {
			r := v.(Refused)
			r.Procedure, r.Suppressed, r.Window, r.Overflow = "", f.Suppressed, f.Window, f.Overflow
			if f.Overflow {
				r.Peer, r.Known = Peer{}, false
			}
			if cfg.OnRefused != nil {
				cfg.OnRefused(r)
			}
		})
}

// interceptor is the Connect interceptor of every procedure: the same check as the guard's. The server runs the
// check before the message is decoded (atoom's socket guard); this one is the second lock for a handler that is
// mounted another way.
type interceptor struct {
	guard guard
}

func (i interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := i.guard.check(ctx, req.Spec().Procedure); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (i interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := i.guard.check(ctx, conn.Spec().Procedure); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}
