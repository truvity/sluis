package issuer

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/httpsec"
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
//
// The path is compared as a router would reach it, not as spelled: cleaned of
// repeated and trailing slashes and folded to lower case. The library's router
// is the one place that would answer a spelling this map does not list.
func withoutUnserved(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unservedPaths[strings.ToLower(path.Clean("/"+r.URL.Path))] {
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
	logRefusal(r, http.StatusBadRequest, why, nil,
		[]any{slog.String("error", code), logattr.SafeString("client_id", r.Form.Get("client_id"))})

	if clients != nil {
		if to, ok := errorRedirect(r, clients, code, refusal.Text(r.Context(), refusal.InvalidRequest), false); ok {
			http.Redirect(w, r, to, http.StatusFound)
			return
		}
	}

	writeRefusal(w, r, code, refusal.InvalidRequest)
}

// endSessionPurpose is what the form of the `/end_session` confirmation is
// for, so that its token opens nothing else (and `/logout`'s opens this not).
const endSessionPurpose = "end_session"

// endSessionKept are the parameters of an `/end_session` request that the
// confirmation carries through to the button. The hint is one, when there is
// one: a hint that is not the browser's own sign-in is why the page asks, and
// the library still has to verify it and match the client to it.
var endSessionKept = []string{"client_id", "post_logout_redirect_uri", "state", "id_token_hint"}

// hintSubject is the `sub` of an ID token hint read WITHOUT verifying it, or
// empty when it has none. Only for deciding whether to ask the person: the
// library verifies the signature before anything is ended, so a hint forged
// to pass this gets no further.
func hintSubject(hint string) string {
	parts := strings.Split(hint, ".")
	if len(parts) != 3 {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}

	var claims struct {
		Subject string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}

	return claims.Subject
}

// hintClient is the client a hint names (`azp`, else the one audience),
// read without verifying it, as the library will take it after it has.
func hintClient(hint string) string {
	parts := strings.Split(hint, ".")
	if len(parts) != 3 {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}

	var claims struct {
		Audience oidc.Audience `json:"aud"`
		AZP      string        `json:"azp"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}

	if claims.AZP != "" {
		return claims.AZP
	}

	if len(claims.Audience) == 1 {
		return claims.Audience[0]
	}

	return ""
}

// endSessionClient is the client an `/end_session` request names: the hint's
// (the library takes `azp` from it) or else `client_id`.
func endSessionClient(r *http.Request) string {
	if hint := r.Form.Get("id_token_hint"); hint != "" {
		return hintClient(hint)
	}

	return r.Form.Get("client_id")
}

// endSessionRedirectRefusal is why a request's `post_logout_redirect_uri` is
// not one its client registered, or empty when it is, or when there is no
// address or no client known to judge it by (the library then answers).
//
// The library accepts a registered address exactly, and for a native client
// a loopback address that equals a registered loopback one in path and query
// alone. The second is held to RFC 8252 7.3 here, as [loopbackRedirects]
// holds `redirect_uri`: scheme, path and query, a loopback host, and only the
// port free.
func endSessionRedirectRefusal(r *http.Request, clients clientLookup) string {
	uri := r.Form.Get("post_logout_redirect_uri")
	if uri == "" || clients == nil {
		return ""
	}

	id := endSessionClient(r)
	if id == "" {
		return ""
	}

	found, err := clients(r.Context(), id)
	if err != nil || found == nil {
		return ""
	}

	if op.ValidateEndSessionPostLogoutRedirectURI(uri, found) != nil {
		return "post_logout_redirect_uri is not one the client registered"
	}

	if slices.Contains(found.PostLogoutRedirectURIs(), uri) || found.ApplicationType() != op.ApplicationTypeNative {
		return ""
	}

	if asked, loopback := op.HTTPLoopbackOrLocalhost(uri); loopback && !loopbackRegistered(found.PostLogoutRedirectURIs(), asked) {
		return "post_logout_redirect_uri is a loopback address that differs from the registered ones in more than the port"
	}

	return ""
}

// confirmedEndSession reports whether an `/end_session` request may go on to
// end the sign-in, and answers it itself when it may not.
//
//   - A deployment with no browser sign-ins has nothing to end.
//   - A browser holding no live sign-in has nothing to end, and nothing to ask
//     about: the request goes on as it did.
//   - A request whose `id_token_hint` names the person this browser is signed
//     in as goes on: the relying party that signed them in is the one asking.
//   - Any other GET is answered with a page and a button, and ends nothing.
//     The button POSTs the same parameters back with a token bound to the
//     sign-in cookie, as `/logout` does.
//   - A POST goes on only with that token. A page of somebody else's cannot
//     read it, and the cookie is SameSite=Lax, so it does not ride a
//     cross-site POST either.
//
// A deployment that has sign-ins and no key to bind the token with refuses
// with 503 and ends nothing, rather than ending without asking.
//
// The library then validates the parameters exactly as for a GET, so what a
// confirmed request may do is what an unconfirmed one always could.
func confirmedEndSession(signIn SignInDeps, w http.ResponseWriter, r *http.Request) bool {
	if signIn.SSO == nil {
		return true
	}

	if signIn.State == nil {
		logRefusal(r, http.StatusServiceUnavailable, "no key to bind the sign-out confirmation with", nil, nil)
		_ = writePage(w, http.StatusServiceUnavailable, "Sign-out is unavailable",
			`<p>This service cannot ask you to confirm a sign-out just now, so nothing was ended.</p>
	<p class="note">You are still signed in. Try again later.</p>`)

		return false
	}

	cookie := SSOFromRequest(r, signIn.Secure)
	if cookie == "" {
		return true
	}

	session, live, err := signIn.SSO.Resolve(r.Context(), cookie)
	if err == nil && !live {
		return true
	}

	// An unreadable store is "ask": the page costs a click and ends nothing.
	if hint := hintSubject(r.Form.Get("id_token_hint")); err == nil && hint != "" && strings.EqualFold(hint, session.Identity) {
		return true
	}

	if r.Method == http.MethodPost {
		if signIn.State.CSRFValid(endSessionPurpose, cookie, r.PostFormValue("csrf")) {
			return true
		}

		recordLoginFailure(r.Context(), LoginBadState)
		logRefusal(r, http.StatusBadRequest, "the sign-out did not come from the sign-out page", nil, nil)
		_ = writePage(w, http.StatusBadRequest, "Sign-out not confirmed",
			`<p>This sign-out did not come from the page that asks, so nothing was ended.</p>
	<p class="note">You are still signed in. <a href="/logout">Sign out</a> from the page that asks.</p>`)

		return false
	}

	if r.Method != http.MethodGet {
		return true
	}

	token, err := signIn.State.CSRFToken(endSessionPurpose, cookie)
	if err != nil {
		refusalPage(w, r, http.StatusInternalServerError, "the sign-out could not be prepared", err)
		return false
	}

	var fields strings.Builder

	for _, name := range endSessionKept {
		if value := r.Form.Get(name); value != "" {
			fmt.Fprintf(&fields, `<input type="hidden" name="%s" value="%s">`, name, html.EscapeString(value))
		}
	}

	// Who asked, when the request names a client this installation declares:
	// the name the policy gives it, never one from the request.
	asker := "An application"

	if id := endSessionClient(r); id != "" && signIn.Issuer != nil {
		if declared, ok := signIn.Issuer.Policy().Client(id); ok {
			asker = "<strong>" + html.EscapeString(declared.Title(id)) + "</strong>"
		}
	}

	// The answer to the form is a redirect to the relying party, which the
	// page's content security policy holds back by default: see
	// [httpsec.Policy.FormsMayRedirect].
	httpsec.Apply(w, httpsec.Policy{FormsMayRedirect: true})
	_ = writePage(w, http.StatusOK, "Sign out?",
		`<p>`+asker+` asked to sign you out. This ends your sign-in here, and the sessions it opened in a browser.</p>
	<p class="note">Agent connections you allowed to work in the background keep running.</p>
	<form method="post" action="`+endSessionPath+`">
		<input type="hidden" name="csrf" value="`+html.EscapeString(token)+`">`+fields.String()+`
		<p><button type="submit">Sign out</button></p>
	</form>`)

	return false
}
