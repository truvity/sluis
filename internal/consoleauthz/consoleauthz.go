// Package consoleauthz is the console's authorization rules for a module that
// runs a console operation on the caller snapshot ([consolecall.Caller]). They
// are the rules of the console's own gates (requireRole, requireWorkspace,
// requireOwner), word for word, so moving an operation into a module changes
// where the check runs and not what it allows.
//
// A refusal is a [modcall.Error] with [modcall.CodeForbidden] (or
// [CodeUnauthenticated] for a snapshot naming no one) and a message safe to
// show the person.
package consoleauthz

import (
	"fmt"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
)

// CodeUnauthenticated is the code of a snapshot with no actor.
const CodeUnauthenticated = "unauthenticated"

func who(c consolecall.Caller) (access.Identity, error) {
	if c.Actor == "" {
		return access.Identity{}, modcall.Coded(CodeUnauthenticated, "sign in first")
	}
	return c.Identity(), nil
}

func denied(format string, args ...any) error {
	return modcall.Coded(modcall.CodeForbidden, fmt.Sprintf(format, args...))
}

// RequireRole is the gate that needs the installation-wide role.
func RequireRole(c consolecall.Caller, want access.Role) (access.Identity, error) {
	id, err := who(c)
	if err != nil {
		return id, err
	}
	if !id.Can(want) {
		return access.Identity{}, denied("this needs the %s role", want)
	}
	return id, nil
}

// RequireWorkspace is RequireRole for something done to one workspace: it
// passes for the installation-wide role and for the role held over that
// workspace.
func RequireWorkspace(c consolecall.Caller, want access.Role, workspace string) (access.Identity, error) {
	id, err := who(c)
	if err != nil {
		return id, err
	}
	if !id.CanFor(want, workspace) {
		return access.Identity{}, denied("this needs the %s role over %s", want, workspace)
	}
	return id, nil
}

// RequireAnywhere is the gate on a page that lists workspaces and shows only
// the ones the caller may see.
func RequireAnywhere(c consolecall.Caller, want access.Role) (access.Identity, error) {
	id, err := who(c)
	if err != nil {
		return id, err
	}
	if !id.CanAnywhere(want) {
		return access.Identity{}, denied("this needs the %s role", want)
	}
	return id, nil
}

// RequireOwner is the gate on something done to a thing another party owns,
// by the owning directory's workspace id: owner empty, only the
// installation-wide role passes; owner set, that role passes and so does the
// role held over that directory. subject names the thing, so the refusal says
// whose directory the caller was missing.
func RequireOwner(c consolecall.Caller, want access.Role, owner, subject string) (access.Identity, error) {
	id, err := who(c)
	if err != nil {
		return id, err
	}
	if owner == "" {
		if !id.Can(want) {
			return access.Identity{}, denied("this needs the installation-wide %s role: %s names no owning directory", want, subject)
		}
		return id, nil
	}
	if !id.CanFor(want, owner) {
		return access.Identity{}, denied("this needs the %s role over %s's directory", want, subject)
	}
	return id, nil
}

// RequireOrg is RequireOwner for a GitHub organisation: owner is the
// directory workspace the organisation's record names, org its login.
func RequireOrg(c consolecall.Caller, want access.Role, owner, org string) (access.Identity, error) {
	return RequireOwner(c, want, owner, org)
}
