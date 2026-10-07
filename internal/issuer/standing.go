package issuer

import (
	"context"
	"net/http"
	"time"

	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/logsafe"
)

// signInVerdict is what the issuer decided about a browser's sign-in.
type signInVerdict int

const (
	// signInStands: the sign-in may be answered from.
	signInStands signInVerdict = iota
	// signInAbsent: no cookie, one naming no live sign-in, or a store that
	// could not be read. There is nothing to end.
	signInAbsent
	// signInPastLimit: auth_time plus the absolute limit has passed.
	signInPastLimit
	// signInStale: older than the request's max_age. The request asked for
	// a newer proof; the sign-in itself is not at fault and is not ended.
	signInStale
	// signInNotAdmitted: the directory, through the issuer's resolver and
	// its hold window, no longer admits the person.
	signInNotAdmitted
)

// signInStanding is the decision and what it was made from.
type signInStanding struct {
	Verdict signInVerdict
	// Session is the sign-in the cookie named, when it named a live one.
	Session SSOSession
	// Resolution is the directory's answer for a person it admitted: the
	// groups a caller is then evaluated with, so nobody asks twice. Zero
	// for a recovery sign-in, which has no directory to ask.
	Resolution Resolution
	// Refusal is the resolver's error, for signInNotAdmitted.
	Refusal error
}

// recovered reports whether the sign-in was made by recovery: by what the
// sign-in RECORDED when it began ([RecoveryHow]), never by the shape of
// its subject. A subject string is whatever an identity provider said, and
// one that happens to look like a ServiceAccount must not skip the
// directory or be taken for the recovery account.
func (s SSOSession) recovered() bool { return s.How == RecoveryHow }

// checkSignIn is the issuer's decision about the sign-in cookie names, and
// nothing else: it ends nothing and writes no cookie. Every door that
// answers from a browser's sign-in asks it -- the silent completion of an
// authorization request and the console on this origin through
// [standingSignIn], which then ends what does not stand, and the session
// service ([SessionsService]), which refuses it -- so they cannot come to
// disagree. (Asking the directory may refresh the hold window's
// last-known groups, as every resolution does.)
//
// In order: the cookie must name a live sign-in; auth_time plus the
// absolute limit for resource must not have passed (empty resource is the
// installation's limit); the sign-in must be within maxAge (zero for
// none); and the directory must admit the person, through the issuer's
// own resolver, hold window and all -- a directory that cannot be reached
// is answered from the last-known standing for the hold window, and only
// past it, or for an identity it holds nothing for, is the person not
// admitted. A recovery sign-in has no directory to ask: the policy's
// matchers decide it at token time, exactly as for a workload.
func (i *Issuer) checkSignIn(ctx context.Context, sso *SSO, cookie, resource string, maxAge time.Duration) signInStanding {
	session, live, err := sso.Resolve(ctx, cookie)
	if err != nil || !live {
		return signInStanding{Verdict: signInAbsent}
	}

	// The limit is the one THIS request's resource allows: a read-only
	// resource may carry a longer absolute session than the installation's
	// (ADR 0033), and a browser session older than the installation's
	// limit may still complete for it, since the chain it opens is held to
	// the resource's own end. A request for anything else is held to the
	// installation's.
	if absolute := i.AbsoluteFor(resource); absolute > 0 && !time.Now().Before(session.AuthTime.Add(absolute)) {
		return signInStanding{Verdict: signInPastLimit, Session: session}
	}

	if !session.Fresh(time.Now(), maxAge) {
		return signInStanding{Verdict: signInStale, Session: session}
	}

	if session.recovered() {
		return signInStanding{Verdict: signInStands, Session: session}
	}

	resolved, err := i.resolver.Resolve(ctx, session.Identity)
	if err != nil {
		return signInStanding{Verdict: signInNotAdmitted, Session: session, Refusal: err}
	}

	return signInStanding{Verdict: signInStands, Session: session, Resolution: resolved}
}

