package reconcile

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/truvity/sluis/internal/emailaddr"
	"github.com/truvity/sluis/internal/outlineroster/connection"
	"github.com/truvity/sluis/internal/outlineroster/status"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/policy"
)

// The breakers' limits.
const (
	// MaxSuspensions is how many accounts one pass suspends without an
	// operator's confirmation.
	MaxSuspensions = 3
	// MaxDemotions is how many admins one pass demotes without one.
	MaxDemotions = 1
)

// KeyUserNotAdmin is the hold of a workspace whose API key's user is not an
// admin: most calls would be refused, and the key must not be trusted to act.
const KeyUserNotAdmin = "the API key's user is not an admin: make that account an admin in Outline, or connect a key of an admin"

// decider carries one pass's working state.
type decider struct {
	in Input
	// users by id, and by lowercase address.
	users   map[string]User
	byEmail map[string]User
	// groupsByName are Outline's groups of each name.
	groupsByName map[string][]Group
	groupName    map[string]string
	// leaving are the accounts this pass suspends, which nothing else adds.
	leaving map[string]bool
	// heldGroups are the managed groups additions to which are held, and why:
	// a group bound to a collection whose `directory` its members reach
	// outside.
	heldGroups map[string]string
	// guarded are, by managed group, the directories a holder's address must
	// be served by: the `directory` of each collection the group is bound to.
	guarded map[string][]Directory

	report  status.Workspace
	actions []Action
}

// Decide is one pass over one workspace.
func Decide(in Input) Decision {
	d := &decider{
		in: in, users: map[string]User{}, byEmail: map[string]User{}, groupsByName: map[string][]Group{},
		groupName: map[string]string{}, leaving: map[string]bool{}, heldGroups: map[string]string{}, guarded: map[string][]Directory{},
	}
	d.report = status.Workspace{Workspace: in.Workspace, KeyUser: in.Observed.KeyUser.Email}
	for _, u := range in.Observed.Users {
		d.users[u.ID] = u
		if u.Email != "" {
			d.byEmail[u.Email] = u
		}
	}
	for _, g := range in.Observed.Groups {
		// Outline keeps group names unique without regard to case.
		d.groupsByName[strings.ToLower(g.Name)] = append(d.groupsByName[strings.ToLower(g.Name)], g)
		d.groupName[g.ID] = g.Name
	}

	var creates, members, roles, collections, grants, revokes, leavers []Action
	d.guardCollections()
	roles, leavers = d.people()
	creates, members = d.groups()
	collections, grants, revokes = d.collections()
	d.findings()

	// Groups are created before anything fills or grants them, collections
	// before anything is granted on them; suspensions come last, after
	// everything that could fail first has been tried.
	for _, batch := range [][]Action{creates, collections, members, grants, revokes, roles, leavers} {
		d.actions = append(d.actions, batch...)
	}
	if in.Observed.KeyUser.Role != RoleAdmin {
		d.holdWorkspace(KeyUserNotAdmin)
	}
	d.count()
	return Decision{Report: d.report, Actions: d.actions}
}

// holdWorkspace holds every change: the rows that would change say held, and
// nothing is done.
func (d *decider) holdWorkspace(reason string) {
	d.report.Held = reason
	d.actions = nil
	hold := func(state *status.State, action *status.Action, why *string) {
		if strings.HasPrefix(string(*state), "will-") {
			*state, *action, *why = status.StateHeld, "", "the workspace is held: "+reason
		}
	}
	for i := range d.report.Users {
		u := &d.report.Users[i]
		hold(&u.State, &u.Action, &u.Reason)
	}
	for i := range d.report.Groups {
		g := &d.report.Groups[i]
		hold(&g.State, &g.Action, &g.Reason)
		for j := range g.Members {
			m := &g.Members[j]
			hold(&m.State, &m.Action, &m.Reason)
		}
	}
	for i := range d.report.Collections {
		c := &d.report.Collections[i]
		hold(&c.State, &c.Action, &c.Reason)
		for j := range c.Grants {
			g := &c.Grants[j]
			hold(&g.State, &g.Action, &g.Reason)
		}
		for j := range c.Members {
			m := &c.Members[j]
			hold(&m.State, &m.Action, &m.Reason)
		}
	}
}

