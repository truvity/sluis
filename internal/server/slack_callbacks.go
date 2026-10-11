package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
	slackcatalogue "github.com/truvity/sluis/internal/slackapp/catalogue"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
	"github.com/truvity/sluis/internal/slackroster/status"
	"github.com/truvity/sluis/storage/logattr"
)

// slackFlow is which install a redirect belongs to, for the page a failure
// lands on.
type slackFlow struct{ name, back, heading string }

var (
	slackCatalogueFlow = slackFlow{name: "catalogue", back: "/#/slack-apps", heading: "The Slack App could not be installed"}
	slackWorkspaceFlow = slackFlow{name: "workspace", back: "/#/slack", heading: "The Slack workspace could not be connected"}
)

// slackRefusal ends a Slack callback that cannot finish: it logs why, records
// why, and answers with the page. reason is the short sentence the log and
// the audit record carry; it may hold Slack's own words, so the log takes it
// through logattr, and it never holds the code, a token or a secret.
type slackRefusal func(code int, summary, detail, reason string, causes []string)

// slackRefused is the log line every refused Slack callback leaves, whichever
// of its reasons, so that nobody has to guess why an install did nothing.
func (s *ConsoleServer) slackRefused(flow slackFlow, r *http.Request, actor, reason string) {
	s.log.WarnContext(r.Context(), "a Slack install callback was refused", slog.String("flow", flow.name),
		logattr.SafeString("reason", reason), logattr.SafeString("by", actor))
}

// slackBound checks a Slack flow's redirect the way GitHub's is checked: the
// cookie the flow was pinned with must equal the state, and the state must be
// one this service signed. It returns the state's bind and who began the flow,
// and whoever is signed in now, whose role the flow's workspace asks again (in
// the Slack module, which holds the records that say whose the workspace is).
// learn is told what the state names as soon as it is known, so a later refusal
// can say whose install it was.
func (s *ConsoleServer) slackBound(r *http.Request, refuse slackRefusal, learn func(bind, actor string)) (
	bind, actor string, now *access.Identity, ok bool,
) {
	state := r.URL.Query().Get("state")
	if state == "" {
		refuse(http.StatusBadRequest, "This install did not start in this browser.", "", "no state in the redirect", nil)
		return "", "", nil, false
	}
	binding, err := s.state.VerifyBinding(state)
	if err != nil {
		refuse(http.StatusBadRequest, "This install cannot be finished.", err.Error(), "the state is not valid: "+err.Error(), nil)
		return "", "", nil, false
	}
	learn(binding.Bind, binding.Actor)
	cookie, err := access.ReadCookie(r, access.ConnectCookieName, s.sessions.Secure())
	if err != nil || cookie.Value == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		refuse(http.StatusBadRequest, "This install did not start in this browser.", "", "the flow cookie is missing or is not this state's", []string{
			"It was started in another browser, profile or private window.",
			"More than ten minutes passed on Slack's page before returning.",
			"The browser is refusing the cookie this flow is pinned to.",
		})
		return "", "", nil, false
	}
	actor = binding.Actor
	// The state binds who began the flow, and that was checked then. It is
	// asked again here, about whoever is signed in NOW, by the same rule as
	// the call that began it: a flow begun by an operator of one company's
	// directory must not finish in another's workspace, nor after the role
	// was taken away.
	if id, signedIn := IdentityFrom(r.Context()); signedIn {
		now = &id
		actor = id.Who()
		learn(binding.Bind, actor)
	}
	return binding.Bind, actor, now, true
}

// slackProblem is the page a Slack redirect lands on when it cannot finish:
// a 4xx, never a 5xx, which a CDN in front would replace with a page of its
// own and lose Slack's words.
func (s *ConsoleServer) slackProblem(flow slackFlow, w http.ResponseWriter, r *http.Request, code int, summary, detail string, causes []string) {
	var body strings.Builder
	fmt.Fprintf(&body, `<h1>%s</h1>`, html.EscapeString(flow.heading))
	fmt.Fprintf(&body, `<p>%s</p>`, html.EscapeString(summary))
	if detail != "" {
		fmt.Fprintf(&body, `<p class="note">What Slack said:</p><pre>%s</pre>`, html.EscapeString(detail))
	}
	if len(causes) > 0 {
		body.WriteString(`<p class="note">The usual causes, most common first:</p><ul>`)
		for _, cause := range causes {
			fmt.Fprintf(&body, `<li>%s</li>`, html.EscapeString(cause))
		}
		body.WriteString(`</ul>`)
	}
	body.WriteString(`<p><a class="btn" href="` + s.at(flow.back) + `">Back to the console</a></p>`)
	s.writePage(w, r, code, "Slack", body.String())
}

