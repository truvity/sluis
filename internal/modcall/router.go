package modcall

import (
	"context"
	"fmt"
)

// Target is where one module is reached when it is not in this process. Set
// Function for a Lambda function (its `live` alias), URL for a Kubernetes
// Service. Neither: the module is in this process.
type Target struct {
	Function string `json:"function,omitempty"`
	URL      string `json:"url,omitempty"`
	Audience string `json:"audience,omitempty"`
}

// Config says how each module is reached.
type Config struct {
	Modules map[string]Target `json:"modules,omitempty"`
}

// Remote reports whether the module has a target of its own.
func (c Config) Remote(module string) bool {
	t := c.Modules[module]
	return t.Function != "" || t.URL != ""
}

// Router sends each call by the configuration: to the server in this process
// unless the module has a Function or a URL.
type Router struct {
	local  Caller
	lambda Caller
	http   Caller
	cfg    Config
}

// NewRouter builds the Caller. lambda and http may be nil when the
// configuration names no such target; a target whose transport is missing is
// an error here, not at the first call.
func NewRouter(cfg Config, local Caller, lambda, http Caller) (*Router, error) {
	for name, t := range cfg.Modules {
		switch {
		case t.Function != "" && t.URL != "":
			return nil, fmt.Errorf("modcall: module %q names both a function and a url", name)
		case t.Function != "" && lambda == nil:
			return nil, fmt.Errorf("modcall: module %q is a Lambda function, and this build has no Lambda transport", name)
		case t.URL != "" && http == nil:
			return nil, fmt.Errorf("modcall: module %q is a Service, and no token source is configured", name)
		}
	}
	return &Router{local: local, lambda: lambda, http: http, cfg: cfg}, nil
}

// Call implements [Caller].
func (r *Router) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	t := r.cfg.Modules[module]
	switch {
	case t.Function != "":
		return r.lambda.Call(ctx, module, method, payload)
	case t.URL != "":
		return r.http.Call(ctx, module, method, payload)
	}
	if r.local == nil {
		return nil, fmt.Errorf("%w: module %q is not in this process", ErrNoRoute, module)
	}
	return r.local.Call(ctx, module, method, payload)
}