// count fills the tick's counts from the rows and the changes.
func (d *decider) count() {
	t := &d.report.Tick
	t.Changes = len(d.actions)
	tally := func(s status.State) {
		switch s {
		case status.StateHeld:
			t.Held++
		case status.StateRetrying:
			t.Retrying++
		}
	}
	for i := range d.report.Users {
		u := &d.report.Users[i]
		tally(u.State)
	}
	for _, g := range d.report.Groups {
		tally(g.State)
		for _, m := range g.Members {
			tally(m.State)
		}
	}
	for i := range d.report.Collections {
		c := &d.report.Collections[i]
		tally(c.State)
		for _, g := range c.Grants {
			tally(g.State)
		}
		for _, m := range c.Members {
			tally(m.State)
		}
	}
	if d.report.Held != "" {
		t.Held++
	}
}

// ignored is why an account is never demoted, removed or suspended, empty
// when it may be.
func (d *decider) ignored(u User) string {
	switch {
	case u.ID != "" && u.ID == d.in.Observed.KeyUser.ID:
		return "the API key's own user"
	case u.Email != "" && d.in.Policy.Ignored(u.Email):
		return "on the workspace's ignore list"
	}
	return ""
}

// person is the answer about an account's address, and whether one exists.
func (d *decider) person(u User) (Person, bool) {
	if u.Email == "" {
		return Person{}, false
	}
	p, ok := d.in.People[u.Email]
	return p, ok && p.Asked
}

// admitted is whether a person reaches the workspace: the client's required
// groups when the policy names a client, else any group the workspace binds.
func (d *decider) admitted(p Person) bool {
	if d.in.Client != "" {
		return p.Admitted
	}
	return holdsAny(p.Groups, d.in.Policy.Groups())
}

