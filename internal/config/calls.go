package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/truvity/sluis/internal/modcall"
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

// ModCall is the routing the document says, for [modcall.Dial]. cf is the
// deprecated `cloudflare.remote`, which is read as `calls.modules.cloudflare`;
// naming the module in both is refused, since two places would disagree.
func (c *Calls) ModCall(cf *Cloudflare) (modcall.Config, error) {
	out := modcall.Config{}
	if c != nil {
		out.Modules = make(map[string]modcall.Target, len(c.Modules)+1)
		for name, t := range c.Modules {
			out.Modules[name] = modcall.Target{Function: t.Function, URL: t.URL, Audience: t.Audience, TokenFile: t.TokenFile}
		}
	}
	if cf != nil && cf.Remote != nil {
		if _, dup := out.Modules[cloudflareModule]; dup {
			return modcall.Config{}, errors.New("cloudflare.remote and calls.modules.cloudflare both name the Cloudflare module: drop the deprecated cloudflare.remote")
		}
		if out.Modules == nil {
			out.Modules = map[string]modcall.Target{}
		}
		r := cf.Remote
		out.Modules[cloudflareModule] = modcall.Target{Function: r.Function, URL: r.URL, Audience: r.Audience, TokenFile: r.TokenFile}
	}
	return out, nil
}

// ValidateCalls holds `calls` to what the schema cannot say, and refuses the
// Cloudflare module named by both `calls` and the deprecated `cloudflare.remote`.
func (s *Sluis) ValidateCalls() error {
	if err := s.Calls.Validate(); err != nil {
		return err
	}
	_, err := s.ModCall()
	return err
}

// ModCall is the routing the document says: `calls`, plus the deprecated
// `cloudflare.remote` as the Cloudflare module's entry.
func (s *Sluis) ModCall() (modcall.Config, error) { return s.Calls.ModCall(s.Cloudflare) }

// ModCall is the routing a v2 `serve` document says: `cloudflare.remote` alone,
// since the section `calls` came with v3.
func (s *Serve) ModCall() (modcall.Config, error) { return (*Calls)(nil).ModCall(s.Cloudflare) }

// ModCall is the routing a controller document says.
func (r *Roster) ModCall() (modcall.Config, error) { return r.Calls.ModCall(nil) }
