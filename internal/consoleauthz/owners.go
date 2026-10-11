package consoleauthz

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
)

// Directory is one connected directory as an owner is chosen and shown. The
// console tells a module the directories it can see ([consolecall.DirectoryFacts]),
// because a module reads no directory snapshot of its own.
type Directory = consolecall.Directory

// Actor is an identity as the trail names it: a workload by its service
// account, a recovery by the identity it completed as, and a person by their
// address.
func Actor(id access.Identity) audit.Actor {
	switch {
	case id.Source == access.SourceWorkload:
		return audit.Workload(id.Subject)
	case id.Source == access.SourceRecovery:
		return audit.RecoveryIdentity(id.Who())
	case id.Email != "":
		return audit.Person(id.Email)
	default:
		return audit.Identified(id.Who())
	}
}

// ActorOf is the caller snapshot as the trail names it, as [Actor].
func ActorOf(c consolecall.Caller) audit.Actor {
	if c.Actor == "" {
		return audit.Anonymous()
	}
	return Actor(c.Identity())
}

func badRequest(format string, args ...any) error {
	return modcall.Coded(modcall.CodeBadRequest, fmt.Sprintf(format, args...))
}

// MayOwned reports whether an identity holds a role over a thing owned by
// owner: the installation-wide role always does, and the same role held over
// the owning directory. With no owner only the installation-wide role does.
func MayOwned(id access.Identity, want access.Role, owner string) bool {
	if owner != "" {
		return id.CanFor(want, owner)
	}
	return id.Can(want)
}

// OwnerDomains maps a directory id to every domain it is authoritative for,
// sorted and joined with ", ": the one text a row names its owner by.
func OwnerDomains(dirs []Directory) map[string]string {
	out := make(map[string]string, len(dirs))
	for i := range dirs {
		out[dirs[i].ID] = strings.Join(dirs[i].Authoritative, ", ")
	}
	return out
}

// OwnerChoices are the directories the caller may name as an owner when it
// connects something, and whether it may name none. The installation-wide
// operator may name any connected directory, or none; a scoped operator only
// the connected directories it operates.
func OwnerChoices(id access.Identity, dirs []Directory) (refs []*directoryrosterv1.DirectoryRef, mayNone bool) {
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

// ResolveOwner is THE connect-time rule: who the owner of a thing about to be
// connected is, given who is connecting it and what they asked for.
//
//   - The installation-wide operator chooses: any connected directory, or none
//     (an empty request).
//   - An operator scoped to exactly one connected directory is that
//     directory's operator for this: the owner is it, whether they name it or
//     leave the choice empty.
//   - An operator scoped to several chooses among them, and must.
//   - Anything else is refused: a directory the caller does not operate, or one
//     that is not connected, is never recorded.
func ResolveOwner(id access.Identity, requested string, dirs []Directory) (string, error) {
	connected := make([]string, 0, len(dirs))
	for i := range dirs {
		connected = append(connected, dirs[i].ID)
	}
	requested = strings.TrimSpace(requested)
	if id.Can(access.RoleOperator) {
		if requested != "" && !slices.Contains(connected, requested) {
			return "", badRequest("%q is not a connected directory, so it cannot own anything", requested)
		}
		return requested, nil
	}
	mine := slices.DeleteFunc(slices.Clone(id.Workspaces(access.RoleOperator)), func(w string) bool { return !slices.Contains(connected, w) })
	switch {
	case requested != "":
		if !slices.Contains(mine, requested) {
			return "", denied("this needs the operator role over %s's directory, which names it as the owner", requested)
		}
		return requested, nil
	case len(mine) == 1:
		return mine[0], nil
	case len(mine) == 0:
		return "", denied("this needs the installation-wide operator role, or the operator role over a connected directory that would own it")
	default:
		return "", badRequest("you operate more than one directory: choose which of them owns it (%s)", strings.Join(mine, ", "))
	}
}

// RequireOwnerChange is the gate on changing a recorded owner: the
// installation-wide operator alone, and only to a connected directory or to
// none. dirs reads the connected directories, and is asked only for an owner
// that is not empty.
func RequireOwnerChange(c consolecall.Caller, owner string, dirs func() ([]Directory, error)) (access.Identity, error) {
	id, err := RequireRole(c, access.RoleOperator)
	var coded *modcall.Error
	if errors.As(err, &coded) && coded.Code == modcall.CodeForbidden {
		return id, denied("only the installation-wide operator changes an owner")
	}
	if err != nil {
		return id, err
	}
	if owner == "" {
		return id, nil
	}
	all, err := dirs()
	if err != nil {
		return id, err
	}
	for i := range all {
		if all[i].ID == owner {
			return id, nil
		}
	}
	return id, badRequest("%q is not a connected directory, so it cannot own anything", owner)
}