// people decides every account's role and whether it is suspended or
// reactivated, and holds the admin and suspension breakers.
func (d *decider) people() (roles, leavers []Action) {
	var demotions, suspensions []int
	admins := 0
	for _, u := range d.in.Observed.Users {
		if !u.Suspended && u.Role == RoleAdmin {
			admins++
		}
	}
	for _, u := range sortedUsers(d.in.Observed.Users) {
		row := status.User{Email: u.Email, ID: u.ID, Name: u.Name, Role: u.Role, Suspended: u.Suspended}
		p, asked := d.person(u)
		why := d.ignored(u)
		target := RoleMember
		if asked && holdsAny(p.Groups, d.in.Policy.Roles.Admin) {
			target = RoleAdmin
		}
		switch {
		case u.Suspended:
			row.State = status.StateOK
			if why == "" && asked && p.Authoritative && p.Found && !p.Suspended && d.admitted(p) {
				row.State, row.Action, row.Reason = status.StateWillActivate, status.ActionActivate,
					"the directory has the account back and it reaches the workspace"
				leavers = append(leavers, Action{Kind: status.ActionActivate, UserID: u.ID, Email: u.Email, Reason: row.Reason, Groups: p.Groups})
			}
		case u.Email == "":
			row.State, row.Reason = status.StateHeld, "the Outline account shows no address, so the directory cannot be asked about it"
		case !asked:
			row.State, row.Reason = status.StateRetrying, "the directory could not be asked about "+u.Email+" this pass"
		case why == "" && p.Gone():
			row.State, row.Action, row.Reason = status.StateWillSuspend, status.ActionSuspend, goneReason(p)
		case why == "" && p.Authoritative && !d.admitted(p):
			row.State, row.Action, row.Reason = status.StateWillSuspend, status.ActionSuspend, d.notAdmittedReason()
		case !p.Authoritative && len(p.Groups) == 0:
			row.State, row.Reason = status.StateRetrying, "the directory cannot vouch for "+u.Email+" right now and nothing is held: nothing changes until it can"
		case u.Role == RoleGuest:
			row.State, row.Reason = status.StateReported, "a guest: guests are left alone"
		case u.Role == target:
			row.State, row.Target = status.StateOK, target
		case target == RoleAdmin && !p.Authoritative:
			// An answer the directory cannot vouch for adds people, and never
			// makes anybody an admin.
			row.State, row.Target, row.Reason = status.StateRetrying, target,
				"the directory cannot vouch for "+u.Email+" right now, so the admin role waits for an answer it can"
		case target == RoleAdmin || u.Role == RoleViewer:
			row.State, row.Target, row.Action = status.StateWillChange, target, status.ActionPromote
			row.Reason = "holds " + strings.Join(intersect(p.Groups, d.in.Policy.Roles.Admin), ", ")
			if target != RoleAdmin {
				row.Reason = "every managed account is a member"
			}
			roles = append(roles, Action{Kind: status.ActionPromote, UserID: u.ID, Email: u.Email, Role: target, From: u.Role, Reason: row.Reason, Groups: p.Groups})
		case why != "":
			row.State, row.Target, row.Reason = status.StateIgnored, target, why
		case !p.Authoritative:
			row.State, row.Target, row.Reason = status.StateRetrying, target, "the directory cannot vouch for "+u.Email+" right now, so the admin role stays"
		default:
			row.State, row.Target, row.Action = status.StateWillChange, target, status.ActionDemote
			row.Reason = "holds none of " + strings.Join(d.in.Policy.Roles.Admin, ", ")
			demotions = append(demotions, len(d.report.Users))
		}
		if row.State == status.StateWillSuspend {
			d.leaving[u.ID] = true
			suspensions = append(suspensions, len(d.report.Users))
		}
		if why != "" && row.State == status.StateOK && u.Role != RoleAdmin && target != RoleAdmin {
			row.Reason = why
		}
		d.report.Users = append(d.report.Users, row)
	}

	// The admin breaker: more than one demotion, or none left.
	if len(demotions) > 0 {
		var lines []string
		for _, i := range demotions {
			lines = append(lines, "demote "+d.report.Users[i].Email)
		}
		reason := ""
		switch {
		case len(demotions) > MaxDemotions:
			reason = fmt.Sprintf("more than %d admin would be demoted in one pass", MaxDemotions)
		case admins-len(demotions) < 1:
			reason = "the last admin would be demoted"
		}
		if d.tripped(connection.ScopeAdmins, reason, len(demotions), admins, lines) {
			for _, i := range demotions {
				r := &d.report.Users[i]
				r.State, r.Action, r.Reason = status.StateHeld, "", "held by the admin breaker: an operator confirms the demotions"
			}
		} else {
			for _, i := range demotions {
				r := d.report.Users[i]
				roles = append(roles, Action{Kind: status.ActionDemote, UserID: r.ID, Email: r.Email, Role: RoleMember, From: RoleAdmin, Reason: r.Reason})
			}
		}
	}
	// The suspension breaker: more than three in one pass.
	if len(suspensions) > 0 {
		var lines []string
		for _, i := range suspensions {
			lines = append(lines, "suspend "+d.report.Users[i].Email)
		}
		reason := ""
		if len(suspensions) > MaxSuspensions {
			reason = fmt.Sprintf("more than %d accounts would be suspended in one pass", MaxSuspensions)
		}
		active := 0
		for _, u := range d.in.Observed.Users {
			if !u.Suspended {
				active++
			}
		}
		if d.tripped(connection.ScopeSuspensions, reason, len(suspensions), active, lines) {
			for _, i := range suspensions {
				r := &d.report.Users[i]
				r.State, r.Action, r.Reason = status.StateHeld, "", "held by the suspension breaker: an operator confirms the suspensions ("+r.Reason+")"
			}
		} else {
			for _, i := range suspensions {
				r := d.report.Users[i]
				leavers = append(leavers, Action{Kind: status.ActionSuspend, UserID: r.ID, Email: r.Email, Reason: r.Reason})
			}
		}
	}
	return roles, leavers
}

func goneReason(p Person) string {
	if p.Found && p.Suspended {
		return "the directory has suspended the account"
	}
	return "the directory no longer has the account"
}

func (d *decider) notAdmittedReason() string {
	if d.in.Client != "" {
		return "holds none of the groups the " + d.in.Client + " client requires"
	}
	return "holds none of the groups the workspace binds"
}

