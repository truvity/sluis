package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/truvity/sluis/audit/sdk/record"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/outlineroster/outlineapi"
	"github.com/truvity/sluis/internal/outlineroster/reconcile"
	"github.com/truvity/sluis/internal/outlineroster/status"
	"github.com/truvity/sluis/storage/logattr"
)

// result is what became of one change.
type result struct {
	action reconcile.Action
	// err is Outline's refusal; skipped says the change was not tried (an
	// earlier 429, or a group or collection it rests on was not created).
	err     error
	skipped string
}

// apply makes the changes in order and records each. A 429 stops it: what is
// left is tried by the next pass.
func (c *Controller) apply(ctx context.Context, client *outlineapi.Client, key string, actions []reconcile.Action) []result {
	groupIDs, collectionIDs := map[string]string{}, map[string]string{}
	out := make([]result, 0, len(actions))
	limited := ""
	for i := range actions {
		a := actions[i]
		r := result{action: a}
		if a.GroupID == "" && a.Group != "" {
			a.GroupID = groupIDs[strings.ToLower(a.Group)]
		}
		if a.CollectionID == "" && a.Collection != "" {
			a.CollectionID = collectionIDs[a.Collection]
		}
		switch {
		case limited != "":
			r.skipped = limited
		case ctx.Err() != nil:
			r.skipped = "the tick ended first"
		default:
			r.skipped, r.err = c.do(ctx, client, &a, groupIDs, collectionIDs)
			r.action = a
			if outlineapi.RateLimited(r.err) {
				limited = "Outline's rate limit was reached; the next pass tries again"
			}
		}
		if r.skipped == "" {
			c.audit(ctx, key, r)
			c.metrics.recordChange(ctx, key, a.Kind, r.err == nil)
		}
		if r.err != nil {
			c.deps.Log.WarnContext(ctx, "a change was refused by Outline", logattr.SafeString("workspace", key), logattr.SafeString("action", string(a.Kind)),
				logattr.SafeError("error", r.err))
		}
		out = append(out, r)
	}
	return out
}

// do makes one change. skipped says why it was not tried.
func (c *Controller) do(
	ctx context.Context, client *outlineapi.Client, a *reconcile.Action, groupIDs, collectionIDs map[string]string,
) (skipped string, err error) {
	needGroup := func() string {
		if a.GroupID == "" {
			return "the group " + a.Group + " was not created"
		}
		return ""
	}
	needCollection := func() string {
		if a.CollectionID == "" {
			return "the collection " + a.Collection + " was not created"
		}
		return ""
	}
	switch a.Kind {
	case status.ActionCreate:
		if a.Collection != "" {
			col, err := client.CreateCollection(ctx, a.Collection)
			if err == nil {
				collectionIDs[a.Collection], a.CollectionID = col.ID, col.ID
			}
			return "", err
		}
		g, err := client.CreateGroup(ctx, a.Group, reconcile.ManagedDescription)
		if err == nil {
			groupIDs[strings.ToLower(a.Group)], a.GroupID = g.ID, g.ID
		}
		return "", err
	case status.ActionAdd:
		if why := needGroup(); why != "" {
			return why, nil
		}
		return "", client.AddGroupUser(ctx, a.GroupID, a.UserID)
	case status.ActionRemove:
		if a.Collection != "" {
			return "", client.RemoveCollectionUser(ctx, a.CollectionID, a.UserID)
		}
		return "", client.RemoveGroupUser(ctx, a.GroupID, a.UserID)
	case status.ActionPromote, status.ActionDemote:
		return "", client.UpdateRole(ctx, a.UserID, a.Role)
	case status.ActionGrant:
		if why := needCollection() + needGroup(); why != "" {
			return why, nil
		}
		return "", client.AddCollectionGroup(ctx, a.CollectionID, a.GroupID, a.Permission)
	case status.ActionRevoke:
		return "", client.RemoveCollectionGroup(ctx, a.CollectionID, a.GroupID)
	case status.ActionSuspend:
		return "", client.Suspend(ctx, a.UserID)
	case status.ActionActivate:
		return "", client.Activate(ctx, a.UserID)
	}
	return "", fmt.Errorf("unknown change %q", a.Kind)
}

