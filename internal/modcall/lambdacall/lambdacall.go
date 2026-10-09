// Package lambdacall is the Lambda transport of internal/modcall: a call is a
// synchronous lambda:InvokeFunction of the callee's `live` alias with the JSON
// envelope as payload, and a function answers one with [Serve]. It is a package
// of its own so that only a Lambda build imports the Lambda client.
package lambdacall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/truvity/sluis/internal/modcall"
)

// Alias is the alias every module call goes through: the callee's resource
// policy names its callers per alias.
const Alias = "live"

// API is the part of the Lambda client the transport uses.
type API interface {
	Invoke(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}

// Caller invokes module functions by name.
type Caller struct {
	API API
	// Functions maps a module to its function's name or ARN.
	Functions map[string]string
}

// New is the Caller for the functions a configuration names.
func New(api API, cfg modcall.Config) *Caller {
	c := &Caller{API: api, Functions: map[string]string{}}
	for name, t := range cfg.Modules {
		if t.Function != "" {
			c.Functions[name] = t.Function
		}
	}
	return c
}

// Call implements [modcall.Caller].
func (c *Caller) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	fn, ok := c.Functions[module]
	if !ok {
		return nil, fmt.Errorf("%w: no function is configured for module %q", modcall.ErrNoRoute, module)
	}
	body, err := json.Marshal(modcall.Request{Kind: modcall.Kind, Module: module, Method: method, Payload: payload})
	if err != nil {
		return nil, err
	}
	out, err := c.API.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName: aws.String(fn), Qualifier: aws.String(Alias),
		InvocationType: types.InvocationTypeRequestResponse, Payload: body,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: invoke %s: %w", modcall.ErrTransport, fn, err)
	}
	// A function that crashed or timed out answers 200 with FunctionError set.
	if out.FunctionError != nil {
		return nil, fmt.Errorf("%w: %s failed: %s", modcall.ErrTransport, fn, aws.ToString(out.FunctionError))
	}
	var res modcall.Response
	if err = json.Unmarshal(out.Payload, &res); err != nil {
		return nil, fmt.Errorf("%w: %s answered a payload that is not valid", modcall.ErrTransport, fn)
	}
	return modcall.Result(res)
}

// ErrNotACall is an event that is not a module call (a scheduler tick, say).
var ErrNotACall = errors.New("lambdacall: the event is not a module call")

// Serve answers a Lambda event that carries a module call, with the response's
// JSON. The right to invoke is IAM's, so there is no bearer; the alias the
// function was invoked through is the caller class and is passed as subject.
func Serve(ctx context.Context, s *modcall.Server, event []byte, subject string) ([]byte, error) {
	var req modcall.Request
	if err := json.Unmarshal(event, &req); err != nil || req.Kind != modcall.Kind {
		return nil, ErrNotACall
	}
	return json.Marshal(s.Dispatch(modcall.WithCaller(ctx, subject), req))
}