// tripped records a breaker when reason is set, and says whether it holds
// the set: it does unless an operator confirmed exactly these lines.
func (d *decider) tripped(scope, reason string, affected, total int, lines []string) bool {
	if reason == "" {
		return false
	}
	fingerprint := rails.Fingerprint(lines)
	confirmed := d.in.Confirmed[scope] == fingerprint
	d.report.Breakers = append(d.report.Breakers, status.Breaker{
		Scope: scope, Affected: affected, Total: total, Reason: reason, Fingerprint: fingerprint, Confirmed: confirmed,
	})
	return !confirmed
}

// halfBreaker is the removal breaker of a group or a collection: more than
// half of what it has.
func (d *decider) halfBreaker(scope, what string, affected, total int, lines []string) bool {
	b := rails.CheckBreaker(affected, lines, total, d.in.Confirmed[scope])
	if b == nil {
		return false
	}
	d.report.Breakers = append(d.report.Breakers, status.Breaker{
		Scope: scope, Affected: b.Affected, Total: b.Total, Fingerprint: b.Fingerprint, Confirmed: b.Confirmed,
		Reason: fmt.Sprintf("more than half of %s would be removed in one pass", what),
	})
	return !b.Confirmed
}

// groups decides the managed groups: created when missing, filled with their
// holders and emptied of the rest.
func (d *decider) groups() (creates, members []Action) {
	for _, name := range d.in.Policy.Groups() {
		row := status.Group{Name: name}
		found := d.groupsByName[strings.ToLower(name)]
		switch {
		case len(found) > 1:
			row.State, row.Reason = status.StateHeld, fmt.Sprintf("%d Outline groups are named %s: delete or rename the extra ones", len(found), name)
			d.report.Groups = append(d.report.Groups, row)
			continue
		case len(found) == 0:
			row.State, row.Action = status.StateWillCreate, status.ActionCreate
			creates = append(creates, Action{Kind: status.ActionCreate, Group: name, Reason: "bound by the policy"})
		default:
			row.ID, row.State = found[0].ID, status.StateOK
		}
		current := map[string]bool{}
		if row.ID != "" {
			ids, read := d.in.Observed.Members[row.ID]
			if !read {
				row.State, row.Reason = status.StateHeld, "its members could not be read whole this pass, so nothing in it changes"
				d.report.Groups = append(d.report.Groups, row)
				continue
			}
			for _, id := range ids {
				current[id] = true
			}
		}
		var removals []int
		for _, u := range sortedUsers(d.in.Observed.Users) {
			in := current[u.ID]
			if u.Suspended || d.leaving[u.ID] {
				if in {
					row.Members = append(row.Members, status.Member{Email: u.Email, UserID: u.ID, State: status.StateOK, Reason: "a suspended account"})
				}
				continue
			}
			p, asked := d.person(u)
			holds := asked && slices.Contains(p.Groups, name)
			m := status.Member{Email: u.Email, UserID: u.ID}
			switch {
			case holds && in:
				m.State = status.StateOK
			case holds:
				if why := d.addHeld(name, u.Email); why != "" {
					m.State, m.Reason = status.StateHeld, why
				} else if !p.Authoritative && d.confersAdmin(name) {
					m.State, m.Reason = status.StateRetrying, "the directory cannot vouch for "+u.Email+
						" right now, and "+name+" makes its holders admins: the addition waits for an answer it can vouch for"
				} else {
					m.State, m.Action, m.Reason = status.StateWillAdd, status.ActionAdd, "holds "+name
					members = append(members, Action{Kind: status.ActionAdd, Group: name, GroupID: row.ID, UserID: u.ID, Email: u.Email,
						Reason: m.Reason, Groups: []string{name}})
				}
			case !in:
				continue
			case d.ignored(u) != "":
				m.State, m.Reason = status.StateIgnored, d.ignored(u)
			case u.Email == "":
				m.State, m.Reason = status.StateHeld, "the Outline account shows no address"
			case !asked:
				m.State, m.Reason = status.StateRetrying, "the directory could not be asked about "+u.Email+" this pass"
			case !p.Authoritative:
				m.State, m.Reason = status.StateRetrying, "the directory cannot vouch for "+u.Email+" right now"
			default:
				m.State, m.Action, m.Reason = status.StateWillRemove, status.ActionRemove, "no longer holds "+name
				removals = append(removals, len(row.Members))
			}
			row.Members = append(row.Members, m)
		}
		if len(removals) > 0 {
			var lines []string
			for _, i := range removals {
				lines = append(lines, "remove "+row.Members[i].Email+" from "+name)
			}
			if d.halfBreaker(connection.GroupScope(name), "the group's members", len(removals), len(current), lines) {
				for _, i := range removals {
					m := &row.Members[i]
					m.State, m.Action, m.Reason = status.StateHeld, "", "held by the group's removal breaker: an operator confirms the removals"
				}
			} else {
				for _, i := range removals {
					m := row.Members[i]
					members = append(members, Action{Kind: status.ActionRemove, Group: name, GroupID: row.ID, UserID: m.UserID, Email: m.Email, Reason: m.Reason})
				}
			}
		}
		d.report.Groups = append(d.report.Groups, row)
	}
	return creates, members
}

