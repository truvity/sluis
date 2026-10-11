package console

import (
	"context"
	"net/http"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
)

// CompleteRequest is what the console forwards of Slack's redirect, once it has
// checked the signed state and the cookie the flow was pinned with. The code is
// single use: a call that spends it is never repeated, so a lost answer is a
// lost install, to be started again.
type CompleteRequest struct {
	// Bind is what the verified state binds: the workspace's or the App's key,
	// with the flow's prefix.
	Bind string `json:"bind"`
	// Code is Slack's one-time code for the bot token.
	Code string `json:"code,omitempty"`
	// Denied is the `error` Slack sent instead of a code, when the install was
	// not approved.
	Denied string `json:"denied,omitempty"`
	// Recheck says the caller is the person signed in now, whose role is asked
	// again by the rule of the call that began the flow. A flow finished by
	// someone who is not signed in here rests on the state alone.
	Recheck bool `json:"recheck,omitempty"`
}

// Completion is what became of a redirect: nothing to say when it finished, or
// the refusal the console shows on the page the browser lands on.
type Completion struct {
	Refused *Refusal `json:"refused,omitempty"`
}

// Refusal is a redirect that could not finish. It is told as data, since the
// words are the ones the person reads on the page.
type Refusal struct {
	// Status is the HTTP status of the page: a 4xx, never a 5xx, which a CDN in
	// front would replace with a page of its own.
	Status  int    `json:"status"`
	Summary string `json:"summary"`
	// Detail is what Slack said.
	Detail string `json:"detail,omitempty"`
	// Reason is the short sentence the log and the audit record carry.
	Reason string   `json:"reason"`
	Causes []string `json:"causes,omitempty"`
}

func refused(status int, summary, detail, reason string, causes []string) Completion {
	return Completion{Refused: &Refusal{Status: status, Summary: summary, Detail: detail, Reason: reason, Causes: causes}}
}

// recheck asks the role question of a flow's workspace again, as the call that
// began it did. It says why not, as a refusal.
func (c *Service) recheck(ctx context.Context, caller consolecall.Caller, req CompleteRequest) *Refusal {
	if !req.Recheck {
		return nil
	}
	if _, err := c.requireSlack(ctx, caller, access.RoleOperator, c.workspaceOfBind(req.Bind)); err != nil {
		return refused(http.StatusForbidden, said(err)+".", "", "the signed-in operator may not operate the workspace: "+said(err), nil).Refused
	}
	return nil
}

// words are the usual causes of a code Slack would not exchange.
var exchangeCauses = []string{
	"The page was reloaded: the code Slack returns can be exchanged once.",
	"More than ten minutes passed between approving and returning here.",
	"This service cannot reach slack.com: the cluster's egress policy has to allow it.",
}