// audit records one change that was tried.
func (c *Controller) audit(ctx context.Context, key string, r result) {
	if c.deps.Audit == nil {
		return
	}
	a := r.action
	outcome := audit.Succeeded()
	if r.err != nil {
		outcome = audit.Failed(r.err.Error())
	}
	change := audit.OutlineChange{
		Workspace: key, Group: a.Group, Collection: a.Collection, UserID: a.UserID, Person: a.Email,
		Permission: a.Permission, Reason: a.Reason, Groups: a.Groups,
	}
	var rec *record.Record
	switch a.Kind {
	case status.ActionCreate:
		if a.Collection != "" {
			rec = audit.OutlineCollectionCreated(change, outcome)
		} else {
			rec = audit.OutlineGroupCreated(change, outcome)
		}
	case status.ActionAdd:
		rec = audit.OutlineMemberAdded(change, outcome)
	case status.ActionRemove:
		if a.Collection != "" {
			rec = audit.OutlineCollectionMemberRemoved(change, outcome)
		} else {
			rec = audit.OutlineMemberRemoved(change, outcome)
		}
	case status.ActionPromote, status.ActionDemote:
		change.Role, change.From = a.Role, a.From
		rec = audit.OutlineRoleChanged(change, outcome)
	case status.ActionGrant:
		rec = audit.OutlineCollectionGranted(change, outcome)
	case status.ActionRevoke:
		rec = audit.OutlineCollectionRevoked(change, outcome)
	case status.ActionSuspend:
		rec = audit.OutlineUserSuspended(change, outcome)
	case status.ActionActivate:
		rec = audit.OutlineUserActivated(change, outcome)
	default:
		return
	}
	c.deps.Audit.Record(ctx, rec)
}

// fold puts what became of each change on its row: done, refused (retrying,
// with Outline's words) or not tried.
func fold(report *status.Workspace, results []result) {
	report.Tick.Changes = 0
	for i := range results {
		r := &results[i]
		state, action, reason := status.StateOK, r.action.Kind, ""
		switch {
		case r.skipped != "":
			state, action, reason = status.StateRetrying, "", r.skipped
		case r.err != nil:
			state, action, reason = status.StateRetrying, "", "Outline refused: "+r.err.Error()
		default:
			report.Tick.Changes++
		}
		if state == status.StateRetrying {
			report.Tick.Retrying++
		}
		setRow(report, r.action, state, action, reason)
	}
}

// setRow finds the row a change was decided on and sets its state.
func setRow(report *status.Workspace, a reconcile.Action, state status.State, action status.Action, reason string) {
	set := func(s *status.State, act *status.Action, why *string) {
		// A change made keeps the reason it was made for.
		*s, *act = state, action
		if reason != "" {
			*why = reason
		}
	}
	switch {
	case a.Kind == status.ActionCreate && a.Collection != "":
		for i := range report.Collections {
			if col := &report.Collections[i]; col.Name == a.Collection {
				set(&col.State, &col.Action, &col.Reason)
				if a.CollectionID != "" {
					col.ID = a.CollectionID
				}
			}
		}
	case a.Kind == status.ActionCreate:
		for i := range report.Groups {
			if g := &report.Groups[i]; g.Name == a.Group {
				set(&g.State, &g.Action, &g.Reason)
				if a.GroupID != "" {
					g.ID = a.GroupID
				}
			}
		}
	case a.Collection != "":
		for i := range report.Collections {
			col := &report.Collections[i]
			if col.Name != a.Collection {
				continue
			}
			for j := range col.Grants {
				if g := &col.Grants[j]; a.Group != "" && g.Group == a.Group {
					set(&g.State, &g.Action, &g.Reason)
					if state == status.StateOK {
						g.Permission = a.Permission
						if a.Kind == status.ActionRevoke {
							g.Permission = ""
						}
					}
				}
			}
			for j := range col.Members {
				if m := &col.Members[j]; a.Group == "" && m.UserID == a.UserID {
					set(&m.State, &m.Action, &m.Reason)
				}
			}
		}
	case a.Group != "":
		for i := range report.Groups {
			g := &report.Groups[i]
			if g.Name != a.Group {
				continue
			}
			for j := range g.Members {
				if m := &g.Members[j]; m.UserID == a.UserID {
					set(&m.State, &m.Action, &m.Reason)
				}
			}
		}
	default:
		for i := range report.Users {
			if u := &report.Users[i]; u.ID == a.UserID {
				set(&u.State, &u.Action, &u.Reason)
				if state == status.StateOK {
					switch a.Kind {
					case status.ActionPromote, status.ActionDemote:
						u.Role = a.Role
					case status.ActionSuspend:
						u.Suspended = true
					case status.ActionActivate:
						u.Suspended = false
					}
				}
			}
		}
	}
}

