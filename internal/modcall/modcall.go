// Package modcall is how one module calls another (docs/decisions/0071 D93 a):
// a typed request and response over one of three transports, behind one Go
// interface chosen by configuration.
//
//   - [Local] calls a [Server] in the same process: tests, and every deployment
//     that has not split the callee out.
//   - [HTTPCaller] posts to the callee's Service on Kubernetes, with a bearer
//     the callee verifies ([Server.Handler]).
//   - internal/modcall/lambdacall invokes the callee's function through one
//     alias per caller class (`live-issuer`, `live-console`, ...) on Lambda; it is a package of its own so that a Kubernetes
//     build does not carry the Lambda client.
//
// A call is a module name, a method name and a JSON payload. The callee answers
// a JSON result or a coded [Error]; anything else a handler returns is reported
// as an internal error with no detail, since the caller is another module's
// trust domain.
//
// Who is calling is the transport's to say, never the payload's: [Local] knows
// it from its constructor ([Local.As]), HTTP from the verified token, Lambda
// from the alias the function was invoked through. A method that registers
// [Allow] answers only those caller classes and refuses the rest with
// [CodeForbidden].
//
// The envelope also carries the caller's trace (`traceparent`, `tracestate`),
// its deadline, and is bounded in size; a callee answers a request of the
// previous version in that version. See [Request] and [Dispatch].
package modcall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/sluis/internal/telemetry"
	"github.com/truvity/sluis/storage/logattr"
)

// Kind is the `kind` of a Lambda event that carries a module call, beside the
// scheduler's ticks.
const Kind = "rpc"

// Version is the protocol version a caller writes in [Request.V]. Version 1,
// which wrote none, is read as 1. A callee answers versions [MinVersion] to
// Version, each in the caller's own version: a release is deployed one function
// at a time, so for a while the two sides differ by one.
const Version = 2

// MinVersion is the oldest version a callee still answers (N-1).
const MinVersion = Version - 1

// The size bounds of a call. A method answers requests and responses of at most
// [DefaultMaxBytes] unless it registers [MaxBytes]; no method may exceed
// [HardMaxBytes], which keeps a call under Lambda's 6 MB synchronous payload.
const (
	DefaultMaxBytes = 256 << 10
	HardMaxBytes    = 5 << 20
	// envelopeSlack is what the envelope's own fields add to a payload.
	envelopeSlack = 4 << 10
)

// Codes of an [Error]. A module may define more for its own methods.
const (
	CodeInternal     = "internal"
	CodeBadRequest   = "bad_request"
	CodeNoSuchMethod = "no_such_method"
	CodeUnavailable  = "unavailable"
	CodeForbidden    = "forbidden"
	// CodeDeadlineExceeded is a call whose deadline had passed when it arrived,
	// or passed while the method ran.
	CodeDeadlineExceeded = "deadline_exceeded"
)

// Request is one call on the wire. A reader ignores fields it does not know, so
// a later version can add some without breaking this one.
type Request struct {
	// V is the protocol version; absent is 1.
	V       int             `json:"v,omitempty"`
	Kind    string          `json:"kind"`
	Module  string          `json:"module"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload,omitempty"`

	// Version 2 and later. A version 1 request has none of them, and a callee
	// ignores them on one.
	//
	// Traceparent and Tracestate are the caller's W3C trace context; the callee
	// continues the trace with a span of its own.
	Traceparent string `json:"traceparent,omitempty"`
	Tracestate  string `json:"tracestate,omitempty"`
	// Deadline is the caller's deadline as Unix milliseconds. The callee bounds
	// its work by it, and refuses a call that arrives after it. It is read
	// against the callee's clock, so hosts need ordinary time sync.
	Deadline int64 `json:"deadline,omitempty"`
}

// Response is its answer: a result, or an error. V is the version the callee
// answered in, the caller's own; version 1 wrote none.
type Response struct {
	V      int             `json:"v,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a failure the callee chose to tell: a code a caller can switch on and
// a message that is safe to show another module.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`

	// cause is what went wrong behind a code that tells nothing. It is never
	// sent: the callee's log has it ([Server.SetLogger]).
	cause error
}

// Unwrap is the cause the callee kept for its log, if any.
func (e *Error) Unwrap() error { return e.cause }

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

