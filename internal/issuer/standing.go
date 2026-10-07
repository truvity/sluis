package issuer

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/logsafe"
)

// standingSignIn reads the sign-in a browser holds and applies to it every
// decision this issuer makes before anything is answered from it, and
// reports whether it still stands.
//
// It is ONE function because two doors answer from a browser's sign-in:
// the silent completion of an authorization request ([signIn.silent]),
// and the console on this origin reading who is signed in
// ([SignedInReader]). They used to be two, and the console's read only
// asked whether the cookie named a live sign-in -- so a sign-in past the
// absolute limit, or one whose person the directory had since suspended,
// went on opening the most privileged surface this service has for as
// long as the sign-in's own lifetime ran, while every other client was
// already being refused. Two copies of a security decision drift; one
// cannot.
//
// resource is the RFC 8707 resource the request is for, which sets the
// absolute limit (empty is the client itself, held to the installation's
// limit); maxAge is the request's own max_age, zero for none. A sign-in
// that is merely not fresh enough is not ended -- the request asked for a
// newer proof, and the browser goes to get one.
//
// What does not stand is ENDED, not only refused, exactly as below, so a
// browser that is turned away at one door is not still signed in at the
// other.
func standingSignIn(deps SignInDeps, w http.ResponseWriter, r *http.Request, resource string, maxAge time.Duration) (SSOSession, bool) {
	session, live, err := deps.SSO.Resolve(r.Context(), SSOFromRequest(r, deps.Secure))
	if err != nil || !live {
		return SSOSession{}, false
	}

	// The absolute session limit measures from auth_time and ends the
	// SIGN-IN itself, not only the silence: a browser session past its
	// limit must not go on answering /authorize at all, or a person who
	// leaves a tab open would renew their sign-in indefinitely, one
	// client at a time, without ever meeting the limit that exists to
	// stop exactly that. Ending it here runs the SAME cascade an explicit
	// sign-out does -- the per-client sessions this browser opened, and
	// Back-Channel Logout to the clients that held them -- so a relying
	// party finds out the way it would if the person had clicked sign
	// out, and the browser falls through to an interactive sign-in below.
	//
	// The limit is the one THIS request's resource allows: a read-only
	// resource may carry a longer absolute session than the installation's
	// (ADR 0033), and a browser session older than the installation's
	// limit may still complete for it, since the chain it opens is held to
	// the resource's own end. A request for anything else is held to the
	// installation's, as before.
	if absolute := deps.Issuer.AbsoluteFor(resource); absolute > 0 && !time.Now().Before(session.AuthTime.Add(absolute)) {
		// Sparing what is still live: past the installation's limit the
		// only sessions this browser still holds are chains a resource
		// extended, which are inside their own limit and are ended by
		// sign-out, revocation or their own end -- never by an unrelated
		// console's silent request finding the browser session old.
		// A failure is logged there and leaves the cookie; the browser
		// goes to an interactive sign-in either way, which ends the
		// sign-in it held once it succeeds ([signIn.established]).
		_ = signOut(deps, w, r, true)
		return SSOSession{}, false
	}

	if !session.Fresh(time.Now(), maxAge) {
		return SSOSession{}, false
	}

	// The browser proved who it is; the DIRECTORY still decides whether
	// that account is live. Without this a suspended person would keep
	// signing in silently for as long as their browser session lasted --
	// the one failure single sign-on can introduce that the login path
	// does not have, and the one an identity service least wants. A
	// ServiceAccount subject (a recovery sign-in) has no directory to ask:
	// the policy's matchers decide it at token time, exactly as they do
	// for a workload.
	//
	// The resolver is the issuer's own, hold window and all: a directory
	// that cannot be reached is answered from the last-known standing for
	// the hold window, and only past it, or for an identity it holds
	// nothing for, does the sign-in end.
	if strings.Contains(session.Identity, "@") {
		if _, err = deps.Issuer.resolver.Resolve(r.Context(), session.Identity); err != nil {
			deps.log().WarnContext(r.Context(), "browser session is no longer admitted",
				"identity", logsafe.Value(session.Identity), "error", logsafe.Error(err))
			_ = deps.SSO.End(r.Context(), session.ID)
			http.SetCookie(w, deps.SSO.Cookie("", deps.Secure))

			return SSOSession{}, false
		}
	}

	return session, true
}

// SignedInReader returns what the console on this origin asks on every
// request: whose sign-in does this browser hold, if it still stands.
//
// It is [standingSignIn] for the console's own client -- no resource, so
// the installation's absolute limit, and no max_age -- with the same
// dependencies the issuer's handler is built with, so that a sign-in it
// ends is ended the way the issuer's own pages end one: the per-client
// sessions, Back-Channel Logout to the clients that asked, and the
// browser's cookie cleared through w.
//
// Nil where the issuer keeps no browser sessions, and then the console
// has no sign-in of the issuer's to read.
func SignedInReader(iss *Issuer, storage op.Storage, deps SignInDeps) func(http.ResponseWriter, *http.Request) (SSOSession, bool) {
	deps = deps.over(iss, storage)
	if deps.SSO == nil {
		return nil
	}

	return func(w http.ResponseWriter, r *http.Request) (SSOSession, bool) {
		session, ok := standingSignIn(deps, w, r, "", 0)
		if !ok || session.Identity == "" {
			return SSOSession{}, false
		}

		return session, true
	}
}

// over completes deps with what the issuer and its storage supply, where
// the caller left it empty: the issuer itself, its browser sessions, the
// storage that completes requests, and the Back-Channel Logout a sign-in
// that ends announces. [HandlerWithSignIn] and [SignedInReader] both build
// from it, so the console ends a sign-in with exactly the dependencies
// the issuer's own pages do.
func (d SignInDeps) over(iss *Issuer, storage op.Storage) SignInDeps {
	d.Issuer = iss
	if d.SSO == nil {
		d.SSO = iss.SSO()
	}
	if completer, ok := storage.(Completer); ok && d.Storage == nil {
		d.Storage = completer
	}

	// Wired here because this is the one place that holds BOTH halves:
	// the storage owns the signing key a logout token needs, and the
	// sign-in deps own the moment a sign-out happens.
	if own, ok := storage.(*Storage); ok && d.Announce == nil {
		log := d.log()
		d.Announce = func(ctx context.Context, ended []Session) {
			own.announceLogout(ctx, log, ended)
		}
	}

	return d
}
