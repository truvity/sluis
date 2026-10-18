package compile

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/truvity/sluis/policy"
)

// A relying party is declared as ONE row, and every URL the issuer has to be
// told is derived from it: the hostname a browser reaches, the redirect
// registered with the issuer and the sign-in proxy's own prefix all come out
// of one pair of values, so there is nothing left to keep in step by hand.
//
// The drift this prevents looks like success each time: a client's
// redirects pointing at the old host after the console moved; a sign-out
// whose post_logout_redirect_uri matched no signed-out entry, so the issuer
// ended the session and landed the person on its own page; a proxy's prefix
// and the registered callback changed in step, by hand, in two files.
//
// A proxied console and an application running its own OIDC flow are the
// same row with and without a proxy. They differ in who runs the flow, not
// in what the issuer has to be told.

const (
	// ProxyCallback is the path the sign-in returns to, under the proxy's
	// prefix. Every one of these is registered at the issuer, so it does not
	// move.
	ProxyCallback = "/callback"
	// ProxySuffix is the prefix the sign-in owns, relative to the
	// application's own mount point: appended to the mount rather than to
	// the host, because at the root of a host the issuer owns `/oauth2` is
	// the issuer's.
	ProxySuffix = "/oauth2"
)

// Endpoints is what a relying party's URLs are derived from.
type Endpoints struct {
	// Hostname a browser reaches the application at.
	Hostname string
	// Prefix is where the application is mounted on that host, with a
	// leading and no trailing slash ("/console"). Empty is the host root.
	Prefix string
	// Redirects override the derived callback. Paths are host-relative and
	// joined to Hostname; an absolute URL passes through.
	Redirects []string
	// SignedOut overrides where a person lands once their session is ended.
	SignedOut []string
	// BackChannelLogout is where a logout token is POSTed, host-relative.
	BackChannelLogout string
	// Proxied: a sign-in proxy in front of the application runs the flow.
	Proxied bool
	// SignInPrefix overrides the proxy's derived prefix, for a host where
	// the derived one belongs to someone else. Proxied rows only.
	SignInPrefix string
	// ServedByIssuer marks the console the issuer itself serves.
	ServedByIssuer bool
	// Loopback is a native client listening on 127.0.0.1 on whatever port it
	// was given.
	Loopback bool
	// Exchange is an audience only: nobody signs in to it.
	Exchange bool
}

// Origin is the scheme and host every URL of this client is built on.
func (e Endpoints) Origin() string {
	return "https://" + e.Hostname
}

// Mount is where the application lives on its host, always with a trailing
// slash: "/console/" or "/". It is the sign-out landing page and the base
// every derived path hangs off.
func (e Endpoints) Mount() string {
	if e.Prefix == "" {
		return "/"
	}

	return e.Prefix + "/"
}

// ProxyPrefix is the prefix the proxy owns: sign-in, callback and sign-out
// live under it. Derived, never declared: it is one half of the pair that
// drifted.
func (e Endpoints) ProxyPrefix() string {
	if e.Proxied && e.SignInPrefix != "" {
		return e.SignInPrefix
	}

	return e.Prefix + ProxySuffix
}

// RedirectURIs are the callbacks registered with the issuer.
//
// For a proxied console it is derived from the same prefix the proxy is
// configured with: the URI a browser is sent to and the URI the issuer will
// accept are one expression. The console the issuer serves is sent back to
// its own front page, the mount: it wants the session the flow establishes,
// not the code. Anything else runs its own flow and chooses its own
// callback, and nil says so.
func (e Endpoints) RedirectURIs() []string {
	if len(e.Redirects) > 0 {
		return e.absolute(e.Redirects)
	}

	if e.Proxied {
		return []string{e.Origin() + e.ProxyPrefix() + ProxyCallback}
	}

	if e.ServedByIssuer {
		return []string{e.Origin() + e.Mount()}
	}

	return nil
}

// SignedOutURIs are the post-logout landing pages the issuer will accept.
//
// A CLI or an exchange audience has nowhere to be put down. Nor does the
// console the issuer serves: its front page bounces an unauthenticated
// browser to /authorize, so landing there starts the sign-in just ended;
// with none registered the issuer shows its own signed-out page.
func (e Endpoints) SignedOutURIs() []string {
	if len(e.SignedOut) > 0 {
		return e.absolute(e.SignedOut)
	}

	if e.Loopback || e.Exchange || e.ServedByIssuer {
		return nil
	}

	return []string{e.Origin() + e.Mount()}
}

