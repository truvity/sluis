package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
)

// cloudflareModule is the module name `cloudflare.remote` stands for.
const cloudflareModule = "cloudflare"

// callModuleName is what a module is called in `calls.modules`.
var callModuleName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// Calls is how a process reaches the modules that are not in it. A module with
// no entry, or an entry with neither a function nor a URL, is called in the
// same process.
type Calls struct {
	// Modules maps a module name to where it is.
	Modules map[string]CallTarget `json:"modules,omitempty"`
}

// CallTarget is one module in another process: a Lambda function or a
// Kubernetes Service.
type CallTarget struct {
	// Function is the module's Lambda function, name or ARN; it is invoked
	// through the `live-<class>` alias of the caller's class.
	Function string `json:"function,omitempty"`
	// URL is the module's Service, called with the pod's projected token.
	URL string `json:"url,omitempty"`
	// Audience is the audience of that token; the module's name when unset.
	Audience string `json:"audience,omitempty"`
	// TokenFile is where the projected token is mounted.
	TokenFile string `json:"tokenFile,omitempty"`
}

// Validate holds the section to what its schema cannot say. A nil section is
// valid: every module is in this process.
func (c *Calls) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	for _, name := range sortedKeys(c.Modules) {
		t := c.Modules[name]
		at := "calls.modules." + name
		if !callModuleName.MatchString(name) {
			errs = append(errs, fmt.Errorf("%s: %q is not a module name", at, name))
		}
		if t.Function != "" && t.URL != "" {
			errs = append(errs, fmt.Errorf("%s: set at most one of function and url", at))
		}
		if t.URL != "" {
			if u, err := url.Parse(t.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				errs = append(errs, fmt.Errorf("%s.url: %q is not an http(s) URL", at, t.URL))
			}
		}
	}
	return errors.Join(errs...)
}

// Targets are the modules this process reaches elsewhere, by name: `calls`
// plus the deprecated `cf.Remote`, read as the Cloudflare module's entry.
// Naming the Cloudflare module in both is refused, since two places would
// disagree. internal/modcall/dial.FromConfig turns them into a routing.
func (c *Calls) Targets(cf *Cloudflare) (map[string]CallTarget, error) {
	var out map[string]CallTarget
	if c != nil {
		out = make(map[string]CallTarget, len(c.Modules)+1)
		for name, t := range c.Modules {
			out[name] = t
		}
	}
	if cf != nil && cf.Remote != nil {
		if _, dup := out[cloudflareModule]; dup {
			return nil, errors.New("cloudflare.remote and calls.modules.cloudflare both name the Cloudflare module: drop the deprecated cloudflare.remote")
		}
		if out == nil {
			out = map[string]CallTarget{}
		}
		out[cloudflareModule] = cf.Remote.Target()
	}
	return out, nil
}

// Target is the module's entry in `calls.modules` that says the same.
func (r *CloudflareRemote) Target() CallTarget {
	return CallTarget{Function: r.Function, URL: r.URL, Audience: r.Audience, TokenFile: r.TokenFile}
}

// ValidateCalls holds `calls` to what the schema cannot say, and refuses the
// Cloudflare module named by both `calls` and the deprecated `cloudflare.remote`.
func (s *Sluis) ValidateCalls() error {
	if err := s.Calls.Validate(); err != nil {
		return err
	}
	_, err := s.Targets()
	return err
}

// Targets is [Calls.Targets] for the document: `calls`, plus the deprecated
// `cloudflare.remote` as the Cloudflare module's entry.
func (s *Sluis) Targets() (map[string]CallTarget, error) { return s.Calls.Targets(s.Cloudflare) }

// Targets is the routing a v2 `serve` document says: `cloudflare.remote` alone,
// since the section `calls` came with v3.
func (s *Serve) Targets() (map[string]CallTarget, error) { return (*Calls)(nil).Targets(s.Cloudflare) }

// Targets is the routing a controller document says.
func (r *Roster) Targets() (map[string]CallTarget, error) { return r.Calls.Targets(nil) }
