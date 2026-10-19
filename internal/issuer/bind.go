package issuer

import (
	"net/http"
	"net/url"

	"github.com/truvity/sluis/internal/access"
)

// Binding an authorization request to its browser.
//
// `/authorize` makes a request and sends the browser to `/login?auth=<id>`;
// whoever opens that address with a live sign-in completes the request as
// themselves. The id is in a URL, so an attacker who starts a request of
// their own can send the link to a victim, and the victim's silent sign-in
// completes the ATTACKER's request: the code goes to the attacker's
// redirect, and a relying party that does not tie `state` to a cookie of
// its own has no way to tell. (The agent consent page already defends
// against this, with a cookie of its own; interactive clients had nothing.)
//
// `/authorize` therefore sets a short-lived cookie holding a MAC of the
// request id ([access.StateCodec.RequestTag]), and every step that would
// advance the request -- the chooser, the provider's start, the silent
// completion and the callback that hands the code over -- asks for it. A
// browser without it is shown a page that says so and nothing completes.
//
// A deployment with no sign-in state (a provider assembled without sign-in
// pages) has nothing to bind and enforces nothing; every deployment that
// serves a sign-in has one.

// authorizeCallbackPath is where the library hands the code over once the
// request is complete.
const authorizeCallbackPath = authorizePath + "/callback"

// boundHere reports whether this browser started the authorization request:
// the request carries the cookie for its id.
func (s *signIn) boundHere(r *http.Request, request string) bool {
	return boundHere(s.deps, r, request)
}

func boundHere(deps SignInDeps, r *http.Request, request string) bool {
	if deps.State == nil {
		return true
	}

	if deps.State.RequestStartedHere(r, request, deps.Secure) {
		return true
	}
	// The refusal that follows names the client the request is for, which
	// this browser's request names only by the authorization's id.
	if deps.Storage != nil && request != "" {
		if pending, err := deps.Storage.Pending(request); err == nil {
			noteClient(r.Context(), pending.ClientID)
		}
	}

	return false
}

// startedElsewhere answers a request this browser did not start. The words
// are fixed and name nothing of the request: whoever reads them may be the
// victim of a link, and is owed no more than what to do.
func startedElsewhere(w http.ResponseWriter, r *http.Request) {
	recordLoginFailure(r.Context(), LoginBadState)
	logRefusal(r, http.StatusBadRequest, "the authorization request was not started in this browser", nil, nil)

	_ = writePage(w, http.StatusBadRequest, "Sign-in started elsewhere",
		`<p>This sign-in was started in another browser.</p>
	<p class="note">Nothing was signed in. If you were sent here by a link, close this page.
	To sign in here, open the application again from this browser.</p>`)
}

// bindAuthorization is the HTTP half: it sets the cookie where
// `/authorize` redirects to the sign-in, and refuses the library's
// callback for a request this browser did not start.
func bindAuthorization(deps SignInDeps, next http.Handler) http.Handler {
	if deps.State == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case authorizePath:
			next.ServeHTTP(&bindingWriter{ResponseWriter: w, r: r, deps: deps}, r)
		case authorizeCallbackPath:
			if !boundHere(deps, r, r.URL.Query().Get("id")) {
				startedElsewhere(w, r)
				return
			}

			next.ServeHTTP(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// bindingWriter adds the cookie to the response that sends the browser to
// the sign-in, at the moment its status is written and not before: the
// library decides what to answer, and a refusal or an error redirect to the
// client carries no request to bind.
type bindingWriter struct {
	http.ResponseWriter

	r    *http.Request
	deps SignInDeps
	done bool
}

func (b *bindingWriter) WriteHeader(status int) {
	if !b.done {
		b.done = true
		b.bind(status)
	}

	b.ResponseWriter.WriteHeader(status)
}

func (b *bindingWriter) Write(p []byte) (int, error) {
	if !b.done {
		b.WriteHeader(http.StatusOK)
	}

	return b.ResponseWriter.Write(p)
}

// Unwrap lets [http.ResponseController] reach the real writer.
func (b *bindingWriter) Unwrap() http.ResponseWriter { return b.ResponseWriter }

func (b *bindingWriter) bind(status int) {
	if status < http.StatusMultipleChoices || status >= http.StatusBadRequest {
		return
	}

	where, err := url.Parse(b.Header().Get("Location"))
	if err != nil || where.Path != "/login" {
		return
	}

	request := where.Query().Get("auth")
	if request == "" {
		return
	}

	held := ""
	if cookie, err := access.ReadCookie(b.r, access.AuthRequestCookieName, b.deps.Secure); err == nil {
		held = cookie.Value
	}

	value, err := b.deps.State.AddRequest(held, request)
	if err != nil {
		return
	}

	access.SetCookie(b.ResponseWriter, access.AuthRequestCookie(value, b.deps.Secure, authRequestTTL))
}
