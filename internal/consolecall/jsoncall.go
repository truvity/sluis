package consolecall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/truvity/sluis/internal/modcall"
)

// A method whose request or answer is not one of the console's proto messages
// (a callback's code and what became of it) is carried the same way, as plain
// JSON in the same envelope and under the same rules: the console class alone,
// a [Problem] as data, any other error as the transport's.

// HandleJSON registers a console method over Go values. It answers [Class] and
// no other, as [Handle] does.
func HandleJSON[Req, Resp any](
	s *modcall.Server, method string, fn func(ctx context.Context, caller Caller, req Req) (Resp, error), opts ...modcall.Option,
) {
	if !strings.HasPrefix(method, MethodPrefix) {
		panic("consolecall: " + s.Module() + "." + method + " is a console method and must start with " + MethodPrefix)
	}
	opts = append(slices.Clone(opts), modcall.Allow(Class))
	modcall.Handle(s, method, func(ctx context.Context, env Envelope) (Reply, error) {
		var req Req
		if len(env.Msg) > 0 {
			if err := json.Unmarshal(env.Msg, &req); err != nil {
				return Reply{}, modcall.Coded(modcall.CodeBadRequest, "the request is not valid")
			}
		}
		resp, err := fn(ctx, env.Caller, req)
		var p *Problem
		if errors.As(err, &p) {
			return Reply{Problem: p}, nil
		}
		if err != nil {
			return Reply{}, err
		}
		raw, err := json.Marshal(resp)
		if err != nil {
			return Reply{}, modcall.Coded(modcall.CodeInternal, "")
		}
		return Reply{Msg: raw}, nil
	}, opts...)
}

// ProxyJSON calls a console method registered by [HandleJSON].
func ProxyJSON[Req, Resp any](ctx context.Context, c modcall.Caller, module, method string, caller Caller, req Req) (Resp, error) {
	var zero Resp
	raw, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("console call %s.%s: the request cannot be encoded: %w", module, method, err)
	}
	reply, err := modcall.Do[Envelope, Reply](ctx, c, module, method, Envelope{Caller: caller, Msg: raw})
	if err != nil {
		return zero, err
	}
	if reply.Problem != nil {
		return zero, reply.Problem
	}
	var out Resp
	if len(reply.Msg) > 0 {
		if err := json.Unmarshal(reply.Msg, &out); err != nil {
			return zero, fmt.Errorf("console call %s.%s: the response is not valid: %w", module, method, err)
		}
	}
	return out, nil
}
