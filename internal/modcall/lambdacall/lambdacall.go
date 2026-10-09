// Package lambdacall is the Lambda transport of internal/modcall: a call is a
// synchronous lambda:InvokeFunction of the callee's `live-<class>` alias for the caller class with the JSON
// envelope as payload, and a function answers one with [Serve]. It is a package
// of its own so that only a Lambda build imports the Lambda client.
package lambdacall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/truvity/sluis/internal/modcall"
)

// Alias is the alias of a call from a caller with no class; a deployment before
// per-class aliases has only this one.
const Alias = "live"

// AliasOf is the alias a caller of the class invokes: `live-<class>`. The
// callee's resource policy names its callers per alias, so the alias is the
// caller's identity and IAM, not the payload, enforces it.
func AliasOf(class string) string {
	if class == "" {
		return Alias
	}
	return Alias + "-" + class
}

// ClassOf is the caller class of an invoked alias: the inverse of [AliasOf].
// Anything else, a version number or `$LATEST` included, is no class.
func ClassOf(alias string) string {
	class, _ := strings.CutPrefix(alias, Alias+"-")
	if class == alias {
		return ""
	}
	return class
}

// API is the part of the Lambda client the transport uses.
type API interface {
	Invoke(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}

// Caller invokes module functions by name.
type Caller struct {
	API API
	// Functions maps a module to its function's name or ARN.
	Functions map[string]string
	// Class is the caller class the calls arrive as: the alias is `live-<Class>`.
	Class string
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

// As is the Caller for calls arriving as the caller class.
func (c *Caller) As(class string) *Caller {
	cp := *c
	cp.Class = class
	return &cp
}

// Call implements [modcall.Caller].
func (c *Caller) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	fn, ok := c.Functions[module]
	if !ok {
		return nil, fmt.Errorf("%w: no function is configured for module %q", modcall.ErrNoRoute, module)
	}
	body, err := json.Marshal(modcall.Request{V: modcall.Version, Kind: modcall.Kind, Module: module, Method: method, Payload: payload})
	if err != nil {
		return nil, err
	}
	out, err := c.API.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName: aws.String(fn), Qualifier: aws.String(AliasOf(c.Class)),
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
// JSON. The right to invoke is IAM's, so there is no bearer; the caller class is
// the one the alias the function was invoked through names ([ClassOf]), and the
// event cannot say otherwise.
func Serve(ctx context.Context, s *modcall.Server, event []byte, alias string) ([]byte, error) {
	var req modcall.Request
	if err := json.Unmarshal(event, &req); err != nil || req.Kind != modcall.Kind {
		return nil, ErrNotACall
	}
	return json.Marshal(s.Dispatch(modcall.WithCaller(ctx, ClassOf(alias)), req))
}
