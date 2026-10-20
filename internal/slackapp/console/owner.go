package console

import (
	"context"
	"maps"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/slackroster/connection"
)

// Book is every connected Slack workspace's record by key. What the policy
// leaves out of a workspace — which team it is, which directory owns it — is
// read here, from what connecting it recorded.
type Book map[string]connection.Record

// book reads the records. A deployment that keeps none has an empty book:
// nothing is connected, so nothing has an owner.
func (c *Service) book(ctx context.Context) (Book, error) {
	book := Book{}
	if c.d().SlackWorkspaces == nil {
		return book, nil
	}
	records, err := c.d().SlackWorkspaces.List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	for i := range records {
		book[records[i].Workspace] = records[i]
	}
	return book, nil
}

// Owner is the directory workspace recorded as a workspace's owner, "" when it
// has none or is not connected.
func (b Book) Owner(workspace string) string { return b[workspace].Owner }

// Team is the Slack team id recorded at the workspace's first install, ""
// before it.
func (b Book) Team(workspace string) string { return b[workspace].TeamID }

// May reports whether an identity holds a role over one Slack workspace: by
// the recorded owner once it is connected. A workspace nobody has connected
// has no owner to be a viewer or an operator of; the one thing to do with it is
// connect it, so whoever could ([consoleauthz.ResolveOwner]) is let in.
func (b Book) May(id access.Identity, want access.Role, workspace string) bool {
	if record, connected := b[workspace]; connected {
		return consoleauthz.MayOwned(id, want, record.Owner)
	}
	return id.Can(want) || id.CanAnywhere(access.RoleOperator)
}

// MayAct is whether the caller passes [Service.RequireSlack] for the
// workspace: the operator of its recorded owner, or installation-wide, which is
// all anyone but the installation-wide operator can be of a workspace that has
// no owner.
func (b Book) MayAct(id access.Identity, workspace string) bool {
	return consoleauthz.MayOwned(id, access.RoleOperator, b.Owner(workspace))
}

// RequireSlack is [consoleauthz.RequireOwner] for one Slack workspace: the
// operator of its recorded owning directory, or the installation-wide role.
func (c *Service) RequireSlack(ctx context.Context, caller consolecall.Caller, want access.Role, workspace string) (access.Identity, error) {
	return c.requireSlack(ctx, caller, want, workspace)
}

func (c *Service) requireSlack(ctx context.Context, caller consolecall.Caller, want access.Role, workspace string) (access.Identity, error) {
	book, err := c.book(ctx)
	if err != nil {
		return access.Identity{}, err
	}
	return consoleauthz.RequireOwner(caller, want, book.Owner(workspace), workspace)
}

// RequireAnySlack is the gate on a page that lists Slack workspaces and then
// shows only the ones the caller may see: the installation-wide role, or the
// role over the directory that owns at least one Slack workspace, or the
// operator role over any directory when a declared workspace is still to be
// connected (the caller may be the one to connect it). A scoped role over a
// directory that owns none has nothing here to see, and is refused rather than
// shown an empty page that looks like an answer.
func (c *Service) RequireAnySlack(ctx context.Context, caller consolecall.Caller, want access.Role) (access.Identity, Book, error) {
	return c.requireAnySlack(ctx, caller, want)
}

func (c *Service) requireAnySlack(ctx context.Context, caller consolecall.Caller, want access.Role) (access.Identity, Book, error) {
	id, err := consoleauthz.RequireAnywhere(caller, want)
	if err != nil {
		return id, nil, err
	}
	book, err := c.book(ctx)
	if err != nil {
		return id, nil, err
	}
	if id.Can(want) {
		return id, book, nil
	}
	for _, workspace := range c.slackKeys(book) {
		if book.May(id, want, workspace) {
			return id, book, nil
		}
	}
	return access.Identity{}, nil, connect.Errorf(connect.CodePermissionDenied,
		"this needs the %s role, installation-wide or over a directory that owns a Slack workspace", want)
}

// slackKeys are every workspace the policy declares or a record exists for.
func (c *Service) slackKeys(book Book) []string {
	seen := map[string]bool{}
	for _, key := range c.d().Authorizer.Policy().SlackWorkspaceKeys() {
		seen[key] = true
	}
	for key := range book {
		seen[key] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// workspaceOfBind is the Slack workspace a signed state binds, so a callback
// can ask the role question again, now: the workspace itself for a workspace
// connect, the catalogue entry's for a catalogue App.
func (c *Service) workspaceOfBind(bind string) string {
	if workspace, ok := strings.CutPrefix(bind, WorkspaceBind); ok {
		return workspace
	}
	id, ok := strings.CutPrefix(bind, CatalogueBind)
	if !ok || c.d().SlackCatalogue == nil {
		return ""
	}
	entry, _ := c.d().SlackCatalogue.Get(id)
	return entry.Workspace
}

// Book is the connected workspaces' records.
func (c *Service) Book(ctx context.Context) (Book, error) { return c.book(ctx) }