// recordOnce records, once each, the holds, the tripped breakers and the
// adopted collections of a pass that acts, and returns the keys recorded, for
// the report to carry so a restart records none of them again.
func (c *Controller) recordOnce(ctx context.Context, key string, report *status.Workspace, seed []string) []string {
	type pending struct {
		key string
		rec func() *record.Record
	}
	var items []pending
	held := func(id, reason string, change audit.OutlineChange, what string) {
		change.Workspace, change.Reason = key, reason
		items = append(items, pending{"held/" + id + "/" + reason, func() *record.Record { return audit.OutlineActionHeld(change, what) }})
	}
	if report.Held != "" {
		held("workspace", report.Held, audit.OutlineChange{}, "workspace")
	}
	for i := range report.Users {
		u := &report.Users[i]
		if u.State == status.StateHeld {
			held("user/"+u.ID, u.Reason, audit.OutlineChange{UserID: u.ID, Person: u.Email}, "user")
		}
	}
	for _, g := range report.Groups {
		if g.State == status.StateHeld {
			held("group/"+g.Name, g.Reason, audit.OutlineChange{Group: g.Name}, "group")
		}
		for _, m := range g.Members {
			if m.State == status.StateHeld {
				held("group/"+g.Name+"/"+m.UserID, m.Reason, audit.OutlineChange{Group: g.Name, UserID: m.UserID, Person: m.Email}, "member")
			}
		}
	}
	for i := range report.Collections {
		col := &report.Collections[i]
		if col.State == status.StateHeld {
			held("collection/"+col.Name, col.Reason, audit.OutlineChange{Collection: col.Name}, "collection")
		}
		for _, g := range col.Grants {
			if g.State == status.StateHeld {
				held("collection/"+col.Name+"/"+g.Group, g.Reason, audit.OutlineChange{Collection: col.Name, Group: g.Group}, "grant")
			}
		}
		for _, m := range col.Members {
			if m.State == status.StateHeld {
				held("collection/"+col.Name+"/"+m.UserID, m.Reason, audit.OutlineChange{Collection: col.Name, UserID: m.UserID, Person: m.Email}, "member")
			}
		}
		switch {
		case col.ID == "" || col.State == status.StateHeld:
		case col.Action == status.ActionCreate:
			// Created here: nothing to adopt, now or at the next pass.
			items = append(items, pending{"adopted/" + col.ID, nil})
		default:
			change := audit.OutlineChange{Workspace: key, Collection: col.Name, Reason: "the collection " + col.ID + " is managed from the policy"}
			items = append(items, pending{"adopted/" + col.ID, func() *record.Record { return audit.OutlineCollectionAdopted(change) }})
		}
	}
	for _, b := range report.Breakers {
		if b.Confirmed {
			continue
		}
		breaker := audit.OutlineBreaker{Workspace: key, Scope: b.Scope, Fingerprint: b.Fingerprint, Reason: b.Reason, Affected: b.Affected, Total: b.Total}
		items = append(items, pending{"breaker/" + b.Scope + "/" + b.Fingerprint, func() *record.Record { return audit.OutlineBreakerTripped(breaker) }})
	}
	keys := make([]string, len(items))
	for i := range items {
		keys[i] = items[i].key
	}
	fresh := c.recorded.Fresh(key, keys, func() []string { return seed })
	for i, item := range items {
		if fresh[i] && item.rec != nil && c.deps.Audit != nil {
			c.deps.Audit.Record(ctx, item.rec())
		}
	}
	// What was adopted stays recorded for as long as it is managed; a hold
	// that cleared is forgotten, so it is recorded again if it returns.
	slices.Sort(keys)
	return slices.Compact(keys)
}