// confersAdmin reports a managed group that makes its holders admins: of the
// workspace (roles.admin), or of a collection (granted `admin` on one). An
// answer the directory cannot vouch for adds nobody to such a group.
func (d *decider) confersAdmin(group string) bool {
	if slices.Contains(d.in.Policy.Roles.Admin, group) {
		return true
	}
	for name := range d.in.Policy.Collections {
		if d.in.Policy.Collections[name].Grants()[group] == policy.OutlineAdmin {
			return true
		}
	}
	return false
}

// addHeld is why a person is not added to a managed group, empty when they
// are: the group is bound to a collection whose `directory` check failed, or
// their address is not served by the directory a collection the group is
// bound to names.
func (d *decider) addHeld(group, email string) string {
	if why := d.heldGroups[group]; why != "" {
		return why
	}
	domain, _ := emailaddr.Domain(email)
	for _, dir := range d.guarded[group] {
		if !slices.Contains(dir.Domains, domain) {
			return fmt.Sprintf("%s is not served by %s, which a collection %s is granted on requires", email, dirName(dir), group)
		}
	}
	return ""
}

func dirName(dir Directory) string {
	if dir.Primary != "" {
		return dir.Primary
	}
	return dir.ID
}

// guardCollections checks every bound collection's `directory` against the
// definitions of the groups bound to it, holding a collection whose groups
// reach outside it, and the additions to those groups.
func (d *decider) guardCollections() {
	for _, name := range slices.Sorted(maps.Keys(d.in.Policy.Collections)) {
		c := d.in.Policy.Collections[name]
		if c.Directory == "" {
			continue
		}
		dir, why := d.directoryCheck(c)
		for group := range c.Grants() {
			if why != "" {
				if d.heldGroups[group] == "" {
					d.heldGroups[group] = "the collection " + name + " is held: " + why
				}
				continue
			}
			d.guarded[group] = append(d.guarded[group], dir)
		}
	}
}

// directoryCheck is the directory a collection names and, when its groups
// reach outside it, why.
func (d *decider) directoryCheck(c policy.OutlineCollection) (Directory, string) {
	if d.in.DirectoryErr != nil {
		return Directory{}, "the connected directories could not be read, so " + c.Directory + " cannot be checked: " + d.in.DirectoryErr.Error()
	}
	var dir Directory
	found := false
	for _, candidate := range d.in.Directories {
		if strings.EqualFold(candidate.Primary, c.Directory) {
			dir, found = candidate, true
			break
		}
	}
	if !found {
		return Directory{}, "the directory " + c.Directory + " is not connected"
	}
	for _, group := range slices.Sorted(maps.Keys(c.Grants())) {
		def := d.in.Groups[group]
		for _, member := range def.Members {
			g, ok := d.in.DirGroups[strings.ToLower(member)]
			switch {
			case !ok || !g.Found:
				return dir, fmt.Sprintf("%s's members include %s, which is not a group of a connected directory", group, member)
			case g.Owner != dir.ID:
				return dir, fmt.Sprintf("%s's members include %s, a group of another directory than %s", group, member, c.Directory)
			}
		}
		for _, m := range def.Matchers {
			switch {
			case m.EmailDomain != "":
				if !slices.Contains(dir.Domains, strings.ToLower(m.EmailDomain)) {
					return dir, fmt.Sprintf("%s admits the domain %s, which %s does not serve", group, m.EmailDomain, c.Directory)
				}
			case m.Email != "":
				domain, _ := emailaddr.Domain(strings.ToLower(m.Email))
				if !slices.Contains(dir.Domains, domain) {
					return dir, fmt.Sprintf("%s admits %s, whose domain %s does not serve", group, m.Email, c.Directory)
				}
			default:
				return dir, fmt.Sprintf("%s admits %s, which no directory vouches for", group, m.Describe())
			}
		}
	}
	return dir, ""
}

