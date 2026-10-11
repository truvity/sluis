package console

import (
	"net/http"
	"strings"

	"context"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/status"
)

// What a provider's redirect finishes. The issuer verified the signed state and
// the browser's cookie before it called; what it could not check is the part
// that needs the records, and that is asked here, against the caller snapshot
// of whoever is signed in NOW: a flow begun by an operator of one company's
// directory must not finish in another's organisation, nor after the role was
// taken away.

// A flow's refusal is a page, so its Problem carries the HTTP status of the
// page: a 4xx, never a 5xx, which a CDN in front would replace with a page of
// its own and lose the provider's words.
func flowProblem(code int, summary, detail string, hints ...string) *consolecall.Problem {
	return &consolecall.Problem{Summary: summary, Detail: detail, Hints: hints, Status: code}
}

// flowOrg is the organisation a flow's state names behind prefix.
func flowOrg(f consolewire.Flow, prefix string) (string, error) {
	org, isGitHub := strings.CutPrefix(f.Bind, prefix)
	if !isGitHub || !status.ValidOrg(org) {
		return "", flowProblem(http.StatusBadRequest, "This is not a GitHub connect.", "")
	}
	return org, nil
}

// flowActor is who finishes the flow: whoever is signed in now, when the owner
// rule still lets them, else whom the state names.
func (c *Service) flowActor(ctx context.Context, f consolewire.Flow) (string, error) {
	actor := f.Actor
	if id, signedIn := identityFrom(ctx); signedIn {
		bound, subject, err := c.ownerOfBind(ctx, access.Binding{Bind: f.Bind, Actor: f.Actor, Owner: f.Owner})
		if err != nil {
			return "", flowProblem(http.StatusConflict, "The connection records could not be read.", err.Error())
		}
		if _, err = requireOwner(ctx, access.RoleOperator, bound, subject); err != nil {
			return "", flowProblem(http.StatusForbidden, err.Error()+".", "")
		}
		actor = id.Who()
	}
	if actor == "" {
		return "", flowProblem(http.StatusForbidden, "Connecting a GitHub organisation needs the operator role.", "")
	}
	return actor, nil
}
