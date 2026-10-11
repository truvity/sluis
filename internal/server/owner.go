package server

import (
	"context"
	"slices"
	"strings"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/emailaddr"
	"github.com/truvity/sluis/internal/hub"
)

// An OWNER is the connected directory a Slack workspace or a GitHub
// organisation belongs to. It is recorded in the connection's own record
// when the connection is made, never declared in the policy: sluis
// already knows which directories are connected and which domains each
// serves, and a second copy in a file would only drift from the first.
//
// The rules for who may connect something not yet connected, what that
// records, and who may change it afterwards are internal/consoleauthz's, applied
// in each provider's module; the console only lists the directories.

// connectedDirectory is one connected directory as an owner is chosen and shown.
type connectedDirectory struct {
	id      string
	primary string
	served  []string
	// authoritative is the served domains the directory answers for with
	// confidence, sorted: what an owner is labelled with.
	authoritative []string
}

// primaryDomain is the domain people know a directory by: its admin
// account's domain when the directory serves it, else the first domain it
// serves, else its id.
func primaryDomain(ws *hub.Workspace, served []string) string {
	if domain, ok := emailaddr.Domain(strings.ToLower(ws.Admin)); ok && slices.Contains(served, domain) {
		return domain
	}
	if len(served) > 0 {
		return served[0]
	}
	return ws.ID
}

// directories are every connected directory, sorted by id, with what each
// serves now.
func (c *Console) directories(ctx context.Context) ([]connectedDirectory, error) {
	if c.deps.Hub == nil {
		return nil, nil
	}
	views, err := c.deps.Hub.WorkspaceViews(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	out := make([]connectedDirectory, 0, len(views))
	for i := range views {
		served := views[i].Workspace.Served()
		slices.Sort(served)
		var authoritative []string
		for _, d := range views[i].Domains {
			if d.Served && d.Authoritative {
				authoritative = append(authoritative, d.Name)
			}
		}
		slices.Sort(authoritative)
		out = append(out, connectedDirectory{
			id: views[i].Workspace.ID, primary: primaryDomain(&views[i].Workspace, served), served: served, authoritative: authoritative,
		})
	}
	return out, nil
}

// ownerDomains maps a directory id to every domain it is authoritative for,
// sorted and joined with ", ": the one text a row names its owner by. The
// console words it with the directory's id beside it, the same way it words
// an owner choice.
func ownerDomains(dirs []connectedDirectory) map[string]string {
	out := make(map[string]string, len(dirs))
	for i := range dirs {
		out[dirs[i].id] = strings.Join(dirs[i].authoritative, ", ")
	}
	return out
}

// ownerChoices are the directories the caller may name as an owner when it
// connects something, and whether it may name none. The installation-wide
// operator may name any connected directory, or none; a scoped operator only
// the connected directories it operates.
func ownerChoices(id access.Identity, dirs []connectedDirectory) (refs []*directoryrosterv1.DirectoryRef, mayNone bool) {
	scope := id.Workspaces(access.RoleOperator)
	mayNone = id.Can(access.RoleOperator)
	for i := range dirs {
		if !mayNone && !slices.Contains(scope, dirs[i].id) {
			continue
		}
		refs = append(refs, &directoryrosterv1.DirectoryRef{WorkspaceId: dirs[i].id, PrimaryDomain: dirs[i].primary, Domains: slices.Clone(dirs[i].authoritative)})
	}
	return refs, mayNone
}

// ListServedDomains implements the controllers' read: for each connected
// directory the caller may view, the domains the hub serves for it now.
func (c *Console) ListServedDomains(
	ctx context.Context, _ *connect.Request[directoryrosterv1.ListServedDomainsRequest],
) (*connect.Response[directoryrosterv1.ListServedDomainsResponse], error) {
	id, err := requireAnywhere(ctx, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	dirs, err := c.directories(ctx)
	if err != nil {
		return nil, err
	}
	// A nil list is every directory: an installation-wide role is not a list
	// of tenants and must not be turned into one.
	visible := id.Workspaces(access.RoleViewer)
	out := &directoryrosterv1.ListServedDomainsResponse{}
	for i := range dirs {
		if visible != nil && !slices.Contains(visible, dirs[i].id) {
			continue
		}
		out.Directories = append(out.Directories, &directoryrosterv1.DirectoryDomains{
			WorkspaceId: dirs[i].id, PrimaryDomain: dirs[i].primary, Domains: slices.Clone(dirs[i].served),
		})
	}
	return connect.NewResponse(out), nil
}