// collections decides the bound collections: found or created, granted, and,
// when strict, emptied of what the policy does not name.
func (d *decider) collections() (creates, grants, revokes []Action) {
	for _, name := range slices.Sorted(maps.Keys(d.in.Policy.Collections)) {
		bound := d.in.Policy.Collections[name]
		mode := bound.Mode
		if mode == "" {
			mode = policy.OutlineModeExtend
		}
		row := status.Collection{Name: name, Mode: mode, Directory: bound.Directory}
		target := bound.Grants()
		var found []Collection
		for _, c := range d.in.Observed.Collections {
			if (bound.Adopt != "" && c.ID == bound.Adopt) || (bound.Adopt == "" && c.Name == name) {
				found = append(found, c)
			}
		}
		switch {
		case bound.Adopt != "" && len(found) == 0:
			row.State, row.Reason = status.StateHeld, "no collection the API key's user can see has the id "+bound.Adopt
		case len(found) > 1:
			row.State, row.Reason = status.StateHeld, fmt.Sprintf("%d collections are named %s: set `adopt` to the one meant", len(found), name)
		case len(found) == 0:
			row.State, row.Action = status.StateWillCreate, status.ActionCreate
			creates = append(creates, Action{Kind: status.ActionCreate, Collection: name, Reason: "bound by the policy"})
		default:
			row.ID, row.State = found[0].ID, status.StateOK
		}
		if row.State == status.StateHeld {
			d.report.Collections = append(d.report.Collections, row)
			continue
		}
		if bound.Directory != "" {
			if _, why := d.directoryCheck(bound); why != "" {
				row.State, row.Action, row.Reason = status.StateHeld, "", why
				d.report.Collections = append(d.report.Collections, row)
				if len(creates) > 0 && creates[len(creates)-1].Collection == name {
					creates = creates[:len(creates)-1]
				}
				continue
			}
		}
		var current []Grant
		var direct []Membership
		if row.ID != "" {
			g, gOK := d.in.Observed.Grants[row.ID]
			m, mOK := d.in.Observed.Direct[row.ID]
			if !gOK || !mOK {
				row.State, row.Reason = status.StateHeld, "its grants and members could not be read whole this pass, so nothing on it changes"
				d.report.Collections = append(d.report.Collections, row)
				continue
			}
			current, direct = g, m
		}
		g, r := d.collectionGrants(&row, bound, target, current, direct)
		grants = append(grants, g...)
		revokes = append(revokes, r...)
		d.report.Collections = append(d.report.Collections, row)
	}
	return creates, grants, revokes
}

