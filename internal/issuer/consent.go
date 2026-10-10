package issuer

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/storage/logattr"
)

// The agent consent page (docs/decisions/0040-agent-class-sessions.md,
// decision 6).
//
// An agent-class authorization opens a chain that lives for weeks in a
// credential store the person does not see, so it never completes
// silently: after the person is authenticated -- by a provider round trip
// or from the browser's sign-in -- [Storage.Complete] refuses it with
// [ErrAgentConsentRequired], and the door that authenticated renders this
// page in its place. Its accept is a POST carrying an acceptance token
// bound to the request, the person and the sign-in, beside a cookie of its
// own that only this browser holds; [Storage.Complete] verifies both
// itself.
//
// Why a page in front of the provider buttons would not be enough: a start
// link is a GET anybody holding the request id can open, so an attacker
// who began `/authorize` for their own client could send it to a victim
// whose provider then answers silently on an earlier consent. With the page
// after authentication, the victim's browser ends on a page naming the
// client and the deadline, and the attacker -- who has neither the cookie
// nor the token -- can complete nothing.

// agentConsentPath is where the page posts its accept.
const agentConsentPath = "/login/agent-consent"

// askAgentConsent answers a completion refused for want of an acceptance
// with the consent page, and reports whether that is what happened. It is
// the ONLY place an acceptance token is minted: after authentication, for
// the person just established (who), under the sign-in their browser now
// holds.
func (s *signIn) askAgentConsent(w http.ResponseWriter, r *http.Request, err error, request string, who Authenticated) bool {
	if !errors.Is(err, ErrAgentConsentRequired) {
		return false
	}

	// The accept re-reads the browser's sign-in, so a browser that holds
	// none -- no sign-in store, or one that could not be written -- has
	// nothing to accept under.
	if s.deps.SSO == nil || s.deps.State == nil || who.SSO == "" {
		recordLoginFailure(r.Context(), LoginConsentRefused)
		_ = writePage(w, http.StatusServiceUnavailable, "This connection cannot be accepted here",
			`<p>`+named(s.pendingOf(request), "This application")+` asks for a connection that works in the background,
	which has to be accepted in a browser that is signed in here, and this sign-in could not be kept.</p>
	<p class="note">Try again in a moment.</p>`)

		return true
	}

	token, err := s.deps.State.IssueAgentConsent(request, access.AgentConsentActor(who.Subject, who.SSO))
	if err != nil {
		s.deps.log().ErrorContext(r.Context(), "an agent-consent token could not be minted", logattr.SafeError("error", err))
		http.Error(w, "the sign-in could not be completed", http.StatusInternalServerError)

		return true
	}

	nonce, err := scriptNonce()
	if err != nil {
		s.deps.log().ErrorContext(r.Context(), "a consent page's script nonce could not be drawn", logattr.SafeError("error", err))
		http.Error(w, "the sign-in could not be completed", http.StatusInternalServerError)

		return true
	}

	access.SetCookie(w, access.AgentConsentCookie(token, s.deps.Secure, signInWindow))
	noFraming(w)
	// The page's one script is its own, by nonce; nothing else runs here.
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; script-src 'nonce-"+nonce+"'")
	s.page(w, "Allow a background connection?", agentConsentBody(s.pendingOf(request), who, token, nonce))

	return true
}

// allowArmDelay is how long the consent page must have been visible and
// focused before its Allow button takes a click.
const allowArmDelay = 500 * time.Millisecond

// scriptNonce is a fresh nonce for one page's inline script.
func scriptNonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	return base64.RawStdEncoding.EncodeToString(raw), nil
}

// noFraming refuses to let the page be shown inside another one: a
// one-click accept in a frame is a click somebody else's page can steal.
func noFraming(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
}

// agentConsentBody is what the person is asked to accept: the client, its
// origin when it describes itself with a document, where the code goes
// back to, that it is a background connection, the computed deadline and
// who is signed in. Every word comes from the policy, the pending request
// and the sign-in, none from the query string.
//
// The Allow button is inert until the page has been VISIBLE AND FOCUSED for
// [allowArmDelay] (DoubleClickjacking: a page that opens this one under the
// person's cursor between the two clicks of a double-click would otherwise
// have the second click land on Allow). It is disabled in the markup, so
// keyboard and pointer alike wait, and the script disables it again
// whenever the page is hidden or loses focus, and starts the delay over
// when it is visible and focused again (document.hasFocus() at load).
func agentConsentBody(pending Pending, who Authenticated, token, nonce string) string {
	name := html.EscapeString(pending.Client.Title(pending.ClientID))

	var out strings.Builder

	fmt.Fprintf(&out, `<p><strong>%s</strong> asks to work on your behalf in the background.</p>`, name)

	if description := strings.TrimSpace(pending.Client.Description); description != "" {
		fmt.Fprintf(&out, `<p class="note">%s</p>`, html.EscapeString(description))
	}

	if pending.ResourceName != "" {
		fmt.Fprintf(&out, `<p>It asks for access to <strong>%s</strong>.</p>`, html.EscapeString(pending.ResourceName))
	}

	if target, err := documentURL(pending.ClientID); err == nil {
		fmt.Fprintf(&out, `<p class="note">It describes itself from <span class="host">%s</span></p>`,
			html.EscapeString(target.Scheme+"://"+target.Host))
	}

	host, loopback := returnHost(pending.RedirectURI)

	switch {
	case loopback:
		out.WriteString(`<p class="note">It returns to a program on this computer, not to a website.</p>`)
	case host != "":
		fmt.Fprintf(&out, `<p class="note">It returns to <span class="host">%s</span></p>`, html.EscapeString(host))
	}

	out.WriteString(`<p>This is an <strong>agent connection</strong>: it keeps working while you are away, and signing out of this browser does not end it.`)

	if pending.AgentAbsolute > 0 && !who.AuthTime.IsZero() {
		deadline := who.AuthTime.Add(pending.AgentAbsolute).UTC()
		fmt.Fprintf(&out, ` It lasts until <strong>%s</strong> at the latest, unless it is revoked first or not used for a while.`,
			html.EscapeString(deadline.Format("2 January 2006, 15:04 MST")))
	}

	out.WriteString(`</p>`)

	fmt.Fprintf(&out, `<p class="note">Signed in as <strong>%s</strong></p>`, html.EscapeString(who.Subject))
	fmt.Fprintf(&out, `<form method="post" action="%s">
		<input type="hidden" name="state" value="%s">
		<p><button type="submit" id="allow" disabled>Allow</button></p>
	</form>
	<noscript><p class="warn">Allowing a background connection needs JavaScript in this browser.</p></noscript>
	<script nonce="%s">(function(){var b=document.getElementById("allow"),armed=0;
	function ready(){return document.visibilityState==="visible"&&document.hasFocus();}
	function arm(){var mine=++armed;b.disabled=true;
	if(ready()){setTimeout(function(){if(mine===armed&&ready()){b.disabled=false;}},%d);}}
	document.addEventListener("visibilitychange",arm);
	window.addEventListener("focus",arm);window.addEventListener("blur",arm);arm();})();</script>
	<p class="note">If you did not just start this, close this page. To end a connection later, use the console's
	Sessions page or <em>Disconnect all agents</em>.</p>`,
		agentConsentPath, html.EscapeString(token), nonce, allowArmDelay.Milliseconds())

	return out.String()
}

