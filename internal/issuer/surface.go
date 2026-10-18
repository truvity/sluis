package issuer

import (
	"log/slog"
	"net/http"
	"slices"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/storage/logattr"
)

// This file is the part of the library's surface that this issuer does not
// offer, answered as the specifications ask rather than left to whatever the
// library does with it.

// unservedPaths are the routes the library mounts from its own defaults for
// features this issuer does not offer. None is advertised, and each answered
// something: introspection, `{"active":false}` for every token once a client
// authenticated (the tokens are JWTs, verified offline against the key set);
// the device authorization endpoint, an error from a storage method that is
// not implemented. An endpoint that answers without meaning it is a question
// somebody can build a client on, so they are not routes.
var unservedPaths = map[string]bool{
	"/oauth/introspect":           true,
	"/introspect":                 true,
	"/device_authorization":       true,
	"/oauth/device_authorization": true,
}

// withoutUnserved answers a route in [unservedPaths] with a 404, before
// anything else reads the request.
func withoutUnserved(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unservedPaths[r.URL.Path] {
			http.NotFound(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Authorization request parameters that name something this issuer does not
// do. The library ignores all three (`response_mode` it honours, for modes
// that are not advertised), and an ignored parameter is a request answered as
// though it had not been made.
const (
	// errInvalidToken is RFC 6750 3.1.
	errInvalidToken = "invalid_token"

	// errRequestURINotSupported and errRegistrationNotSupported are OpenID
	// Connect Core 3.1.2.6; the library has no constant for either.
	errRequestURINotSupported   = "request_uri_not_supported"
	errRegistrationNotSupported = "registration_not_supported"
)

// servedResponseModes is what `response_modes_supported` says, and all that
// an authorization request may ask for. The code flow answers in the query
// of the redirect; `fragment` is the response mode of the implicit and hybrid
// flows, which are not served, and `form_post` needs a page that posts the
// response, which this issuer's content security policy does not let the
// library's page do. A mode that is not here is refused, not ignored.
var servedResponseModes = []string{string(oidc.ResponseModeQuery)}

// authorizeSurface refuses, at `/authorize`, a request that names a feature
// this issuer does not offer:
//
//   - `request_uri` (OpenID Connect Core 6.2): `request_uri_not_supported`;
//   - `registration` (Core 7.2.1): `registration_not_supported`;
//   - a `response_mode` that is not `query`: `invalid_request`.
//
// The error goes back to the client as a redirect when the request names a
// `redirect_uri` the client registered, as [refuseTarget] does and for the
// same reason, and as a page otherwise. It is made before the library runs,
// so a request that is refused here is not stored and signs nobody in.
//
// Every value of `response_mode` is looked at, not the first: the library
// takes the last, so judging one and acting on another is the gap a repeated
// parameter opens.
func authorizeSurface(clients clientLookup, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != authorizePath || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
			next.ServeHTTP(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			next.ServeHTTP(w, r)
			return
		}

		switch {
		case r.Form.Has("request_uri"):
			refuseAuthorize(w, r, clients, errRequestURINotSupported,
				"`request_uri` is not supported: this issuer reads no request object, by value or by reference")
		case r.Form.Has("registration"):
			refuseAuthorize(w, r, clients, errRegistrationNotSupported,
				"`registration` is not supported: this issuer registers no client from an authorization request")
		case unsupportedResponseMode(r.Form["response_mode"]):
			refuseAuthorize(w, r, clients, string(oidc.InvalidRequest),
				"`response_mode` must be `query`: it is the only response mode this issuer answers in")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// unsupportedResponseMode reports whether any value asks for a mode other
// than the ones served. Absent means the default of the response type, which
// for `code` is `query`.
func unsupportedResponseMode(values []string) bool {
	for _, value := range values {
		if value != "" && !slices.Contains(servedResponseModes, value) {
			return true
		}
	}

	return false
}

// refuseAuthorize answers an authorization request with an error of code, as
// a redirect to the client when it may be sent one and as a page or an OAuth
// error body otherwise. The fixed words go to the caller and why to the log.
func refuseAuthorize(w http.ResponseWriter, r *http.Request, clients clientLookup, code, why string) {
	logRefusal(r.Context(), http.StatusBadRequest, why, nil,
		[]any{slog.String("error", code), logattr.SafeString("client_id", r.Form.Get("client_id"))})

	if clients != nil {
		if to, ok := errorRedirect(r, clients, code, refusal.Text(r.Context(), refusal.InvalidRequest), false); ok {
			http.Redirect(w, r, to, http.StatusFound)
			return
		}
	}

	writeRefusal(w, r, code, refusal.InvalidRequest)
}