// collectionGrants decides one collection's group grants and direct members.
func (d *decider) collectionGrants(row *status.Collection, bound policy.OutlineCollection, target map[string]string,
	current []Grant, direct []Membership,
) (grants, revokes []Action) {
	have := map[string]Grant{}
	for _, g := range current {
		have[strings.ToLower(d.groupName[g.GroupID])] = g
	}
	boundLower := map[string]bool{}
	for group := range target {
		boundLower[strings.ToLower(group)] = true
	}
	for _, group := range slices.Sorted(maps.Keys(target)) {
		want := target[group]
		grant := status.Grant{Group: group, Target: want}
		found := d.groupsByName[strings.ToLower(group)]
		if len(found) > 1 {
			grant.State, grant.Reason = status.StateHeld, "more than one Outline group is named "+group
			row.Grants = append(row.Grants, grant)
			continue
		}
		groupID := ""
		if len(found) == 1 {
			groupID = found[0].ID
		}
		grant.GroupID = groupID
		if g, ok := have[strings.ToLower(group)]; ok && groupID != "" && g.GroupID == groupID {
			grant.Permission = g.Permission
		}
		switch grant.Permission {
		case want:
			grant.State = status.StateOK
		case "":
			grant.State, grant.Action, grant.Reason = status.StateWillAdd, status.ActionGrant, "bound at "+want
			grants = append(grants, Action{Kind: status.ActionGrant, Collection: row.Name, CollectionID: row.ID, Group: group, GroupID: groupID,
				Permission: want, Reason: grant.Reason, Groups: []string{group}})
		default:
			grant.State, grant.Action, grant.Reason = status.StateWillChange, status.ActionGrant, "bound at "+want+", granted "+grant.Permission
			grants = append(grants, Action{Kind: status.ActionGrant, Collection: row.Name, CollectionID: row.ID, Group: group, GroupID: groupID,
				Permission: want, Reason: grant.Reason, Groups: []string{group}})
		}
		row.Grants = append(row.Grants, grant)
	}

	var pending []Action
	var lines []string
	for _, g := range current {
		name := d.groupName[g.GroupID]
		if boundLower[strings.ToLower(name)] && len(d.groupsByName[strings.ToLower(name)]) == 1 {
			continue
		}
		label := name
		if label == "" {
			label = g.GroupID
		}
		grant := status.Grant{Group: label, GroupID: g.GroupID, Permission: g.Permission}
		if !bound.Strict() {
			grant.State, grant.Reason = status.StateReported, "not bound: an extend collection keeps it"
		} else {
			grant.State, grant.Action, grant.Reason = status.StateWillRemove, status.ActionRevoke, "not bound by the policy"
			pending = append(pending, Action{Kind: status.ActionRevoke, Collection: row.Name, CollectionID: row.ID, Group: label, GroupID: g.GroupID,
				Permission: g.Permission, Reason: grant.Reason})
			lines = append(lines, "revoke "+label+" on "+row.Name)
		}
		row.Grants = append(row.Grants, grant)
	}
	for _, m := range direct {
		u := d.users[m.UserID]
		member := status.Member{Email: u.Email, UserID: m.UserID, Permission: m.Permission}
		switch {
		case !bound.Strict():
			member.State, member.Reason = status.StateReported, "a direct member: an extend collection keeps them"
		case d.ignored(User{ID: m.UserID, Email: u.Email}) != "":
			member.State, member.Reason = status.StateIgnored, d.ignored(User{ID: m.UserID, Email: u.Email})
		default:
			member.State, member.Action, member.Reason = status.StateWillRemove, status.ActionRemove,
				"a direct member: access to a strict collection comes through its groups"
			pending = append(pending, Action{Kind: status.ActionRemove, Collection: row.Name, CollectionID: row.ID, UserID: m.UserID, Email: u.Email,
				Permission: m.Permission, Reason: member.Reason})
			label := u.Email
			if label == "" {
				label = m.UserID
			}
			lines = append(lines, "remove "+label+" from "+row.Name)
		}
		row.Members = append(row.Members, member)
	}
	if len(pending) == 0 {
		return grants, nil
	}
	if d.halfBreaker(connection.CollectionScope(row.Name), "the collection's grants and members", len(pending), len(current)+len(direct), lines) {
		for i := range row.Grants {
			if row.Grants[i].State == status.StateWillRemove {
				row.Grants[i].State, row.Grants[i].Action, row.Grants[i].Reason = status.StateHeld, "",
					"held by the collection's removal breaker: an operator confirms the removals"
			}
		}
		for i := range row.Members {
			if row.Members[i].State == status.StateWillRemove {
				row.Members[i].State, row.Members[i].Action, row.Members[i].Reason = status.StateHeld, "",
					"held by the collection's removal breaker: an operator confirms the removals"
			}
		}
		return grants, nil
	}
	return grants, pending
}

