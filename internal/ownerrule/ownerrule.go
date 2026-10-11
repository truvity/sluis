// Package ownerrule is the rule for who owns a thing a console connects (a
// GitHub organisation, a Slack workspace) and who may operate it: one rule for
// the console and for the modules the console's operations moved into
// (v1.76 D-v176-1 b), so the two cannot drift.
//
// Its refusals are connect errors, as the console's handlers return them; the
// module hosting a handler turns them into a reply.
package ownerrule

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/emailaddr"
	"github.com/truvity/sluis/internal/hub"
)

// MayOwned reports whether an identity holds a role over a thing owned by
// owner: the installation-wide role always does, and the same role held over
// the owning directory. With no owner only the installation-wide role does.
func MayOwned(id access.Identity, want access.Role, owner string) bool {
	if owner != "" {
		return id.CanFor(want, owner)
	}
	return id.Can(want)
}

// Directory is one connected directory as an owner is chosen and shown.
type Directory = consolecall.Directory

// Domains maps a directory id to every domain it is authoritative for,
// sorted and joined with ", ": the one text a row names its owner by. The
// console words it with the directory's id beside it, the same way it words
// an owner choice.
func Domains(dirs []Directory) map[string]string {
	out := make(map[string]string, len(dirs))
	for i := range dirs {
		out[dirs[i].ID] = strings.Join(dirs[i].Authoritative, ", ")
	}
	return out
}

// Choices are the directories the caller may name as an owner when it
// connects something, and whether it may name none. The installation-wide
// operator may name any connected directory, or none; a scoped operator only
// the connected directories it operates.
func Choices(id access.Identity, dirs []Directory) (refs []*directoryrosterv1.DirectoryRef, mayNone bool) {
	scope := id.Workspaces(access.RoleOperator)
	mayNone = id.Can(access.RoleOperator)
	for i := range dirs {
		if !mayNone && !slices.Contains(scope, dirs[i].ID) {
			continue
		}
		refs = append(refs, &directoryrosterv1.DirectoryRef{WorkspaceId: dirs[i].ID, PrimaryDomain: dirs[i].Primary, Domains: slices.Clone(dirs[i].Authoritative)})
	}
	return refs, mayNone
}

// Resolve is THE connect-time rule: who the owner of a thing about to
// be connected is, given who is connecting it and what they asked for.
//
//   - The installation-wide operator chooses: any connected directory, or
//     none (an empty request).
//   - An operator scoped to exactly one connected directory is that
//     directory's operator for this: the owner is it, whether they name it
//     or leave the choice empty.
//   - An operator scoped to several chooses among them, and must.
//   - Anything else is refused: a directory the caller does not operate, or
//     one that is not connected, is never recorded.
func Resolve(id access.Identity, requested string, dirs []Directory) (string, error) {
	connected := make([]string, 0, len(dirs))
	for i := range dirs {
		connected = append(connected, dirs[i].ID)
	}
	requested = strings.TrimSpace(requested)
	if id.Can(access.RoleOperator) {
		if requested != "" && !slices.Contains(connected, requested) {
			return "", connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("%q is not a connected directory, so it cannot own anything", requested))
		}
		return requested, nil
	}
	mine := slices.DeleteFunc(slices.Clone(id.Workspaces(access.RoleOperator)), func(w string) bool { return !slices.Contains(connected, w) })
	switch {
	case requested != "":
		if !slices.Contains(mine, requested) {
			return "", connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("this needs the operator role over %s's directory, which names it as the owner", requested))
		}
		return requested, nil
	case len(mine) == 1:
		return mine[0], nil
	case len(mine) == 0:
		return "", connect.NewError(connect.CodePermissionDenied,
			errors.New("this needs the installation-wide operator role, or the operator role over a connected directory that would own it"))
	default:
		return "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("you operate more than one directory: choose which of them owns it (%s)", strings.Join(mine, ", ")))
	}
}

// FromHub is every connected directory of the hub, sorted by id, with what
// each serves now.
func FromHub(ctx context.Context, h *hub.Hub) ([]Directory, error) {
	views, err := h.WorkspaceViews(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Directory, 0, len(views))
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
		out = append(out, Directory{
			ID: views[i].Workspace.ID, Primary: primaryDomain(&views[i].Workspace, served), Served: served, Authoritative: authoritative,
		})
	}
	return out, nil
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