// CodedCause is [Coded] that keeps the error behind it for the callee's log. The
// cause is not sent: a caller is another module's trust domain.
func CodedCause(code, message string, cause error) error {
	return &Error{Code: code, Message: message, cause: cause}
}

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
type handler struct {
	fn    func(ctx context.Context, payload []byte) (any, error)
	allow map[string]bool // nil: any caller the transport let in
	max   int             // bound of the request payload and of the result
}

// Option configures a method at registration.
type Option func(*handler)

// Allow limits a method to the named caller classes. A call from any other
// class, or from one the transport cannot name, is refused with
// [CodeForbidden] before the method runs.
func Allow(callers ...string) Option {
	return func(h *handler) {
		h.allow = make(map[string]bool, len(callers))
		for _, c := range callers {
			h.allow[c] = true
		}
	}
}

// MaxBytes sets the bound, in bytes, of a method's request payload and of its
// result, in place of [DefaultMaxBytes]. A value over [HardMaxBytes] is a
// programming error and panics at registration. A caller of such a method sets
// the same bound on its transport.
func MaxBytes(n int) Option {
	return func(h *handler) {
		if n <= 0 || n > HardMaxBytes {
			panic("modcall: MaxBytes " + strconv.Itoa(n) + " is outside 1.." + strconv.Itoa(HardMaxBytes))
		}
		h.max = n
	}
}

// Server dispatches calls to the methods a module registered.
type Server struct {
	module string
	log    atomic.Pointer[slog.Logger]

	mu       sync.RWMutex
	handlers map[string]*handler
}

// NewServer is the server of one module.
func NewServer(module string) *Server {
	return &Server{module: module, handlers: map[string]*handler{}}
}

// SetLogger makes the server log what it refuses or fails to answer, with the
// request id [WithRequestID] put on the context: a caller is told a code and
// nothing else, so without this the callee's log says nothing of a failed call.
// A server with no logger logs nothing; SetLogger does not replace one already
// set.
func (s *Server) SetLogger(log *slog.Logger) {
	if log != nil {
		s.log.CompareAndSwap(nil, log)
	}
}

// requestKey carries the transport's request id on a context.
type requestKey struct{}

// WithRequestID marks ctx with the transport's id of the request (a Lambda
// request id), for the callee's log.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestKey{}, id)
}

// report logs a call that was refused or failed. Refusals of the caller's
// request are warnings; a failure of the callee's own is an error. Neither
// carries the payload.
func (s *Server) report(ctx context.Context, req Request, resp Response, cause error) {
	log := s.log.Load()
	if log == nil || resp.Error == nil {
		return
	}
	level := slog.LevelWarn
	switch resp.Error.Code {
	case CodeInternal, CodeDeadlineExceeded:
		level = slog.LevelError
	}
	attrs := []slog.Attr{
		slog.String("module", s.module), slog.String("method", req.Method), slog.String("code", resp.Error.Code),
	}
	if id, _ := ctx.Value(requestKey{}).(string); id != "" {
		attrs = append(attrs, slog.String("request_id", id))
	}
	var coded *Error
	if errors.As(cause, &coded) && coded.cause != nil {
		cause = coded.cause
	}
	if cause != nil {
		attrs = append(attrs, logattr.SafeError("error", cause))
	}
	if resp.Error.Code == CodeForbidden {
		attrs = append(attrs, slog.String("caller", CallerOf(ctx)))
	}
	log.LogAttrs(ctx, level, "a module call was refused or failed", attrs...)
}

// Module is the module the server answers for.
func (s *Server) Module() string { return s.module }

// Handle registers a typed method. A method registered twice is a programming
// error and panics.
func Handle[Req, Resp any](s *Server, method string, fn func(ctx context.Context, req Req) (Resp, error), opts ...Option) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.handlers[method]; dup {
		panic("modcall: " + s.module + "." + method + " is registered twice")
	}
	h := &handler{max: DefaultMaxBytes, fn: func(ctx context.Context, payload []byte) (any, error) {
		var req Req
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &req); err != nil {
				return nil, Coded(CodeBadRequest, "the request is not valid")
			}
		}
		return fn(ctx, req)
	}}
	for _, o := range opts {
		o(h)
	}
	s.handlers[method] = h
}

