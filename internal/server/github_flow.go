package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/storage/logattr"
)

// The callbacks of a GitHub flow are routes of this service, because GitHub
// sends the browser back to it. What it does with one is small: verify the
// signed state and the cookie the browser was pinned with (the session key
// stays here), then forward the one-time code to the GitHub module, which does
// the exchange, keeps the key and records the connection
// (docs/decisions/0071, v1.76 D-v176-1 b).

// githubFlowDeadline bounds a call that finishes a flow. The code GitHub
// returns is single use, so the call is given longer than an ordinary one and
// is never retried: a lost answer after the exchange loses the key, as it
// always did.
const githubFlowDeadline = 25 * time.Second

// githubVerified checks what every GitHub redirect must carry: the state this
// browser was given, signed by this service, and the cookie it was pinned to.
// It returns what the state binds, with the one-time values the redirect
// brought, and the caller snapshot of whoever is signed in now (empty when
// nobody is). The records are the module's: whether that caller may still
// finish the flow is asked there.
func (s *ConsoleServer) githubVerified(w http.ResponseWriter, r *http.Request) (consolewire.Flow, consolecall.Caller, bool) {
	state := r.URL.Query().Get("state")
	cookie, err := access.ReadCookie(r, access.ConnectCookieName, s.sessions.Secure())
	if err != nil || cookie.Value == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		s.githubNotThisBrowser(w, r)
		return consolewire.Flow{}, consolecall.Caller{}, false
	}
	binding, err := s.state.VerifyBinding(state)
	if err != nil {
		s.githubProblem(w, r, http.StatusBadRequest, "This connect cannot be finished.", err.Error(), nil)
		return consolewire.Flow{}, consolecall.Caller{}, false
	}
	var caller consolecall.Caller
	if id, signedIn := IdentityFrom(r.Context()); signedIn {
		caller = consolecall.Snapshot(id)
	}
	q := r.URL.Query()
	return consolewire.Flow{
		Bind: binding.Bind, Actor: binding.Actor, Owner: binding.Owner,
		Code: q.Get("code"), InstallationID: q.Get("installation_id"),
	}, caller, true
}

func (s *ConsoleServer) githubNotThisBrowser(w http.ResponseWriter, r *http.Request) {
	s.githubProblem(w, r, http.StatusBadRequest, "This connect did not start in this browser.", "", []string{
		"It was started in another browser, profile or private window.",
		"More than ten minutes passed on GitHub's page before returning.",
		"The browser is refusing the cookie this flow is pinned to.",
	})
}

// githubForward hands a verified flow to the GitHub module and returns what it
// answers. A refusal the module told is the page's; a failure of the hop is
// told in fixed words, with the reason in the log under the request's id.
func githubForward[Resp any](
	s *ConsoleServer, w http.ResponseWriter, r *http.Request, method string, flow consolewire.Flow, caller consolecall.Caller,
) (Resp, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), githubFlowDeadline)
	defer cancel()
	out, err := consolecall.ProxyJSON[consolewire.Flow, Resp](ctx, s.console.githubModule(), consolewire.Module, method, caller, flow)
	if err == nil {
		return out, true
	}
	if p, isProblem := err.(*consolecall.Problem); isProblem { //nolint:errorlint // a Problem is returned as itself
		status := p.Status
		if status == 0 {
			status = http.StatusConflict
		}
		s.githubProblem(w, r, status, p.Summary, p.Detail, p.Hints)
		return out, false
	}
	s.log.WarnContext(r.Context(), "a GitHub flow could not be finished by the module",
		logattr.SafeString("method", method), logattr.SafeError("error", err))
	s.githubProblem(w, r, http.StatusConflict, refusal.Text(r.Context(), refusal.ServerError)+
		": the GitHub service did not finish this connect.", "", []string{
		"The one-time code GitHub returned can be used once: start Connect again.",
	})
	return out, false
}

// githubNext signs the state for the next step of a flow and returns where the
// module said to go, with it.
func (s *ConsoleServer) githubNext(w http.ResponseWriter, r *http.Request, c consolewire.Continued) bool {
	state, err := s.state.IssueAs(access.Binding{Bind: c.Bind, Actor: c.Actor, Owner: c.Owner})
	if err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App was created; start Connect again to install it.", err.Error(), nil)
		return false
	}
	access.SetCookie(w, access.ConnectCookie(state, s.sessions.Secure(), githubFlowWindow))
	http.Redirect(w, r, withState(c.URL, state), http.StatusFound)
	return true
}

