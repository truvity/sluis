// Package rpc is the Connect transport of every Go actor and driver of sluis: a server and a client over Unix
// sockets, the identity of the caller from the socket, the default-deny call-graph check, and the deadline and
// correlation id of every call (ADR 0003, "Transports" and "The call graph"; docs/reference/contracts.md,
// "Every call").
//
// The socket transport is atoom's (github.com/truvity/atoom/socket and common/call): the socket file, the peer
// credentials, the guard before the message is decoded, the deadline and the correlation id. What is sluis's is
// the rule of the guard: the server maps the peer's uid to an actor or driver name through the registry, and
// refuses every call whose caller is of another boundary or that the call graph does not allow. No request field
// names a caller. The audit of refusals (the rate-limited OnRefused) and the read limit are sluis's too.
//
// A unit holds one actor; fusion is not built.
package rpc
