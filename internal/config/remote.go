package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
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
	// Serve makes the module answer other modules' calls on Kubernetes: a
	// listener that verifies the caller's projected ServiceAccount token. Only
	// the module's own document sets it (`github.serve` for the GitHub
	// module, `slack.serve` for the Slack one).
	Serve *ModuleServe `json:"serve,omitempty"`
}

// ModuleServe is where the GitHub or Slack module answers the issuer on
// Kubernetes (docs/decisions/0071): `POST /rpc` with a bearer that a
// TokenReview accepts for Audience and that belongs to one of Issuers. Lambda
// ignores it: the function answers `rpc` events and IAM decides who invokes it.
type ModuleServe struct {
	// Address is the listener: host:port. :8081 when unset.
	Address string `json:"address,omitempty"`
	// Audience is the audience a caller's projected token must have been minted
	// for; the module's name when unset.
	Audience string `json:"audience,omitempty"`
	// Issuers are the subjects of the issuer's ServiceAccount
	// (`system:serviceaccount:<namespace>:<name>`). Each is both the issuer
	// class (`mint_installation`) and the console class (`*.console.*`); a
	// token of any other subject is a 401. At least one is required.
	Issuers []string `json:"issuers,omitempty"`
}

// Validate holds the listener to what its schema cannot say.
func (s *ModuleServe) Validate(at string) error {
	if s == nil {
		return nil
	}
	if len(s.Issuers) == 0 {
		return fmt.Errorf("%s.issuers: name the issuer's ServiceAccount (system:serviceaccount:<namespace>:<name>); without it no caller could be let in", at)
	}
	for _, sub := range s.Issuers {
		if rest, ok := strings.CutPrefix(sub, "system:serviceaccount:"); !ok || strings.Count(rest, ":") != 1 || strings.HasPrefix(rest, ":") || strings.HasSuffix(rest, ":") {
			return fmt.Errorf("%s.issuers: %q is not system:serviceaccount:<namespace>:<name>", at, sub)
		}
	}
	return nil
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
	if r == nil {
		return nil
	}
	if r.Remote != nil && r.Serve != nil {
		return fmt.Errorf("%s: remote and serve are both set; a module that is somewhere else does not answer here", module)
	}
	if err := r.Serve.Validate(module + ".serve"); err != nil {
		return err
	}
	if r.Remote == nil {
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
func (s *Sluis) ValidateRoutes() error {
	errs := []error{validateRoutes(s.routes())}
	for _, name := range sortedKeys(s.routes()) {
		if r := s.routes()[name]; r != nil && r.Serve != nil {
			errs = append(errs, fmt.Errorf("%s.serve: the issuer's document does not serve a module; it belongs in the %s module's own document", name, name))
		}
	}
	return errors.Join(errs...)
}

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
