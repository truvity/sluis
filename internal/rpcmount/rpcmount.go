// Package rpcmount is what this module's Connect services need from
// connect-go v2 to behave as they did on v1.
//
// connect-go v2 registers services on a [connect.Server] and mounts every
// procedure on a mux with [connecthttp.Mount]. The handlers here are mounted
// by path, wrapped and composed as one http.Handler per service, so Handler
// mounts each server on a mux of its own and returns that mux, the shape a v1
// generated constructor returned. NewServer adds [V1Errors], which keeps the
// code and message a client sees what v1 sent.
package rpcmount

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
)

// Handler returns the path prefix of the one service registered on server,
// "/package.Service/", and an http.Handler that serves every procedure of it.
// It panics when server holds no service or more than one, which is a wiring
// mistake in the caller, not a request.
func Handler(server *connect.Server, opts ...connecthttp.Option) (string, http.Handler) {
	path := ""
	for spec := range server.Specs() {
		i := strings.LastIndexByte(spec.Procedure, '/')
		prefix := spec.Procedure[:i+1]
		switch {
		case path == "":
			path = prefix
		case path != prefix:
			panic("rpcmount: one service per handler, got " + path + " and " + prefix)
		}
	}
	if path == "" {
		panic("rpcmount: no service registered")
	}
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server, opts...)
	return path, mux
}

// HTTPHeader is h as an http.Header, for code that reads headers the way
// net/http hands them over. In connect-go v2 a call's headers are a
// [connect.Header], which holds every header of the request. The values
// alias h's.
func HTTPHeader(h *connect.Header) http.Header {
	return http.Header(maps.Collect(h.All()))
}

// NewServer is connect.NewServer with [V1Errors] outermost, so the other
// interceptors see what the handler returned, as they did on v1, and the
// client gets what v1 sent.
func NewServer(interceptors ...connect.ServerInterceptor) *connect.Server {
	return connect.NewServer(append([]connect.ServerInterceptor{V1Errors()}, interceptors...)...)
}

// V1Errors keeps what a client is told about a failure what connect-go v1
// told it. v2 sends only a locally made *connect.Error as it is: any other
// error goes out as a bare code with no message, and an error the handler
// received from another service as CodeInternal. v1 sent the error's text
// with CodeUnknown (CodeCanceled or CodeDeadlineExceeded for a context
// error), and another service's error with its own code and message. The
// original stays attached as the cause, so errors.Is and errors.As still see
// it on this side.
func V1Errors() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			return v1Error(next(ctx, spec, stream))
		}
	}
}

func v1Error(err error) error {
	if err == nil {
		return nil
	}
	if coded, ok := errors.AsType[*connect.Error](err); ok && coded != nil {
		if !coded.IsRemote() {
			return err
		}
		local := connect.NewError(coded.Code(), coded.Message()).WithCause(err)
		for _, detail := range coded.Details() {
			local = local.WithDetail(detail)
		}
		return local
	}
	code := connect.CodeUnknown
	switch {
	case errors.Is(err, context.Canceled):
		code = connect.CodeCanceled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		code = connect.CodeDeadlineExceeded
	}
	return connect.NewError(code, err.Error()).WithCause(err)
}

// ServerContext is ctx as a Connect server hands it to a handler: carrying
// a server-side [connect.CallInfo], which is returned. A handler reads the
// call's request headers from it and writes its response headers to it.
// This is for calling a handler as a plain method, which a test does: the
// headers it sets on info before the call are the request's, and the ones
// the handler set are on info afterwards.
func ServerContext(ctx context.Context) (context.Context, *connect.CallInfo) {
	var inside context.Context
	rpc := connect.NewServer()
	rpc.SetUnknownHandler(func(ctx context.Context, _ connect.Spec, _ connect.ServerStream) error {
		inside = ctx
		return nil
	})
	info := &connect.CallInfo{}
	_ = rpc.Call(ctx, "/rpcmount.v1.Context/Call", info, nil)
	return inside, info
}

// CallInfo is the [connect.CallInfo] of the call ctx belongs to. A handler
// called as a plain method, outside a Connect server (another part of this
// process asking it, or a test), has none: it gets an empty one, so the
// handler reads no request headers and the response headers it writes go
// nowhere, as they would have on a v1 request built without them.
func CallInfo(ctx context.Context) *connect.CallInfo {
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		return info
	}
	return &connect.CallInfo{}
}

// NewError is connect-go v1's connect.NewError: err's text is what the
// client is told, and err stays the cause, so errors.Is and errors.As see
// through the *connect.Error as they did on v1. v2's connect.NewError takes
// the message alone.
func NewError(code connect.Code, err error) *connect.Error {
	return connect.NewError(code, err.Error()).WithCause(err)
}
