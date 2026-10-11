package config

import (
	"errors"
	"fmt"
	"net/url"
)

// The modules a document can route to another process, besides Cloudflare
// (whose `cloudflare.remote` is the same shape).
const (
	moduleCloudflare = "cloudflare"
	moduleGitHub     = "github"
	moduleSlack      = "slack"
	moduleGoogle     = "google"
)

// ModuleRoute is the section of a module that is not its own settings:
// `github`, `slack` and `google` each carry one. It holds where the module is
// when it is not this process.
type ModuleRoute struct {
	// Remote is where the module is when it is not this process. Unset keeps
	// the module in this process.
	Remote *ModuleRemote `json:"remote,omitempty"`
}

// ModuleRemote is a module in another process: a Lambda function or a
// Kubernetes Service (docs/decisions/0071). It has the shape of
// [CloudflareRemote].
type ModuleRemote struct {
	// Function is the module's Lambda function, name or ARN; it is invoked
	// through the `live-<class>` alias of the caller's class.
	Function string `json:"function,omitempty"`
	// URL is the module's Service, called with the pod's projected token.
	URL string `json:"url,omitempty"`
	// Audience is the audience of that token; the module's name when unset.
	Audience string `json:"audience,omitempty"`
	// TokenFile is where the projected token is mounted;
	// /var/run/secrets/sluis/<module>/token when unset.
	TokenFile string `json:"tokenFile,omitempty"`
}

// Validate holds the section to what its schema cannot say. A nil section, or
// one with no remote, keeps the module in this process.
func (r *ModuleRoute) Validate(module string) error {
	if r == nil || r.Remote == nil {
		return nil
	}
	return validateRemote(module+".remote", r.Remote.Function, r.Remote.URL)
}

// validateRemote refuses a remote that names both or neither of a function and
// a URL, and a URL that is not http(s).
func validateRemote(at, function, rawURL string) error {
	switch {
	case (function == "") == (rawURL == ""):
		return fmt.Errorf("%s: set exactly one of function and url", at)
	case rawURL != "":
		if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("%s.url: %q is not an http(s) URL", at, rawURL)
		}
	}
	return nil
}

// Target is the Cloudflare module's remote as a [ModuleRemote].
func (r *CloudflareRemote) Target() ModuleRemote {
	return ModuleRemote{Function: r.Function, URL: r.URL, Audience: r.Audience, TokenFile: r.TokenFile}
}

// remoteTargets are the modules a process reaches elsewhere, by module name.
// internal/modcall/dial.FromTargets turns them into a routing.
func remoteTargets(cf *Cloudflare, routes map[string]*ModuleRoute) map[string]ModuleRemote {
	var out map[string]ModuleRemote
	add := func(name string, r ModuleRemote) {
		if out == nil {
			out = map[string]ModuleRemote{}
		}
		out[name] = r
	}
	if cf != nil && cf.Remote != nil {
		add(moduleCloudflare, cf.Remote.Target())
	}
	for name, route := range routes {
		if route != nil && route.Remote != nil {
			add(name, *route.Remote)
		}
	}
	return out
}

// validateRoutes holds each module's section.
func validateRoutes(routes map[string]*ModuleRoute) error {
	var errs []error
	for _, name := range sortedKeys(routes) {
		errs = append(errs, routes[name].Validate(name))
	}
	return errors.Join(errs...)
}

func (s *Sluis) routes() map[string]*ModuleRoute {
	return map[string]*ModuleRoute{moduleGitHub: s.GitHub, moduleSlack: s.Slack, moduleGoogle: s.Google}
}

func (r *Roster) routes() map[string]*ModuleRoute {
	return map[string]*ModuleRoute{moduleGitHub: r.GitHub, moduleSlack: r.Slack, moduleGoogle: r.Google}
}

// ValidateRoutes holds `github.remote`, `slack.remote` and `google.remote` to
// what the schema cannot say.
func (s *Sluis) ValidateRoutes() error { return validateRoutes(s.routes()) }

// ValidateRoutes is [Sluis.ValidateRoutes] for a controller document.
func (r *Roster) ValidateRoutes() error { return validateRoutes(r.routes()) }

// Targets are the modules this process reaches elsewhere, by name:
// `cloudflare.remote` and the `remote` of `github`, `slack` and `google`.
func (s *Sluis) Targets() map[string]ModuleRemote { return remoteTargets(s.Cloudflare, s.routes()) }

// Targets is the routing a v2 `serve` document says: `cloudflare.remote` alone,
// since the per-module sections came with v3.
func (s *Serve) Targets() map[string]ModuleRemote { return remoteTargets(s.Cloudflare, nil) }

// Targets is the routing a controller document says.
func (r *Roster) Targets() map[string]ModuleRemote { return remoteTargets(nil, r.routes()) }