// standingSignIn reads the sign-in a browser holds, applies to it every
// decision this issuer makes before anything is answered from it
// ([Issuer.checkSignIn]), and ENDS what does not stand, so a browser that
// is turned away at one door is not still signed in at another.
//
// It is ONE function because two doors answer from a browser's sign-in
// and may end it: the silent completion of an authorization request
// ([signIn.silent]), and the console on this origin reading who is signed
// in ([SignedInReader]). They used to be two, and the console's read only
// asked whether the cookie named a live sign-in -- so a sign-in past the
// absolute limit, or one whose person the directory had since suspended,
// went on opening the most privileged surface this service has for as
// long as the sign-in's own lifetime ran, while every other client was
// already being refused. Two copies of a security decision drift; one
// cannot.
//
// A sign-in that is merely not fresh enough for maxAge is not ended -- the
// request asked for a newer proof, and the browser goes to get one.
func standingSignIn(deps SignInDeps, w http.ResponseWriter, r *http.Request, resource string, maxAge time.Duration) (SSOSession, bool) {
	standing := deps.Issuer.checkSignIn(r.Context(), deps.SSO, SSOFromRequest(r, deps.Secure), resource, maxAge)
	switch standing.Verdict {
	case signInStands:
		return standing.Session, true

	case signInPastLimit:
		// The absolute session limit measures from auth_time and ends the
		// SIGN-IN itself, not only the silence: a browser session past its
		// limit must not go on answering /authorize at all, or a person who
		// leaves a tab open would renew their sign-in indefinitely, one
		// client at a time, without ever meeting the limit that exists to
		// stop exactly that. Ending it here runs the SAME cascade an
		// explicit sign-out does -- the per-client sessions this browser
		// opened, and Back-Channel Logout to the clients that held them --
		// so a relying party finds out the way it would if the person had
		// clicked sign out, and the browser falls through to an interactive
		// sign-in.
		//
		// Sparing what is still live: past the installation's limit the
		// only sessions this browser still holds are chains a resource
		// extended, which are inside their own limit and are ended by
		// sign-out, revocation or their own end -- never by an unrelated
		// console's silent request finding the browser session old.
		// A failure is logged there and leaves the cookie; the browser
		// goes to an interactive sign-in either way, which ends the
		// sign-in it held once it succeeds ([signIn.established]).
		_ = signOut(deps, w, r, true)

	case signInNotAdmitted:
		// The browser proved who it is; the DIRECTORY still decides whether
		// that account is live. Without this a suspended person would keep
		// signing in silently for as long as their browser session lasted
		// -- the one failure single sign-on can introduce that the login
		// path does not have, and the one an identity service least wants.
		//
		// The sign-in record ends and the cookie is cleared, and nothing
		// more: the per-client sessions opened under it are not revoked
		// here, no logout token is sent and nothing is audited. Each of
		// those clients meets the directory's refusal itself, at its next
		// refresh, which resolves the person afresh.
		deps.log().WarnContext(r.Context(), "browser session is no longer admitted",
			"identity", logsafe.Value(standing.Session.Identity), "error", logsafe.Error(standing.Refusal))
		_ = deps.SSO.End(r.Context(), standing.Session.ID)
		http.SetCookie(w, deps.SSO.Cookie("", deps.Secure))

	case signInAbsent, signInStale:
	}

	return SSOSession{}, false
}

// SignedInReader returns what the console on this origin asks on every
// request: whose sign-in does this browser hold, if it still stands.
//
// It is [standingSignIn] for the console's own client -- no resource, so
// the installation's absolute limit, and no max_age -- with the same
// dependencies the issuer's handler is built with, so that a sign-in it
// ends is ended exactly as the issuer's silent sign-in ends one, and the
// browser's cookie is cleared through w. Past the absolute limit that is
// the sign-out cascade: the per-client sessions the browser opened that
// are not inside a limit of their own, and Back-Channel Logout to the
// clients that held them. For a person the directory no longer admits it
// is the sign-in record and the cookie only.
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
