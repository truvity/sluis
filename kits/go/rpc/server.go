package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/socket"
	"github.com/truvity/sluis/v2/kits/go/registry"
)

// readMaxBytes bounds a message a server reads and a message a client reads; it is far above any message of the
// contracts. The peer may be the adversary (key@ reads a signed document from store@), so no reader buffers more.
const readMaxBytes = 4 << 20

// ServerConfig is the configuration of one server: one actor or driver, on one socket.
type ServerConfig struct {
	Actor    string // the callee's registry name, e.g. "store"
	Boundary string
	Socket   string      // absolute path; see the socket layout in docs
	Mode     os.FileMode // 0660 for actor sockets, 0600 for driver sockets; 0 means 0660
	// GID, when not 0, is the group of the socket file. A driver whose owner is another uid sets it with Mode 0660, so
	// that the members of the group, and only they, can connect to the socket. The process must be a member of the
	// group. It changes who may connect and nothing else: the guard still decides, from the peer's uid, who may call.
	// The default, 0, leaves the group the kernel gave and the mode as it is.
	GID uint32
	// DefaultDeadline is the deadline of a call that carries none. 0 means DefaultDeadlineValue.
	DefaultDeadline time.Duration
	// Allow decides whether a peer may call. The default (nil) is registry.MayCall(peer.Name, Actor).
	// Drivers pass OwnerOnly(owner); key@ passes ExactCaller(name) per socket.
	Allow func(peer Peer, procedure string) bool
	// OnRefused, when set, is told of the calls the guard refuses before their handler is reached, with what the
	// transport knows: the reason ("caller" or "call_graph"), the peer and the procedure, never a payload. A holder uses
	// it to make the refusal an audit fact (ADR 0005: every refusal is audited; "Refusals at the guard are counted past a
	// rate"). The hook is rate limited here, for every holder: per peer uid and reason, the first RefusalBurst refusals
	// of each RefusalWindow are passed on one by one, and the rest are folded into one call at the end of the window with
	// Suppressed set to their number. The hook never runs on the goroutine of the request, and at most DefaultInflight
	// calls of it run at once; a refusal that finds them all busy is folded into the count. The hook may therefore block
	// (an audit emit may wait for its transport) without slowing a refused caller or the server. A panic in it is the
	// caller's.
	OnRefused func(Refused)
	// RefusalBurst is the number of refusals of one peer and reason that OnRefused hears one by one in a window; 0 is 10.
	RefusalBurst int
	// RefusalWindow is the length of that window; 0 is one minute.
	RefusalWindow time.Duration
	// RefusalHourly is the most facts (refusals heard and summaries) of one peer and reason in an hour; 0 is 60. What is over
	// is carried into a later summary.
	RefusalHourly int
	// RefusalTotal is the most facts of the whole socket in an hour, over all peers and reasons; 0 is 120. Past it the refusals
	// are counted in one shared summary.
	RefusalTotal int

	resolve NameResolver
}

// Refused is a call the guard refused, or the summary of the calls it refused and OnRefused did not hear one by one. For a
// call: the reason, the peer as the transport read it (Known is false and Name empty for a uid the registry does not
// know or of another boundary; UID is then still the kernel's) and the procedure, which is whatever the caller sent and
// not a name the holder can trust. For a summary Suppressed is the number of calls of this peer and reason in the window
// that were not reported, Procedure is empty, and Window is the length of the window.
type Refused struct {
	Reason     string
	Peer       Peer
	Known      bool
	Procedure  string
	Suppressed int
	Window     time.Duration
	// Overflow says that a summary is of the calls of every peer past the bound of counted peers: Peer is then empty.
	Overflow bool
}

// Handler is one generated service: the path and handler that New<Service>Handler returns.
type Handler struct {
	Path string
	H    http.Handler
}

// resolver is the uid-to-name table of the server: the registry's, unless a test gave its own.
func (c ServerConfig) resolver() NameResolver {
	if c.resolve == nil {
		return registry.NameForUID
	}
	return c.resolve
}

// ownBlock returns the uid block of this process: the block of the boundary this server serves, taken from the
// process's own uid as the registry maps it (REQ-ISO-072: uid = 61000 + ordinal + 1000 x block). A caller from
// another block is refused. It is false when the process's uid is not an actor or driver uid.
func (c ServerConfig) ownBlock() (uint32, bool) {
	name, block, ok := c.resolver()(uint32(os.Getuid())) //nolint:gosec // a uid is non-negative
	return block, ok && name != ""
}

func (c ServerConfig) deadline() time.Duration {
	if c.DefaultDeadline == 0 {
		return DefaultDeadlineValue
	}
	return c.DefaultDeadline
}

// HandlerOptions returns the options of every generated New<Service>Handler of a server: the interceptor with the
// default-deny check, and the read limit. The server checks every call at the HTTP layer first (atoom's socket
// guard, with the call graph as its rule), so a refused call is answered before its message is decoded.
func HandlerOptions(cfg ServerConfig) []connect.HandlerOption {
	return []connect.HandlerOption{
		connect.WithInterceptors(interceptor{guard: newGuard(cfg)}),
		connect.WithReadMaxBytes(readMaxBytes),
	}
}

// Serve serves the handlers on cfg.Socket through atoom's socket transport (github.com/truvity/atoom/socket): the
// socket is made fresh (a stale socket file is replaced, a symlink refused) with its mode and group, the peer
// credentials are read once per connection, and every call passes the guard before its message is decoded. The
// rule of the guard is sluis's: the registry names the peer's uid, the caller must be of this boundary's uid block,
// and the call graph (cfg.Allow) must allow it. A call that carries no deadline gets cfg's default, and the
// correlation id of a call reaches its handler's context. It returns when ctx ends, after a graceful shutdown
// bounded by 5 s.
func Serve(ctx context.Context, cfg ServerConfig, handlers ...Handler) error {
	if cfg.Actor == "" {
		return errors.New("rpc: ServerConfig.Actor is empty")
	}
	mode := cfg.Mode
	if mode == 0 {
		mode = 0o660
	}
	if _, ok := cfg.ownBlock(); !ok {
		return errors.New("rpc: the uid of this process is not an actor or driver uid of the registry")
	}
	if !filepath.IsAbs(cfg.Socket) {
		return fmt.Errorf("rpc: socket path %q is not absolute", cfg.Socket)
	}
	mux := http.NewServeMux()
	for _, h := range handlers {
		mux.Handle(h.Path, h.H)
	}
	g := newGuard(cfg)
	defer g.refusals.Close() // what the guard still counts is reported on the way out
	err := socket.Serve(ctx, socket.Config{
		Socket:          cfg.Socket,
		Mode:            mode,
		GID:             cfg.GID,
		Allow:           socket.AllowFunc(g.rule),
		OnRefused:       g.onRefused,
		DefaultDeadline: cfg.deadline(),
	}, "/", g.named(mux))
	if err != nil {
		return fmt.Errorf("rpc: serve %s: %w", cfg.Actor, err)
	}
	return nil
}