// withState is a URL the module returned with the signed state in place of its
// placeholder.
func withState(u, state string) string {
	return strings.ReplaceAll(u, consolewire.StatePlaceholder, url.QueryEscape(state))
}

// githubCallback is where GitHub sends the owner after Create, with a
// one-time code for the App's key.
func (s *ConsoleServer) githubCallback(w http.ResponseWriter, r *http.Request) {
	flow, caller, ok := s.githubVerified(w, r)
	if !ok {
		return
	}
	next, ok := githubForward[consolewire.Continued](s, w, r, consolewire.CompleteCreate, flow, caller)
	if !ok {
		return
	}
	s.githubNext(w, r, next)
}

// githubSetup is where GitHub sends the owner after Install.
func (s *ConsoleServer) githubSetup(w http.ResponseWriter, r *http.Request) {
	flow, caller, ok := s.githubVerified(w, r)
	if !ok {
		return
	}
	// The flow ends here whichever way it goes.
	access.SetCookie(w, access.ConnectCookie("", s.sessions.Secure(), 0))
	if _, ok = githubForward[consolewire.Done](s, w, r, consolewire.CompleteInstall, flow, caller); !ok {
		return
	}
	http.Redirect(w, r, s.at("/#/github"), http.StatusFound)
}

// githubProblem is the page a GitHub redirect lands on when it cannot
// finish: a 4xx, never a 5xx, which a CDN in front would replace with a
// page of its own and lose GitHub's words.
func (s *ConsoleServer) githubProblem(w http.ResponseWriter, r *http.Request, code int, summary, detail string, causes []string) {
	var body strings.Builder
	body.WriteString(`<h1>The GitHub organisation could not be connected</h1>`)
	fmt.Fprintf(&body, `<p>%s</p>`, html.EscapeString(summary))
	if detail != "" {
		fmt.Fprintf(&body, `<p class="note">What GitHub said:</p><pre>%s</pre>`, html.EscapeString(detail))
	}
	if len(causes) > 0 {
		body.WriteString(`<p class="note">The usual causes, most common first:</p><ul>`)
		for _, cause := range causes {
			fmt.Fprintf(&body, `<li>%s</li>`, html.EscapeString(cause))
		}
		body.WriteString(`</ul>`)
	}
	body.WriteString(`<p><a class="btn" href="` + s.at("/#/github") + `">Back to the console</a></p>`)
	s.writePage(w, r, code, "GitHub", body.String())
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// githubCatalogueCallback is where GitHub sends the owner after creating a
// catalogue App, with a one-time code for its key.
func (s *ConsoleServer) githubCatalogueCallback(w http.ResponseWriter, r *http.Request) {
	s.githubCreated(w, r, consolewire.CompleteCatalogueCreate)
}

// githubCatalogueSetup is where GitHub sends the owner after installing a
// catalogue App.
func (s *ConsoleServer) githubCatalogueSetup(w http.ResponseWriter, r *http.Request) {
	s.githubInstalled(w, r, consolewire.CompleteCatalogueSetup, "/#/github/apps/catalogue")
}

// githubRunnerCallback is where GitHub sends the owner after creating a runner
// App, with a one-time code for its key.
func (s *ConsoleServer) githubRunnerCallback(w http.ResponseWriter, r *http.Request) {
	s.githubCreated(w, r, consolewire.CompleteRunnerCreate)
}

// githubRunnerSetup is where GitHub sends the owner after installing a runner
// App.
func (s *ConsoleServer) githubRunnerSetup(w http.ResponseWriter, r *http.Request) {
	s.githubInstalled(w, r, consolewire.CompleteRunnerSetup, "/#/github/apps")
}

// githubCreated finishes the first redirect of a flow in the module, which
// keeps the key, and sends the owner on to Install under a fresh state.
func (s *ConsoleServer) githubCreated(w http.ResponseWriter, r *http.Request, method string) {
	flow, caller, ok := s.githubVerified(w, r)
	if !ok {
		return
	}
	next, ok := githubForward[consolewire.Continued](s, w, r, method, flow, caller)
	if !ok {
		return
	}
	s.githubNext(w, r, next)
}

// githubInstalled finishes the last redirect of a flow in the module, which
// records where GitHub says the App is installed.
func (s *ConsoleServer) githubInstalled(w http.ResponseWriter, r *http.Request, method, landing string) {
	flow, caller, ok := s.githubVerified(w, r)
	if !ok {
		return
	}
	// The flow ends here whichever way it goes.
	access.SetCookie(w, access.ConnectCookie("", s.sessions.Secure(), 0))
	if _, ok = githubForward[consolewire.Done](s, w, r, method, flow, caller); !ok {
		return
	}
	http.Redirect(w, r, s.at(landing), http.StatusFound)
}

// The link flows' windows and limits. The page and its callback have no session:
// anyone with a browser reaches them, so each address gets a small allowance,
// and the module has a bound of its own on the links it finishes.
const (
	githubLinkWindow  = 15 * time.Minute
	linkAllowance     = 20
	linkAllowanceSpan = time.Minute
)

// linkLimits counts, per client address, the requests the link routes took in
// the current window.
type linkLimits struct {
	mu     sync.Mutex
	window time.Time
	seen   map[string]int
}

// allow reports whether the address may make another request in this window.
func (l *linkLimits) allow(now time.Time, address string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil || now.Sub(l.window) >= linkAllowanceSpan {
		l.window, l.seen = now, map[string]int{}
	}
	if l.seen[address] >= linkAllowance {
		return false
	}
	l.seen[address]++
	return true
}

// linkAllowed refuses a link request over its address's allowance with a 429
// page, and logs it.
func (s *ConsoleServer) linkAllowed(w http.ResponseWriter, r *http.Request) bool {
	address := clientAddress(r.Header, r.RemoteAddr, s.forwardedForTrustedHops)
	if s.linkLimits.allow(time.Now(), address) {
		return true
	}
	s.log.WarnContext(r.Context(), "a link request was refused: too many from one address", logattr.SafeString("remote", address))
	s.linkProblem(w, r, http.StatusTooManyRequests, "Too many tries from this address. Wait a minute and start again.", "")
	return false
}

// githubLinkAppCallback is where GitHub sends the owner after creating the link
// App. There is nothing to install: the App is used by being authorized, never
// by being installed.
func (s *ConsoleServer) githubLinkAppCallback(w http.ResponseWriter, r *http.Request) {
	flow, caller, ok := s.githubVerified(w, r)
	if !ok {
		return
	}
	access.SetCookie(w, access.ConnectCookie("", s.sessions.Secure(), 0))
	if _, ok = githubForward[consolewire.Done](s, w, r, consolewire.CompleteLinkApp, flow, caller); !ok {
		return
	}
	http.Redirect(w, r, s.at("/#/github"), http.StatusFound)
}

// githubLinkPage is where a person starts linking: what linking does, and the
// button that sends them to GitHub.
func (s *ConsoleServer) githubLinkPage(w http.ResponseWriter, r *http.Request) {
	if !s.linkAllowed(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), githubCallTimeout)
	defer cancel()
	page, err := consolecall.ProxyJSON[struct{}, consolewire.LinkPage](ctx, s.console.githubModule(), consolewire.Module,
		consolewire.LinkPageOf, consolecall.Caller{Origin: s.console.githubRoot()}, struct{}{})
	if err != nil {
		s.linkFailed(w, r, err)
		return
	}
	state, err := s.state.IssueAs(access.Binding{Bind: consolewire.BindLink})
	if err != nil {
		s.linkProblem(w, r, http.StatusConflict, "Linking could not be started. Reload the page.", err.Error())
		return
	}
	access.SetCookie(w, access.LinkCookie(state, s.sessions.Secure(), githubLinkWindow))
	authorize := withState(page.AuthorizeURL, state)
	body := `<h1>Link your GitHub account</h1>` +
		`<p>Your GitHub account is added to your organisations and teams by the work address you verified on it. ` +
		`GitHub will ask you to authorize the link: it lets this service read the email addresses on your account, and nothing else.</p>` +
		`<p class="note">Before you continue, add your work address on <a href="https://github.com/settings/emails">GitHub's email settings</a> ` +
		`and verify it. A personal address counts for nothing here.</p>` +
		`<p class="note">The link is checked again every few minutes. If you remove your work address from the account, ` +
		`or revoke the authorization on GitHub, the account leaves the organisations at once.</p>` +
		`<p><a class="btn" href="` + html.EscapeString(authorize) + `">Continue to GitHub</a></p>`
	s.writePage(w, r, http.StatusOK, "Link your GitHub account", body)
}

