package issuer

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/zitadel/oidc/v3/pkg/op"
)

// loopbackRedirects holds a native client's loopback redirect to what RFC 8252
// 7.3 allows: the port is free, and nothing else is.
//
// The library lets a public client name any loopback URI whose path and query
// equal a registered loopback URI's, and compares neither the scheme nor the
// host. So `https://localhost:1/cb` was accepted for a client that registered
// `http://127.0.0.1/cb`, and so was any loopback host with any scheme the
// library calls http or https. Here the request must name a loopback address
// (`127.0.0.1`, `[::1]` or `localhost`), with the scheme, the path and the query
// of a registered loopback URI, and a port that is a number (or none). A URI
// the client registered is let through as it is, as is anything that is not a
// loopback URI: for those the library's exact match stands.
//
// The refusal is the one for a redirect URI that is not registered: a page,
// never a redirect to it.
func loopbackRedirects(clients clientLookup, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != authorizePath || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
			next.ServeHTTP(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			next.ServeHTTP(w, r)
			return
		}

		clientID, redirect := r.Form.Get("client_id"), r.Form.Get("redirect_uri")
		if clientID == "" || redirect == "" || clients == nil {
			next.ServeHTTP(w, r)
			return
		}
		found, err := clients(r.Context(), clientID)
		if err != nil || found == nil || found.ApplicationType() != op.ApplicationTypeNative ||
			slices.Contains(found.RedirectURIs(), redirect) {
			next.ServeHTTP(w, r)
			return
		}
		asked, loopback := op.HTTPLoopbackOrLocalhost(redirect)
		if !loopback {
			next.ServeHTTP(w, r)
			return
		}

		if loopbackRegistered(found.RedirectURIs(), asked) {
			next.ServeHTTP(w, r)
			return
		}

		refuseRequest(w, r, "`redirect_uri` is a loopback URI that differs from the registered ones in more than the port "+
			"(RFC 8252 7.3: scheme, path and query are compared, only the port is free)")
	})
}

// loopbackRegistered reports whether a requested loopback URI is a registered
// loopback URI with another port: the same scheme, path and query, a loopback
// host, and a port that is a number in range or absent. The host may be any
// loopback spelling (RFC 8252 7.3 asks a server to allow `127.0.0.1` and
// `[::1]` alike; `localhost` is what existing registrations use).
func loopbackRegistered(registered []string, asked *url.URL) bool {
	if port := asked.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	if asked.User != nil || asked.Fragment != "" {
		return false
	}

	for _, entry := range registered {
		reg, ok := op.HTTPLoopbackOrLocalhost(entry)
		if !ok || reg.Scheme != asked.Scheme || reg.Path != asked.Path || reg.RawQuery != asked.RawQuery {
			continue
		}
		// The host is the registered one, or any of the three spellings of
		// "this machine" for another: not every address the platform calls
		// loopback (127.0.0.2, an IPv4-mapped IPv6 address).
		if reg.Hostname() == asked.Hostname() || (loopbackName(reg.Hostname()) && loopbackName(asked.Hostname())) {
			return true
		}
	}

	return false
}

// loopbackName is a spelling of "this machine" RFC 8252 7.3 names.
func loopbackName(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}
