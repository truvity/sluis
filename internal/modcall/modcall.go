// Package modcall is how one module calls another (docs/decisions/0071 D93 a):
// a typed request and response over one of three transports, behind one Go
// interface chosen by configuration.
//
//   - [Local] calls a [Server] in the same process: tests, and every deployment
//     that has not split the callee out.
//   - [HTTPCaller] posts to the callee's Service on Kubernetes, with a bearer
//     the callee verifies ([Server.Handler]).
//   - internal/modcall/lambdacall invokes the callee's function through its
//     `live` alias on Lambda; it is a package of its own so that a Kubernetes
//     build does not carry the Lambda client.
//
// A call is a module name, a method name and a JSON payload. The callee answers
// a JSON result or a coded [Error]; anything else a handler returns is reported
// as an internal error with no detail, since the caller is another module's
// trust domain.
package modcall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Kind is the `kind` of a Lambda event that carries a module call, beside the
// scheduler's ticks.
const Kind = "rpc"

// Codes of an [Error]. A module may define more for its own methods.
const (
	CodeInternal     = "internal"
	CodeBadRequest   = "bad_request"
	CodeNoSuchMethod = "no_such_method"
	CodeUnavailable  = "unavailable"
)

// Request is one call on the wire.
type Request struct {
	Kind    string          `json:"kind"`
	Module  string          `json:"module"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Response is its answer: a result, or an error.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a failure the callee chose to tell: a code a caller can switch on and
// a message that is safe to show another module.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Error implements error.
func (e *Error) Error() string {
	if e.Message == "" {
		return "module call: " + e.Code
	}
	return "module call: " + e.Code + ": " + e.Message
}

// Is makes two errors with one code equal.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Coded is an error a handler returns to answer with code and message.
func Coded(code, message string) error { return &Error{Code: code, Message: message} }

// Caller sends one call to a module and returns its raw result.
type Caller interface {
	Call(ctx context.Context, module, method string, payload []byte) ([]byte, error)
}

// Do is a typed call.
func Do[Req, Resp any](ctx context.Context, c Caller, module, method string, req Req) (Resp, error) {
	var out Resp
	payload, err := json.Marshal(req)
	if err != nil {
		return out, fmt.Errorf("module call %s.%s: %w", module, method, err)
	}
	raw, err := c.Call(ctx, module, method, payload)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("module call %s.%s: the result is not valid: %w", module, method, err)
	}
	return out, nil
}

// handler is a registered method.
type handler func(ctx context.Context, payload []byte) (any, error)

// Server dispatches calls to the methods a module registered.
type Server struct {
	module string

	mu       sync.RWMutex
	handlers map[string]handler
}

// NewServer is the server of one module.
func NewServer(module string) *Server { return &Server{module: module, handlers: map[string]handler{}} }

// Module is the module the server answers for.
func (s *Server) Module() string { return s.module }

// Handle registers a typed method. A method registered twice is a programming
// error and panics.
func Handle[Req, Resp any](s *Server, method string, fn func(ctx context.Context, req Req) (Resp, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.handlers[method]; dup {
		panic("modcall: " + s.module + "." + method + " is registered twice")
	}
	s.handlers[method] = func(ctx context.Context, payload []byte) (any, error) {
		var req Req
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &req); err != nil {
				return nil, Coded(CodeBadRequest, "the request is not valid")
			}
		}
		return fn(ctx, req)
	}
}

// Dispatch answers one request. It never returns a Go error: a failure is the
// response's.
func (s *Server) Dispatch(ctx context.Context, req Request) Response {
	if req.Module != s.module {
		return Response{Error: &Error{Code: CodeNoSuchMethod, Message: "no such module"}}
	}
	s.mu.RLock()
	h, ok := s.handlers[req.Method]
	s.mu.RUnlock()
	if !ok {
		return Response{Error: &Error{Code: CodeNoSuchMethod, Message: "no such method"}}
	}
	out, err := h(ctx, req.Payload)
	if err != nil {
		var coded *Error
		if errors.As(err, &coded) {
			return Response{Error: coded}
		}
		return Response{Error: &Error{Code: CodeInternal}}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return Response{Error: &Error{Code: CodeInternal}}
	}
	return Response{Result: raw}
}

// Local calls servers in this process, through the same JSON the other
// transports carry, so what works here works across a boundary.
type Local map[string]*Server

// Call implements [Caller].
func (l Local) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	s, ok := l[module]
	if !ok {
		return nil, &Error{Code: CodeNoSuchMethod, Message: "no such module"}
	}
	return Result(s.Dispatch(ctx, Request{Kind: Kind, Module: module, Method: method, Payload: payload}))
}

// Result is a response as a caller sees it.
func Result(r Response) ([]byte, error) {
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}

type callerKey struct{}

// WithCaller records who the transport authenticated the call as.
func WithCaller(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, callerKey{}, subject)
}

// CallerOf is who the transport authenticated the call as; empty for an
// in-process call or a transport that cannot say.
func CallerOf(ctx context.Context) string {
	s, _ := ctx.Value(callerKey{}).(string)
	return s
}