// githubLinkCallback finishes a link: the state and cookie are checked here,
// GitHub's word on who the account is and the directory's word on its
// addresses in the module.
func (s *ConsoleServer) githubLinkCallback(w http.ResponseWriter, r *http.Request) {
	if !s.linkAllowed(w, r) {
		return
	}
	state := r.URL.Query().Get("state")
	cookie, err := access.ReadCookie(r, access.LinkCookieName, s.sessions.Secure())
	if err != nil || cookie.Value == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		s.linkProblem(w, r, http.StatusBadRequest,
			"This link did not start in this browser, or took longer than fifteen minutes. Start again.", "")
		return
	}
	if binding, err := s.state.VerifyBinding(state); err != nil || binding.Bind != consolewire.BindLink {
		s.linkProblem(w, r, http.StatusBadRequest, "This link cannot be finished. Start again.", errString(err))
		return
	}
	access.SetCookie(w, access.LinkCookie("", s.sessions.Secure(), 0))
	q := r.URL.Query()
	ctx, cancel := context.WithTimeout(r.Context(), githubFlowDeadline)
	defer cancel()
	done, err := consolecall.ProxyJSON[consolewire.LinkComplete, consolewire.Linked](ctx, s.console.githubModule(), consolewire.Module,
		consolewire.LinkCompleteOf, consolecall.Caller{Origin: s.console.githubRoot()}, consolewire.LinkComplete{
			Code: q.Get("code"), GitHubError: q.Get("error"), GitHubErrorDescription: q.Get("error_description"),
		})
	if err != nil {
		s.linkFailed(w, r, err)
		return
	}

	if len(done.Accepted) == 0 {
		var body strings.Builder
		fmt.Fprintf(&body, `<h1>@%s could not be linked</h1>`, html.EscapeString(done.Login))
		if len(done.Refused) == 0 {
			body.WriteString(`<p>None of the addresses GitHub verified on this account is a work address this service knows.</p>`)
		} else {
			body.WriteString(`<p>No verified address on this account could be linked:</p><ul>`)
			for _, reason := range done.Refused {
				fmt.Fprintf(&body, `<li>%s</li>`, html.EscapeString(reason))
			}
			body.WriteString(`</ul>`)
		}
		body.WriteString(`<p class="note">Add your work address on <a href="https://github.com/settings/emails">GitHub's email settings</a>, ` +
			`verify it, and <a href="` + html.EscapeString(s.console.githubRoot()+consolewire.LinkPath) + `">link again</a>.</p>`)
		s.writePage(w, r, http.StatusConflict, "GitHub account not linked", body.String())
		return
	}
	var body strings.Builder
	fmt.Fprintf(&body, `<h1>@%s is linked</h1><p>Linked to:</p><ul>`, html.EscapeString(done.Login))
	for _, email := range done.Accepted {
		fmt.Fprintf(&body, `<li>%s</li>`, html.EscapeString(email))
	}
	body.WriteString(`</ul><p>Within a few minutes the account is invited to the organisations your teams are in, ` +
		`or added to those teams. Accept GitHub's invitation when it arrives.</p>`)
	if len(done.Refused) > 0 {
		body.WriteString(`<p class="note">Not linked:</p><ul>`)
		for _, reason := range done.Refused {
			fmt.Fprintf(&body, `<li class="note">%s</li>`, html.EscapeString(reason))
		}
		body.WriteString(`</ul>`)
	}
	body.WriteString(`<p class="note">If you remove a linked address from this account, or revoke the authorization on GitHub, ` +
		`the account leaves the organisations at once.</p>`)
	s.writePage(w, r, http.StatusOK, "GitHub account linked", body.String())
}

