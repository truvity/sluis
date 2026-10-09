package issuer

import (
	"net/http"
	"net/url"
	"strings"
)

// authorizationResponseIssuer appends `iss` to every authorization
// response that goes back to a client, success and error alike (RFC 9207).
//
// A client that talks to more than one authorization server cannot tell
// from a redirect alone which of them sent it, and a malicious one can
// pass its own code off as another's (the mix-up attack). `iss` names the
// sender, and a client that checks it is safe from that; discovery says
// the parameter is there so that the client knows to insist on it.
//
// The library builds the success redirect and most error redirects, and
// this issuer's own sign-in handlers build the rest, so the parameter is
// added where they all converge: on the way out, to a redirect that leaves
// for another origin and carries a `code` or an `error`. A redirect to a
// sign-in page, to a consent page or to an upstream provider is none of
// those and is left alone, and so is one that already carries `iss`.
func authorizationResponseIssuer(issuerURL string, next http.Handler) http.Handler {
	base, err := url.Parse(issuerURL)
	if err != nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&issuerWriter{ResponseWriter: w, issuer: issuerURL, base: base}, r)
	})
}

type issuerWriter struct {
	http.ResponseWriter

	issuer string
	base   *url.URL
}

func (i *issuerWriter) Unwrap() http.ResponseWriter { return i.ResponseWriter }

func (i *issuerWriter) WriteHeader(status int) {
	if status >= 300 && status < 400 {
		if where := i.ResponseWriter.Header().Get("Location"); where != "" {
			i.ResponseWriter.Header().Set("Location", withIssuer(where, i.issuer, i.base))
		}
	}

	i.ResponseWriter.WriteHeader(status)
}

// withIssuer returns a redirect target with `iss` added, or the target
// unchanged when it is not an authorization response.
func withIssuer(location, issuer string, base *url.URL) string {
	target, err := url.Parse(location)
	if err != nil || target.Scheme == "" {
		// Relative: it is a page of this issuer's.
		return location
	}
	if strings.EqualFold(target.Scheme, base.Scheme) && strings.EqualFold(target.Host, base.Host) {
		return location
	}

	// The response is in the fragment only when the client asked for that
	// mode and there is nothing in the query.
	if target.RawQuery == "" && target.Fragment != "" {
		if carriesResult(target.Fragment) {
			// Spliced as text: re-encoding the fragment would escape
			// the escapes already in it.
			return location + "&iss=" + url.QueryEscape(issuer)
		}
		return location
	}
	if carriesResult(target.RawQuery) {
		target.RawQuery += "&iss=" + url.QueryEscape(issuer)
		return target.String()
	}

	return location
}

// carriesResult reports whether a parameter string holds an authorization
// code or an error, and no `iss` yet.
func carriesResult(params string) bool {
	values, err := url.ParseQuery(params)
	if err != nil || values.Has("iss") {
		return false
	}

	return values.Has("code") || values.Has("error")
}
