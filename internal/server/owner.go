package server

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/ownerrule"
)

// An OWNER is the connected directory a Slack workspace or a GitHub
// organisation belongs to. It is recorded in the connection's own record
// when the connection is made, never declared in the policy: sluis
// already knows which directories are connected and which domains each
// serves, and a second copy in a file would only drift from the first.
//
// One rule decides who may connect something not yet connected and what
// that records, for Slack workspaces and GitHub organisations alike
// ([resolveOwner]); another decides who may change it afterwards
// ([Console.requireOwnerChange]).

// The rule itself is internal/ownerrule's, shared with the modules the
// console's operations moved into.
type connectedDirectory = ownerrule.Directory

var (
	mayOwned     = ownerrule.MayOwned
	ownerDomains = ownerrule.Domains
	ownerChoices = ownerrule.Choices
	resolveOwner = ownerrule.Resolve
)

// directories are every connected directory, sorted by id, with what each
// serves now.
func (c *Console) directories(ctx context.Context) ([]connectedDirectory, error) {
	if c.deps.Hub == nil {
		return nil, nil
	}
	out, err := ownerrule.FromHub(ctx, c.deps.Hub)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return out, nil
}

// requireOwnerChange is the gate on changing a recorded owner: the
// installation-wide operator alone, and only to a connected directory or to
// none.
func (c *Console) requireOwnerChange(ctx context.Context, owner string) (access.Identity, error) {
	id, err := requireRole(ctx, access.RoleOperator)
	if connect.CodeOf(err) == connect.CodePermissionDenied {
		return id, connect.NewError(connect.CodePermissionDenied,
			errors.New("only the installation-wide operator changes an owner"))
	}
	if err != nil {
		return id, err
	}
	if owner == "" {
		return id, nil
	}
	dirs, err := c.directories(ctx)
	if err != nil {
		return id, err
	}
	for i := range dirs {
		if dirs[i].ID == owner {
			return id, nil
		}
	}
	return id, connect.NewError(connect.CodeInvalidArgument,
		fmt.Errorf("%q is not a connected directory, so it cannot own anything", owner))
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
		if visible != nil && !slices.Contains(visible, dirs[i].ID) {
			continue
		}
		out.Directories = append(out.Directories, &directoryrosterv1.DirectoryDomains{
			WorkspaceId: dirs[i].ID, PrimaryDomain: dirs[i].Primary, Domains: slices.Clone(dirs[i].Served),
		})
	}
	return connect.NewResponse(out), nil
}