// slackComplete forwards a verified redirect to the module and says what became
// of it. The call is never repeated: Slack's code is single use, so a lost
// answer after the module spent it is a lost install, to be started again, as it
// always was. A module that cannot be reached, or fails, is a refusal on the
// page, with the words that say so.
func (s *ConsoleServer) slackComplete(
	r *http.Request, method string, bind, actor string, now *access.Identity, denied string,
) (slackconsole.Completion, *slackconsole.Refusal) {
	caller := consolecall.Caller{Actor: actor}
	if now != nil {
		caller = consolecall.Snapshot(*now)
	}
	caller.Origin = s.console.githubRoot()
	req := slackconsole.CompleteRequest{
		Bind: bind, Code: r.URL.Query().Get("code"), Denied: denied, Recheck: now != nil,
	}
	ctx, cancel := context.WithTimeout(modcall.WithoutRetry(r.Context()), slackCompleteTimeout)
	defer cancel()
	res, err := consolecall.ProxyJSON[slackconsole.CompleteRequest, slackconsole.Completion](
		ctx, s.console.slackModule(), slackconsole.Module, method, caller, req)
	if err != nil {
		reason := "the Slack module did not answer: " + logattr.Error(err)
		return slackconsole.Completion{}, &slackconsole.Refusal{
			Status:  http.StatusConflict,
			Summary: "The Slack module did not answer, so it is not known whether the install finished.",
			Reason:  reason,
			Causes: []string{
				"Check the Slack page: if the connection shows as installed, nothing more is needed.",
				"Otherwise start the install again: the code Slack sent can be used once, and this service never repeats the call.",
			},
		}
	}
	return res, res.Refused
}

// slackWorkspaceCallback is where Slack sends the owner after installing the
// roster's App, with a one-time code for the bot token. The console checks the
// state and the cookie; the module exchanges the code and keeps the token.
func (s *ConsoleServer) slackWorkspaceCallback(w http.ResponseWriter, r *http.Request) {
	flow := slackWorkspaceFlow
	console := s.console
	subject := audit.SlackWorkspace{Key: "unknown", App: "unknown"}
	actor := ""
	refuse := func(code int, summary, detail, reason string, causes []string) {
		s.slackRefused(flow, r, actor, reason)
		console.record(r.Context(), audit.SlackWorkspaceConnectRefused(audit.Identified(actor), subject, reason))
		s.slackProblem(flow, w, r, code, summary, detail, causes)
	}
	learn := func(bind, who string) {
		actor = who
		if key, ok := strings.CutPrefix(bind, slackconsole.WorkspaceBind); ok && status.ValidWorkspace(key) {
			subject.Key = key
		}
	}
	bind, actor, now, ok := s.slackBound(r, refuse, learn)
	if !ok {
		return
	}
	// The flow ends here, whichever way it goes.
	access.SetCookie(w, access.ConnectCookie("", s.sessions.Secure(), 0))
	_, refusal := s.slackComplete(r, slackconsole.MethodCompleteWorkspace, bind, actor, now, r.URL.Query().Get("error"))
	if refusal != nil {
		// The module recorded what it refused; one that did not answer left
		// nothing to record but this.
		s.slackRefused(flow, r, actor, refusal.Reason)
		s.slackProblem(flow, w, r, refusal.Status, refusal.Summary, refusal.Detail, refusal.Causes)
		return
	}
	http.Redirect(w, r, s.at("/#/slack"), http.StatusFound)
}

// slackCatalogueCallback is where Slack sends the owner after installing a
// catalogue App, with a one-time code for the bot token. The console checks the
// state and the cookie; the module exchanges the code and keeps the token, only
// if it belongs to the workspace the policy names for the App.
func (s *ConsoleServer) slackCatalogueCallback(w http.ResponseWriter, r *http.Request) {
	flow := slackCatalogueFlow
	console := s.console
	app := audit.SlackCatalogueApp{ID: "unknown", App: "unknown", Workspace: "unknown"}
	actor := ""
	refuse := func(code int, summary, detail, reason string, causes []string) {
		s.slackRefused(flow, r, actor, reason)
		console.record(r.Context(), audit.SlackCatalogueAppInstallRefused(audit.Identified(actor), app, reason))
		s.slackProblem(flow, w, r, code, summary, detail, causes)
	}
	learn := func(bind, who string) {
		actor = who
		if id, ok := strings.CutPrefix(bind, slackconsole.CatalogueBind); ok && slackcatalogue.ValidID(id) {
			app.ID = id
		}
	}
	bind, actor, now, ok := s.slackBound(r, refuse, learn)
	if !ok {
		return
	}
	// The flow ends here, whichever way it goes.
	access.SetCookie(w, access.ConnectCookie("", s.sessions.Secure(), 0))
	_, refusal := s.slackComplete(r, slackconsole.MethodCompleteCatalogue, bind, actor, now, r.URL.Query().Get("error"))
	if refusal != nil {
		s.slackRefused(flow, r, actor, refusal.Reason)
		s.slackProblem(flow, w, r, refusal.Status, refusal.Summary, refusal.Detail, refusal.Causes)
		return
	}
	http.Redirect(w, r, s.at("/#/slack-apps"), http.StatusFound)
}