// findings reports what is never changed: drifted settings, single-document
// shares that cross companies, and integrations.
func (d *decider) findings() {
	if s := d.in.Observed.Settings; s != nil {
		add := func(subject, detail string) {
			d.report.Reported = append(d.report.Reported, status.Finding{Kind: status.FindingSetting, Subject: subject, Detail: detail})
		}
		if s.Sharing {
			add("sharing", "public sharing of documents is on")
		}
		if s.MemberCollectionCreate {
			add("memberCollectionCreate", "members can create collections")
		}
		if s.MemberInvites {
			add("memberInvites", "members can invite people")
		}
		if s.DefaultRole != "" && s.DefaultRole != RoleMember {
			add("defaultUserRole", "a new account's role is "+s.DefaultRole+", not member")
		}
	}
	if !d.in.Observed.SharesScanned {
		d.report.Reported = append(d.report.Reported, d.in.Findings...)
	} else {
		d.report.Reported = append(d.report.Reported, d.shareFindings()...)
	}
	byID := map[string]string{}
	for i := range d.report.Collections {
		c := &d.report.Collections[i]
		if c.ID != "" {
			byID[c.ID] = c.Name
		}
	}
	for _, i := range d.in.Observed.Integrations {
		name, bound := byID[i.CollectionID]
		if !bound || d.in.Policy.Collections[name].Directory == "" {
			continue
		}
		detail := "posts the collection's changes to " + i.Service
		if i.Team != "" {
			detail += " team " + i.Team
		}
		d.report.Reported = append(d.report.Reported, status.Finding{Kind: status.FindingIntegration, Subject: i.ID, Collection: name,
			Detail: detail + ": check it is the company's own"})
	}
}

// ShareFindings are the share findings of a report, which a pass that does not
// scan carries over.
func ShareFindings(w status.Workspace) []status.Finding {
	var out []status.Finding
	for _, f := range w.Reported {
		if f.Kind == status.FindingShare {
			out = append(out, f)
		}
	}
	return out
}

// shareFindings are the single-document shares, in a collection that names a
// directory, with somebody that directory does not serve.
func (d *decider) shareFindings() []status.Finding {
	dirs := map[string]Directory{}
	names := map[string]string{}
	for i := range d.report.Collections {
		c := &d.report.Collections[i]
		bound := d.in.Policy.Collections[c.Name]
		if c.ID == "" || bound.Directory == "" {
			continue
		}
		if dir, why := d.directoryCheck(bound); why == "" {
			dirs[c.ID], names[c.ID] = dir, c.Name
		}
	}
	var out []status.Finding
	for _, s := range d.in.Observed.Shares {
		dir, ok := dirs[s.CollectionID]
		if !ok {
			continue
		}
		subject := s.Title
		if subject == "" {
			subject = s.DocumentID
		}
		if s.UserID != "" {
			u := d.users[s.UserID]
			domain, _ := emailaddr.Domain(u.Email)
			if u.Email != "" && slices.Contains(dir.Domains, domain) {
				continue
			}
			who := u.Email
			if who == "" {
				who = s.UserID
			}
			out = append(out, status.Finding{Kind: status.FindingShare, Subject: subject, Collection: names[s.CollectionID],
				Detail: "shared with " + who + ", whom " + dirName(dir) + " does not serve"})
			continue
		}
		group := d.groupName[s.GroupID]
		if slices.ContainsFunc(slices.Collect(maps.Keys(d.in.Policy.Collections[names[s.CollectionID]].Grants())),
			func(bound string) bool { return strings.EqualFold(bound, group) }) {
			continue
		}
		if group == "" {
			group = s.GroupID
		}
		out = append(out, status.Finding{Kind: status.FindingShare, Subject: subject, Collection: names[s.CollectionID],
			Detail: "shared with the group " + group + ", which the collection does not bind"})
	}
	return out
}

func holdsAny(held, wanted []string) bool {
	for _, g := range wanted {
		if slices.Contains(held, g) {
			return true
		}
	}
	return false
}

func intersect(held, wanted []string) []string {
	var out []string
	for _, g := range wanted {
		if slices.Contains(held, g) {
			out = append(out, g)
		}
	}
	return out
}

func sortedUsers(users []User) []User {
	out := slices.Clone(users)
	slices.SortFunc(out, func(a, b User) int {
		if c := strings.Compare(a.Email, b.Email); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}
