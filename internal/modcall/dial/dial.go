// Package dial is how a process turns the routing its document names into a
// [modcall.Caller]: [Dial] builds the [modcall.Router] for a [modcall.Config],
// with the Lambda transport only in a build with the `lambda` tag, so that the
// Kubernetes build carries no Lambda client.
//
// It is a package of its own, beside internal/modcall, because the Lambda
// transport (internal/modcall/lambdacall) imports modcall.
package dial

import (
	"context"
	"fmt"
	"sort"

	"github.com/truvity/sluis/internal/consoleauth"
	"github.com/truvity/sluis/internal/modcall"
)

// Dial is the Caller for the modules cfg names, calling as the caller class: on
// Lambda each function's `live-<class>` alias, on Kubernetes the pod's
// projected token. A module cfg does not name, or names with neither a function
// nor a URL, is local: the Router's local Caller answers it, and with none
// (nil) a call to it fails with [modcall.ErrNoRoute]. Both a function and a
// URL on one module is refused, and so is a function in a build with no Lambda
// transport.
func Dial(ctx context.Context, cfg modcall.Config, class string, local modcall.Caller) (*modcall.Router, error) {
	var lambda, http modcall.Caller
	var err error
	if hasFunction(cfg) {
		if lambda, err = lambdaCaller(ctx, cfg, class); err != nil {
			return nil, err
		}
	}
	if hasURL(cfg) {
		http = httpCaller(cfg)
	}
	return modcall.NewRouter(cfg, local, lambda, http)
}

func hasFunction(cfg modcall.Config) bool {
	for _, t := range cfg.Modules {
		if t.Function != "" {
			return true
		}
	}
	return false
}

func hasURL(cfg modcall.Config) bool {
	for _, t := range cfg.Modules {
		if t.URL != "" {
			return true
		}
	}
	return false
}

// httpCaller sends each URL module's calls with its own token file: a module
// with its own audience has its own mount, so the file is chosen by module.
func httpCaller(cfg modcall.Config) modcall.Caller {
	by := byModule{}
	for name, t := range cfg.Modules {
		if t.URL == "" {
			continue
		}
		file := t.TokenFile
		if file == "" {
			file = modcall.DefaultTokenFile(name)
		}
		src := consoleauth.File(file)
		by[name] = &modcall.HTTPCaller{
			URLs:      map[string]string{name: t.URL},
			Audiences: map[string]string{name: t.Audience},
			Token:     func(ctx context.Context, _ string) (string, error) { return src.Token(ctx) },
		}
	}
	return by
}

type byModule map[string]*modcall.HTTPCaller

func (b byModule) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	c, ok := b[module]
	if !ok {
		return nil, fmt.Errorf("%w: no Service is configured for module %q", modcall.ErrNoRoute, module)
	}
	return c.Call(ctx, module, method, payload)
}

// Functions is the sorted names of the modules cfg reaches by Lambda function.
func Functions(cfg modcall.Config) []string {
	var out []string
	for name, t := range cfg.Modules {
		if t.Function != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
