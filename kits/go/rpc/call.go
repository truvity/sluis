package rpc

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/atoom/common/call"
)

// HeaderCorrelation is the header that carries the correlation id of a request through every internal call it
// causes (contracts.md, "Every call"). entry makes the id; the kit passes it on and never makes one. It is atoom's
// (common/call).
const HeaderCorrelation = call.HeaderCorrelation

// WithCorrelationID returns ctx carrying the correlation id. An id that is not a plain token (1 to 64 characters
// of letters, digits, "-" and "_") is dropped, so a hostile header cannot reach a log line or a record.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return call.WithCorrelationID(ctx, id)
}

// CorrelationID returns the correlation id of ctx, or "" when there is none.
func CorrelationID(ctx context.Context) string { return call.CorrelationID(ctx) }

// DefaultDeadlineValue is the deadline a call gets when it carries none and the server or client names no other
// (contracts.md, "Every call": every call carries a deadline).
const DefaultDeadlineValue = 30 * time.Second

// DefaultDeadline returns an interceptor that gives a call without a deadline d (atoom's call.DefaultDeadline). A
// call that has one keeps it: a callee never extends its caller's deadline (contracts.md, "Every call"). On a
// handler it covers unary and streaming methods; on a client it covers unary calls, because the lifetime of a
// stream is the caller's.
func DefaultDeadline(d time.Duration) connect.Interceptor { return call.DefaultDeadline(d) }