// acceptAgent is the consent page's accept: a POST from the browser it was
// shown in, re-checked against the sign-in that browser holds now, and
// completed by [Storage.Complete], which verifies the acceptance itself.
func (s *signIn) acceptAgent(w http.ResponseWriter, r *http.Request) {
	noFraming(w)

	r.Body = http.MaxBytesReader(w, r.Body, access.AgentConsentFormLimit)
	if err := r.ParseForm(); err != nil {
		recordLoginFailure(r.Context(), LoginBadRequest)
		http.Error(w, "that form could not be read", http.StatusBadRequest)

		return
	}

	consent := access.AgentConsentPresented(r, r.PostFormValue("state"), s.deps.Secure)

	var request string
	if s.deps.State != nil {
		request = s.deps.State.Request(consent)
	}

	if request == "" || s.deps.Storage == nil || s.deps.SSO == nil {
		recordLoginFailure(r.Context(), LoginBadState)
		http.Error(w, "this acceptance is not valid any more; start again from the application", http.StatusBadRequest)

		return
	}

	pending, err := s.deps.Storage.Pending(request)
	if err != nil {
		recordLoginFailure(r.Context(), LoginNotWaiting)
		http.Error(w, "that sign-in is no longer waiting to be completed", http.StatusBadRequest)

		return
	}

	// Re-checked on the click: a person signed out, past the limit or
	// suspended between seeing the page and clicking cannot complete.
	session, ok := standingSignIn(s.deps, w, r, s.limitResource(pending), pending.MaxAge)
	if !ok {
		recordLoginFailure(r.Context(), LoginConsentRefused)
		_ = writePage(w, http.StatusForbidden, "You are not signed in here any more",
			`<p>The connection was not allowed: this browser's sign-in ended, or no longer stands, since the page was shown.</p>
	<p class="note">Start again from the application.</p>`)

		return
	}

	err = s.deps.Storage.Complete(r.Context(), request, Authenticated{
		Subject:  session.Identity,
		AuthTime: session.AuthTime,
		SSO:      session.ID,
		How:      session.How,
		Consent:  consent,
	})
	if err != nil {
		if s.refuseUnentitled(w, r, err, request) {
			return
		}

		if errors.Is(err, ErrAgentConsentRequired) {
			// Said once, the reason in the log: the post did not come with
			// the acceptance this browser was shown, for this request and
			// this person.
			s.deps.log().WarnContext(r.Context(), "an agent connection's acceptance was refused",
				logattr.SafeString("client", pending.ClientID), logattr.SafeError("error", err))
			recordLoginFailure(r.Context(), LoginConsentRefused)
			_ = writePage(w, http.StatusForbidden, "This connection was not allowed",
				`<p>This acceptance was not made in this browser, for this request, by the person signed in here.</p>
	<p class="note">Start again from the application.</p>`)

			return
		}

		recordLoginFailure(r.Context(), LoginNotWaiting)
		http.Error(w, "that sign-in is no longer waiting to be completed", http.StatusBadRequest)

		return
	}

	// Spent: the page is not one to post twice.
	access.SetCookie(w, access.AgentConsentCookie("", s.deps.Secure, 0))
	recordLoginSuccess(r.Context(), "agent_consent")
	s.deps.Log.InfoContext(r.Context(), "an agent connection was allowed",
		logattr.SafeString("identity", session.Identity), logattr.SafeString("client", pending.ClientID))
	http.Redirect(w, r, s.deps.Return(r.Context(), request), http.StatusFound)
}

// limitResource is the resource a browser's sign-in is held to the
// absolute limit of, for a request. An agent client's is never longer than
// the installation's: a resource that extends a chain for a person at a
// browser does not extend the sign-in that accepts a background
// connection (decision 6).
func (s *signIn) limitResource(pending Pending) string {
	if !pending.Agent || s.deps.Issuer == nil || pending.Resource == "" {
		return pending.Resource
	}

	if s.deps.Issuer.AbsoluteFor(pending.Resource) > s.deps.Issuer.AbsoluteFor("") {
		return ""
	}

	return pending.Resource
}
