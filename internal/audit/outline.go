package audit

import (
	"strings"

	"github.com/truvity/sluis/audit/sdk/record"
)

// The Outline controller's and the Outline console's records. A workspace is
// named by the policy's key, a group by its name, a collection as
// <workspace>/<name>, and an account by its Outline user id with the address
// as the record's subject.

func targetOutlineWorkspace(key string) *record.Target {
	return &record.Target{Type: "outline_workspace", Id: key}
}

func targetOutlineGroup(key, name string) *record.Target {
	return &record.Target{Type: "outline_group", Id: key + "/" + name}
}

func targetOutlineCollection(key, name string) *record.Target {
	return &record.Target{Type: "outline_collection", Id: key + "/" + name}
}

func targetOutlineUser(id string) *record.Target {
	return &record.Target{Type: "outline_user", Id: strings.TrimSpace(id)}
}

// OutlineWorkspace is a connected Outline workspace: its key, its URL, the
// address of the user its API key belongs to (the record's subject) and its
// Outline team.
type OutlineWorkspace struct {
	Key, URL, KeyUser, Team string
}

func (w OutlineWorkspace) data() data {
	return data{"url": w.URL, "team": w.Team}
}

// OutlineWorkspaceConnected is a workspace connected, or its key replaced,
// from the console.
func OutlineWorkspaceConnected(actor Actor, w OutlineWorkspace, o Outcome) *record.Record {
	return build("roster.outline_workspace.connected", actor, o, personParty(w.KeyUser), []*record.Target{targetOutlineWorkspace(w.Key)}, w.data())
}

// OutlineWorkspaceDisconnected is a workspace's connection record and key
// removed from the console.
func OutlineWorkspaceDisconnected(actor Actor, w OutlineWorkspace) *record.Record {
	return build("roster.outline_workspace.disconnected", actor, Succeeded(), personParty(w.KeyUser), []*record.Target{targetOutlineWorkspace(w.Key)}, w.data())
}

// OutlineChange is one change the Outline controller made, or tried to.
type OutlineChange struct {
	Workspace string
	// Group and Collection name what was changed, when it was a group or a
	// collection.
	Group, Collection string
	// UserID and Person are the account changed, when one was.
	UserID, Person string
	// Permission is a grant's level, Role a role change's new role and From
	// the role before.
	Permission, Role, From string
	Reason                 string
	Groups                 []string
}

func (c OutlineChange) targets() []*record.Target {
	out := []*record.Target{targetOutlineWorkspace(c.Workspace)}
	if c.Group != "" {
		out = append(out, targetOutlineGroup(c.Workspace, c.Group))
	}
	if c.Collection != "" {
		out = append(out, targetOutlineCollection(c.Workspace, c.Collection))
	}
	if c.UserID != "" {
		out = append(out, targetOutlineUser(c.UserID))
	}
	return out
}

func (c OutlineChange) data() data {
	d := data{"reason": c.Reason}
	if len(c.Groups) > 0 {
		d["groups"] = c.Groups
	}
	if c.Permission != "" {
		d["permission"] = c.Permission
	}
	if c.Role != "" {
		d["role"] = c.Role
		d["from"] = c.From
	}
	return d
}

func outlineChange(action string, c OutlineChange, o Outcome) *record.Record {
	return build(action, System(), o, personParty(c.Person), c.targets(), c.data())
}

// OutlineGroupCreated is a managed group the controller created.
func OutlineGroupCreated(c OutlineChange, o Outcome) *record.Record {
	return outlineChange("roster.outline_group.created", c, o)
}

// OutlineMemberAdded is a person added to a managed group.
func OutlineMemberAdded(c OutlineChange, o Outcome) *record.Record {
	return outlineChange("roster.outline_member.added", c, o)
}

// OutlineMemberRemoved is a person removed from a managed group.
func OutlineMemberRemoved(c OutlineChange, o Outcome) *record.Record {
	return outlineChange("roster.outline_member.removed", c, o)
}

// OutlineRoleChanged is an account's workspace role changed.
func OutlineRoleChanged(c OutlineChange, o Outcome) *record.Record {
	return outlineChange("roster.outline_role.changed", c, o)
}

// OutlineCollectionCreated is a bound collection the controller created.
func OutlineCollectionCreated(c OutlineChange, o Outcome) *record.Record {
	return outlineChange("roster.outline_collection.created", c, o)
}

// OutlineCollectionAdopted is an existing collection the controller started
// to manage, recorded once.
func OutlineCollectionAdopted(c OutlineChange) *record.Record {
	return outlineChange("roster.outline_collection.adopted", c, Succeeded())
}

// OutlineCollectionGranted is a group granted a permission on a collection, or
// its permission changed.
func OutlineCollectionGranted(c OutlineChange, o Outcome) *record.Record {
	return outlineChange("roster.outline_collection.granted", c, o)
}

// OutlineCollectionRevoked is a group's grant on a strict collection removed.
func OutlineCollectionRevoked(c OutlineChange, o Outcome) *record.Record {
	return outlineChange("roster.outline_collection.revoked", c, o)
}

// OutlineCollectionMemberRemoved is a direct member removed from a strict
// collection.
func OutlineCollectionMemberRemoved(c OutlineChange, o Outcome) *record.Record {
	return outlineChange("roster.outline_collection.member_removed", c, o)
}

// OutlineUserSuspended is a leaver's account suspended.
func OutlineUserSuspended(c OutlineChange, o Outcome) *record.Record {
	return outlineChange("roster.outline_user.suspended", c, o)
}

// OutlineUserActivated is a suspended account reactivated.
func OutlineUserActivated(c OutlineChange, o Outcome) *record.Record {
	return outlineChange("roster.outline_user.activated", c, o)
}

// OutlineActionHeld is a change held for a person to act, recorded once when
// it becomes held. change is the action that is held.
func OutlineActionHeld(c OutlineChange, change string) *record.Record {
	return build("roster.outline_action.held", System(), Failed(c.Reason), personParty(c.Person), c.targets(), data{"change": change})
}

// OutlineBreaker is one breaker of one workspace.
type OutlineBreaker struct {
	Workspace, Scope, Fingerprint, Reason string
	Affected, Total                       int
}

func (b OutlineBreaker) data() data {
	return data{"scope": b.Scope, "fingerprint": b.Fingerprint, "affected": b.Affected, "total": b.Total}
}

// OutlineBreakerTripped is a set of changes held by a breaker, recorded once
// per set.
func OutlineBreakerTripped(b OutlineBreaker) *record.Record {
	return build("roster.outline_breaker.tripped", System(), Failed(b.Reason), nil, []*record.Target{targetOutlineWorkspace(b.Workspace)}, b.data())
}

// OutlineBreakerConfirmed is an operator confirming a held set.
func OutlineBreakerConfirmed(actor Actor, b OutlineBreaker) *record.Record {
	return build("roster.outline_breaker.confirmed", actor, Succeeded(), nil, []*record.Target{targetOutlineWorkspace(b.Workspace)}, b.data())
}