// Dispatch answers one request. It never returns a Go error: a failure is the
// response's.
//
// It answers version [MinVersion] to [Version] in the request's own version; a
// newer one is refused. The caller's trace continues in a server span, the
// caller's deadline bounds the method's context, and a payload or a result over
// the method's bound is refused without being echoed.
func (s *Server) Dispatch(ctx context.Context, req Request) Response {
	if req.V < 0 || req.V > Version {
		return Response{V: Version, Error: &Error{Code: CodeBadRequest, Message: "unsupported protocol version"}}
	}
	v := req.V
	if v == 0 {
		v = 1
	}
	resp := s.dispatch(ctx, v, req)
	if v > 1 {
		resp.V = v
	}
	return resp
}

func (s *Server) dispatch(ctx context.Context, v int, req Request) Response {
	if req.Module != s.module {
		resp := Response{Error: &Error{Code: CodeNoSuchMethod, Message: "no such module"}}
		s.report(ctx, req, resp, nil)
		return resp
	}
	s.mu.RLock()
	h, ok := s.handlers[req.Method]
	s.mu.RUnlock()
	if !ok {
		resp := Response{Error: &Error{Code: CodeNoSuchMethod, Message: "no such method"}}
		s.report(ctx, req, resp, nil)
		return resp
	}
	if v >= 2 {
		ctx = telemetry.ExtractWire(ctx, req.Traceparent, req.Tracestate)
	}
	ctx, span := telemetry.Tracer().Start(ctx, "modcall "+s.module+"."+req.Method,
		trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(rpcAttrs(s.module, req.Method)...))
	defer span.End()
	resp, cause := s.run(ctx, v, h, req)
	s.report(ctx, req, resp, cause)
	if resp.Error != nil {
		span.SetStatus(codes.Error, "")
		span.SetAttributes(attribute.String("error.type", resp.Error.Code))
	}
	return resp
}

func (s *Server) run(ctx context.Context, v int, h *handler, req Request) (Response, error) {
	if h.allow != nil && !h.allows(ctx) {
		return Response{Error: &Error{Code: CodeForbidden}}, nil
	}
	if len(req.Payload) > h.max {
		return Response{Error: &Error{Code: CodeBadRequest, Message: "the request is too large"}}, nil
	}
	if v >= 2 && req.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(req.Deadline))
		defer cancel()
	}
	if ctx.Err() != nil {
		return Response{Error: &Error{Code: CodeDeadlineExceeded}}, ctx.Err()
	}
	out, err := h.fn(ctx, req.Payload)
	if err != nil {
		var coded *Error
		switch {
		case errors.As(err, &coded):
			return Response{Error: &Error{Code: coded.Code, Message: coded.Message}}, err
		case errors.Is(err, context.DeadlineExceeded):
			return Response{Error: &Error{Code: CodeDeadlineExceeded}}, err
		}
		return Response{Error: &Error{Code: CodeInternal}}, err
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > h.max {
		if err == nil {
			err = fmt.Errorf("the result is %d bytes, over the method's bound of %d", len(raw), h.max)
		}
		return Response{Error: &Error{Code: CodeInternal}}, err
	}
	return Response{Result: raw}, nil
}

// rpcAttrs are the span attributes of a call: the system and the method, both
// on the exporter's allowlist, never the payload.
func rpcAttrs(module, method string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("rpc.system.name", "sluis.modcall"),
		attribute.String("rpc.method", module+"."+method),
	}
}

// Pending is one outgoing call between [Begin] and [Pending.Finish].
type Pending struct {
	span trace.Span
	max  int
}

// Begin prepares an outgoing call for a transport: it refuses a payload over
// maxBytes ([DefaultMaxBytes] when 0) and a context already done, starts the
// client span, and builds the envelope with the trace and the deadline of the
// returned context. Every transport calls it, so that they carry the same
// fields; the caller class is never one of them.
func Begin(ctx context.Context, module, method string, payload []byte, maxBytes int) (context.Context, Request, *Pending, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if len(payload) > maxBytes {
		return ctx, Request{}, nil, &Error{Code: CodeBadRequest, Message: "the request is too large"}
	}
	if ctx.Err() != nil {
		return ctx, Request{}, nil, &Error{Code: CodeDeadlineExceeded}
	}
	ctx, span := telemetry.Tracer().Start(ctx, "modcall "+module+"."+method,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(rpcAttrs(module, method)...))
	req := Request{V: Version, Kind: Kind, Module: module, Method: method, Payload: payload}
	req.Traceparent, req.Tracestate = telemetry.InjectWire(ctx)
	if d, ok := ctx.Deadline(); ok {
		req.Deadline = d.UnixMilli()
	}
	return ctx, req, &Pending{span: span, max: maxBytes}, nil
}

