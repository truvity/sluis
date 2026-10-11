package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/status"
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
	http.Redirect(w, r, githubWithState(c.URL, state), http.StatusFound)
	return true
}

// githubWithState is a URL the module returned with the signed state in place of its
// placeholder.
func githubWithState(u, state string) string {
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

// githubFlowFor checks what a flow that is still the console's must carry: the
// state this browser was given, signed by this service, naming a GitHub
// organisation and the operator who started the flow.
func (s *ConsoleServer) githubFlowFor(w http.ResponseWriter, r *http.Request, prefix string) (org, actor, owner string, ok bool) {
	bind, actor, owner, ok := s.githubBound(w, r)
	if !ok {
		return "", "", "", false
	}
	org, isGitHub := strings.CutPrefix(bind, prefix)
	if !isGitHub || !status.ValidOrg(org) {
		s.githubProblem(w, r, http.StatusBadRequest, "This is not a GitHub connect.", "", nil)
		return "", "", "", false
	}
	return org, actor, owner, true
}

// githubBound checks the state this browser was given, signed by this
// service, and returns what it binds, the operator who started the flow and
// the owner the flow was begun to record.
func (s *ConsoleServer) githubBound(w http.ResponseWriter, r *http.Request) (bind, actor, owner string, ok bool) {
	state := r.URL.Query().Get("state")
	cookie, err := access.ReadCookie(r, access.ConnectCookieName, s.sessions.Secure())
	if err != nil || cookie.Value == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		s.githubNotThisBrowser(w, r)
		return "", "", "", false
	}
	binding, err := s.state.VerifyBinding(state)
	if err != nil {
		s.githubProblem(w, r, http.StatusBadRequest, "This connect cannot be finished.", err.Error(), nil)
		return "", "", "", false
	}
	actor = binding.Actor
	// The state binds who began the flow, and that was checked then. It is
	// asked again here, about whoever is signed in NOW, by the same rule as
	// the call that began it: a flow begun by an operator of one company's
	// directory must not finish in another's organisation, nor after the
	// role was taken away.
	if id, signedIn := IdentityFrom(r.Context()); signedIn {
		bound, subject, ownerErr := s.console.ownerOfBind(r.Context(), binding)
		if ownerErr != nil {
			s.githubProblem(w, r, http.StatusConflict, "The connection records could not be read.", ownerErr.Error(), nil)
			return "", "", "", false
		}
		if _, err = requireOwner(r.Context(), access.RoleOperator, bound, subject); err != nil {
			s.githubProblem(w, r, http.StatusForbidden, err.Error()+".", "", nil)
			return "", "", "", false
		}
		actor = id.Who()
	}
	if actor == "" {
		s.githubProblem(w, r, http.StatusForbidden, "Connecting a GitHub organisation needs the operator role.", "", nil)
		return "", "", "", false
	}
	return binding.Bind, actor, binding.Owner, true
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