// linkFailed is the page for a call to the module that did not succeed: its
// refusal if it told one, else fixed words and the reason in the log.
func (s *ConsoleServer) linkFailed(w http.ResponseWriter, r *http.Request, err error) {
	var p *consolecall.Problem
	if errors.As(err, &p) {
		status := p.Status
		if status == 0 {
			status = http.StatusConflict
		}
		s.linkProblem(w, r, status, p.Summary, p.Detail)
		return
	}
	s.log.WarnContext(r.Context(), "a link could not be finished by the module", logattr.SafeError("error", err))
	s.linkProblem(w, r, http.StatusConflict, refusal.Text(r.Context(), refusal.ServerError)+": linking is not available just now. Try again in a minute.", "")
}

// linkProblem is the page a link lands on when it cannot finish. A 4xx, for the
// reason githubProblem is.
func (s *ConsoleServer) linkProblem(w http.ResponseWriter, r *http.Request, code int, summary, detail string) {
	var body strings.Builder
	body.WriteString(`<h1>The GitHub account could not be linked</h1>`)
	fmt.Fprintf(&body, `<p>%s</p>`, html.EscapeString(summary))
	if detail != "" {
		fmt.Fprintf(&body, `<p class="note">What went wrong:</p><pre>%s</pre>`, html.EscapeString(detail))
	}
	body.WriteString(`<p><a class="btn" href="` + html.EscapeString(s.console.githubRoot()+consolewire.LinkPath) + `">Start again</a></p>`)
	s.writePage(w, r, code, "GitHub", body.String())
}