// Oversize reports whether a whole answer, envelope included, is larger than
// any result within the bound could be.
func (p *Pending) Oversize(answer []byte) bool { return len(answer) > p.max+envelopeSlack }

// Finish ends the call: it refuses a result over the bound, with no echo of it,
// and closes the client span.
func (p *Pending) Finish(result []byte, err error) ([]byte, error) {
	if err == nil && len(result) > p.max {
		result, err = nil, &Error{Code: CodeInternal, Message: "the result is too large"}
	}
	if err != nil {
		p.span.SetStatus(codes.Error, "")
		var coded *Error
		if errors.As(err, &coded) {
			p.span.SetAttributes(attribute.String("error.type", coded.Code))
		}
	}
	p.span.End()
	return result, err
}

// Local calls servers in this process, through the same JSON the other
// transports carry, so what works here works across a boundary. Called as
// itself it has no caller class, so a method with [Allow] refuses it; [Local.As]
// names the class the caller is.
type Local map[string]*Server

// Call implements [Caller].
func (l Local) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	return l.As("").Call(ctx, module, method, payload)
}

// As is a Caller whose calls arrive as the caller class.
func (l Local) As(class string) Caller { return localAs{l, class} }

type localAs struct {
	l     Local
	class string
}

func (c localAs) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	s, ok := c.l[module]
	if !ok {
		return nil, &Error{Code: CodeNoSuchMethod, Message: "no such module"}
	}
	ctx, req, p, err := Begin(ctx, module, method, payload, HardMaxBytes)
	if err != nil {
		return nil, err
	}
	return p.Finish(Result(s.Dispatch(WithCaller(ctx, c.class), req)))
}

// Result is a response as a caller sees it.
func Result(r Response) ([]byte, error) {
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}

// allows says whether the caller the transport named is a class the method
// answers: its class, or any of the classes it holds ([WithCallerClasses]).
func (h *handler) allows(ctx context.Context) bool {
	if h.allow[CallerOf(ctx)] {
		return true
	}
	for _, c := range callerClasses(ctx) {
		if h.allow[c] {
			return true
		}
	}
	return false
}

type callerKey struct{}

type callerClassesKey struct{}

// WithCallerClasses records that the caller the transport authenticated holds
// every one of classes. On Kubernetes one ServiceAccount is the issuer's pod,
// which is both the issuer and the console; on Lambda those are two aliases.
// Only a transport calls it. [CallerOf] is the first class.
func WithCallerClasses(ctx context.Context, classes ...string) context.Context {
	if len(classes) == 0 {
		return ctx
	}
	ctx = context.WithValue(ctx, callerClassesKey{}, slices.Clone(classes))
	return WithCaller(ctx, classes[0])
}

func callerClasses(ctx context.Context) []string {
	c, _ := ctx.Value(callerClassesKey{}).([]string)
	return c
}

// WithCaller records the caller class the transport authenticated the call as.
// Only a transport calls it; a request's payload cannot.
func WithCaller(ctx context.Context, class string) context.Context {
	return context.WithValue(ctx, callerKey{}, class)
}

// CallerOf is the caller class the transport authenticated the call as; empty
// for a call the transport cannot name.
func CallerOf(ctx context.Context) string {
	s, _ := ctx.Value(callerKey{}).(string)
	return s
}

type noRetryKey struct{}

// WithoutRetry marks a call that must not be sent twice: a transport that
// retries a failed send on its own (the AWS client does, for a throttled or
// reset invoke) does not, for a call with this context. A method that spends
// something single-use, such as an OAuth code, is called this way, because a
// retry after a lost answer would find the code spent.
func WithoutRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, noRetryKey{}, true)
}

// NoRetry reports whether ctx was marked by [WithoutRetry].
func NoRetry(ctx context.Context) bool {
	v, _ := ctx.Value(noRetryKey{}).(bool)
	return v
}
