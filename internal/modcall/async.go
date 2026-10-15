package modcall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// AsyncCaller is a [Caller] that can start a call and return before the callee
// has finished: a method that runs for minutes (a backup) is started by the
// issuer for a person who asked, and read back by its record. The callee answers
// nobody; what it did is in its own records.
//
// A Lambda call is an asynchronous invocation, accepted when the service queues
// it. A call to a Service, or in this process, runs in the background of the
// caller, which lives on.
type AsyncCaller interface {
	CallAsync(ctx context.Context, module, method string, payload []byte) error
}

// ErrNoAsync is a [Start] over a transport that cannot return early.
var ErrNoAsync = errors.New("module call: this transport cannot start a call and return")

// asyncBackground bounds a call run in the caller's background.
const asyncBackground = 16 * time.Minute

// Start is a typed call that returns once the callee has been handed the call.
func Start[Req any](ctx context.Context, c Caller, module, method string, req Req) error {
	a, ok := c.(AsyncCaller)
	if !ok {
		return fmt.Errorf("%w: %s.%s", ErrNoAsync, module, method)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("module call %s.%s: %w", module, method, err)
	}
	return a.CallAsync(ctx, module, method, payload)
}

// background runs call in the caller's background, detached from the request
// that asked: it keeps the request's values (the trace) and not its end.
func background(ctx context.Context, call func(ctx context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return &Error{Code: CodeDeadlineExceeded}
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncBackground)
	go func() {
		defer cancel()
		_ = call(bg)
	}()
	return nil
}

// CallAsync implements [AsyncCaller] for the calls of one class in this process.
func (c localAs) CallAsync(ctx context.Context, module, method string, payload []byte) error {
	if _, ok := c.l[module]; !ok {
		return &Error{Code: CodeNoSuchMethod, Message: "no such module"}
	}
	return background(ctx, func(ctx context.Context) error {
		_, err := c.Call(ctx, module, method, payload)
		return err
	})
}

// CallAsync implements [AsyncCaller].
func (c *HTTPCaller) CallAsync(ctx context.Context, module, method string, payload []byte) error {
	if _, ok := c.URLs[module]; !ok {
		return fmt.Errorf("%w: no Service is configured for module %q", ErrNoRoute, module)
	}
	background := *c
	background.Client = &http.Client{Timeout: asyncBackground}
	return backgroundCall(ctx, &background, module, method, payload)
}

func backgroundCall(ctx context.Context, c Caller, module, method string, payload []byte) error {
	return background(ctx, func(ctx context.Context) error {
		_, err := c.Call(ctx, module, method, payload)
		return err
	})
}

// CallAsync implements [AsyncCaller]: to the transport the module is reached by.
func (r *Router) CallAsync(ctx context.Context, module, method string, payload []byte) error {
	t := r.cfg.Modules[module]
	var next Caller
	switch {
	case t.Function != "":
		next = r.lambda
	case t.URL != "":
		next = r.http
	default:
		next = r.local
	}
	if next == nil {
		return fmt.Errorf("%w: module %q is not in this process", ErrNoRoute, module)
	}
	a, ok := next.(AsyncCaller)
	if !ok {
		return fmt.Errorf("%w: %s.%s", ErrNoAsync, module, method)
	}
	return a.CallAsync(ctx, module, method, payload)
}