// BackChannelLogoutURI is where a logout token is POSTed for this client, or
// empty for one that asked for none.
func (e Endpoints) BackChannelLogoutURI() string {
	if e.BackChannelLogout == "" {
		return ""
	}

	return e.absolute([]string{e.BackChannelLogout})[0]
}

// absolute joins host-relative paths onto the origin and passes absolute
// URLs through.
func (e Endpoints) absolute(paths []string) []string {
	out := make([]string, 0, len(paths))

	for _, path := range paths {
		if strings.Contains(path, "://") {
			out = append(out, path)

			continue
		}

		out = append(out, e.Origin()+path)
	}

	return out
}

// ValidateURLs refuses a redirect, landing page or logout endpoint on
// another host. The host has one source in the row; an absolute URL
// elsewhere would be a second one, and a second source is the drift. A
// loopback address is the one exception, for a loopback client.
//
// It refuses `http://` for a redirect or a landing page that is not on a
// loopback address too (RFC 9700 4.1, RFC 8252 7.3): the authorization code
// and the state ride in that URL, and over plain HTTP anyone on the path
// reads them. The issuer's library allows `http://` for a confidential
// client's code flow, so this is where it is stopped.
//
// A back-channel logout endpoint is not a browser redirect and is left as it
// is: it may be `http://`, because the service that receives a logout token is
// often inside the cluster, where the policy may declare it by its plain
// address (the issuer's own post refuses loopback and link-local targets and
// follows no redirect). The token is a signed JWT with a two-minute life, not
// a credential for the endpoint. Like the others it must be on this client's
// host.
func (e Endpoints) ValidateURLs() error {
	every := append(append([]string(nil), e.Redirects...), e.SignedOut...)
	browsers := len(every)

	if e.BackChannelLogout != "" {
		every = append(every, e.BackChannelLogout)
	}

	for i, raw := range every {
		if !strings.Contains(raw, "://") {
			if !strings.HasPrefix(raw, "/") {
				return fmt.Errorf("%q must be host-relative (start with /) or absolute", raw)
			}

			continue
		}

		parsed, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("%q is not a URL: %w", raw, err)
		}

		if isLoopbackHost(parsed.Hostname()) {
			if !e.Loopback {
				return fmt.Errorf("%q is a loopback redirect — set `loopback: true` to say that is what this client is", raw)
			}

			continue
		}

		if i < browsers && !strings.EqualFold(parsed.Scheme, "https") {
			return fmt.Errorf("%q is not https — a redirect or landing page that is not on a loopback address "+
				"must be https, because the authorization code travels in it", raw)
		}

		if parsed.Host != e.Hostname {
			return fmt.Errorf("%q is on %q, not this client's host %q — one row names one host", raw, parsed.Host, e.Hostname)
		}
	}

	return nil
}

// isLoopbackHost says a host name is the machine itself: what a native
// client listens on (RFC 8252 7.3).
func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// ValidateLanding refuses an address that is both a redirect and a
// signed-out landing page: landing on a redirect URI starts the sign-in the
// person just ended, and the issuer refuses the client at load.
func (e Endpoints) ValidateLanding() error {
	redirects := e.RedirectURIs()

	for _, out := range e.SignedOutURIs() {
		if slices.Contains(redirects, out) {
			return fmt.Errorf("%q is both a redirect and a signed-out landing page — "+
				"landing on a redirect URI starts the sign-in the person just ended", out)
		}
	}

	return nil
}

// ValidateGrantNames refuses anything that is not a grant where a grant is
// required. Only a three-segment `<scope>:<thing>:<role>` name is one: a
// rung carries a session lifetime and an employee group is a person, both
// land in the same `groups` claim, and gating on either would read as a
// sensible rule while granting on a property that is not a role.
func ValidateGrantNames(groups []string) error {
	for _, group := range groups {
		if _, _, _, ok := policy.SplitGroup(group); !ok {
			return fmt.Errorf("%q is not a grant — it must be <scope>:<thing>:<role>", group)
		}
	}

	return nil
}
