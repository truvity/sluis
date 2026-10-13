package compile

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/truvity/sluis/policy"
)

// LIFETIME is a property of the rung, not of the group it grants. One rung
// may hold a cluster's admin group for eight hours and another the same
// group for four; a table keyed by group cannot say that, and the
// shortest-wins merge would quietly halve the longer day. So each rung is
// also an internal group, `rung:<name>`, carrying its session duration and
// nothing else. The name lands in the `groups` claim like any other, where
// it is the only honest answer to "why does my token expire when it does".

type (
	// Policy is the derived policy, ready to render.
	Policy struct {
		// Groups, sorted by name.
		Groups []Group
		// Lifetimes, sorted by group name, with "default" first.
		Lifetimes []Lifetime
		// Deferred are the grants the derivation declined to render, with
		// the reason. A renderer writes them as comments: a grant the model
		// declares and the policy does not carry has to be visible in the
		// generated file, or the file lies by omission.
		Deferred []Deferred
		// GitHub is the policy's `github` table, sorted by organisation. It
		// names the internal groups the derivation minted for it.
		GitHub []GitHubBinding
		// People links each person's addresses across domains, one row per
		// employee, sorted by slug. Only a link: it never says who holds a
		// group.
		People []Person
		// Slack is the policy's `slack` table, sorted by workspace key. It
		// names the internal groups the derivation minted for it.
		Slack []SlackBinding
	}

	// Person is one row of the policy's `people` table.
	Person struct {
		Name      string
		Addresses []string
	}

	// SlackBinding is one workspace's row of the `slack` table.
	SlackBinding struct {
		Workspace string
		Channels  []SlackChannelBinding
	}

	// SlackChannelBinding is one channel, by name.
	SlackChannelBinding struct {
		Name    string
		Private bool
		// Mode is always "extend" or "strict", never empty.
		Mode   string
		Ignore []string
		// From is the one internal group minted for the channel.
		From  []string
		Adopt string
	}

	// GitHubBinding is one organisation's row of the `github` table.
	GitHubBinding struct {
		Org     string
		Members []string
		Teams   []GitHubTeamBinding
		// Ignore is copied as written: addresses and logins, never groups.
		Ignore []string
	}

	// GitHubTeamBinding is one team, by slug.
	GitHubTeamBinding struct {
		Slug        string
		Members     []string
		Maintainers []string
	}

	// Deferred is one declared grant that was not rendered.
	Deferred struct {
		Group   string
		Subject string
		Reason  string
	}

	// Group is one internal group: who is in it, and why.
	Group struct {
		Name string
		// Members are directory groups. The hub confirms an account really
		// is in one before granting anything, so a member the directory
		// cannot vouch for grants nothing.
		Members []string
		// Emails are named accounts, rendered as sign-in matchers. The
		// exception shape: a population's users, never a rung.
		Emails []string
		// Rungs names the rungs that feed this group, for the comment a
		// renderer writes above it.
		Rungs []string
		// GitHub are the CI jobs in this group, rendered as `github`
		// matchers on the job's verified token. Machines, never people.
		GitHub []GitHubMatcher
		// ServiceAccounts are the installation's own programs in this
		// group, rendered as `service_account` matchers.
		ServiceAccounts []ServiceAccountMatcher
		// AWS are IAM roles in this group, rendered as `aws` matchers on the
		// role's verified outbound-federation token.
		AWS []AWSMatcher
	}

	// AWSMatcher is one `aws` matcher: exactly one IAM role.
	AWSMatcher struct {
		Account string
		Role    string
		Path    string
	}

	// ServiceAccountMatcher is one `service_account` matcher: the
	// ServiceAccount is matched exactly, on the cluster named.
	ServiceAccountMatcher struct {
		Cluster   string
		Namespace string
		Name      string
	}

	// GitHubMatcher is one `github` matcher: every set field must hold for
	// the job's token.
	GitHubMatcher struct {
		// Repository is `<owner>/<repo>`.
		Repository string
		// Owner is the organisation.
		Owner string
		// Visibility is public, private or internal.
		Visibility string
		// Ref is the run's ref, `refs/heads/<branch>`.
		Ref string
		// RefType is branch or tag.
		RefType string
		// EventName is what started the run.
		EventName string
		// WorkflowRef is the file the run started from, at its ref.
		WorkflowRef string
		// JobWorkflowRef is the file the job is defined in, at its ref: the
		// reusable workflow, when the caller calls one.
		JobWorkflowRef string
	}

	// Lifetime is one row of the policy's lifetimes table.
	Lifetime struct {
		Group    string
		Duration string
	}
)

// adder accumulates the groups as the derivation mints them.
type adder struct {
	groups map[string]*Group
}

// add puts members, emails and the feeding rung into the named group,
// creating it, without repeats.
func (a *adder) add(name string, members, emails []string, rung string) {
	group, ok := a.groups[name]
	if !ok {
		group = &Group{Name: name}
		a.groups[name] = group
	}

	group.Members = appendUnique(group.Members, members...)
	group.Emails = appendUnique(group.Emails, emails...)

	if rung != "" {
		group.Rungs = appendUnique(group.Rungs, rung)
	}
}

// Roster derives the policy's groups, lifetimes, github, slack and people
// tables from the model.
//
// It returns nil when the model has no scopes matrix: there is then no
// ladder to derive from.
func Roster(m Model, c Conventions) *Policy {
	if m.Scopes == nil {
		return nil
	}

	a := &adder{groups: map[string]*Group{}}
	lifetimes := map[string]string{}

	// One group per rung, carrying that rung's session duration.
	for name, rung := range m.Rungs {
		group := RungPrefix + name
		a.add(group, rung.Groups, rung.Users, "")

		if duration, err := Duration(rung.SessionDuration); err == nil {
			lifetimes[group] = duration
		}
	}

	// The cluster tier: scope by rung to the cluster role.
	if c.ClusterThing != "" {
		for scope, matrix := range m.Scopes {
			if !m.hasKubernetes(scope) {
				continue
			}

			for name, generic := range matrix {
				rung, has := m.Rungs[name]
				if !has {
					continue
				}

				role, ok := c.ClusterRoles[generic]
				if !ok {
					continue
				}

				a.add(GroupName(scope, c.ClusterThing, role), rung.Groups, rung.Users, name)
			}
		}
	}

	rosterProjects(m, c, a)

	// Application grants, with the partner grants they could not render.
	deferred := rosterApplications(m, c, a)

	rosterAdminMirrors(m, c, a)
	rosterRungGrants(m, c, a)

	// One group per employee, named after them: a matcher on each of their
	// addresses rather than members, because a person is not a directory
	// group and the directory has nothing to confirm them against. One
	// human, one slug, any number of corporate identities.
	for slug, addresses := range m.Employees {
		a.add(EmployeePrefix+slug, nil, addresses, "")
	}

	// A GitHub team is a CONSUMER of an internal group, the way a client's
	// `requires` is, so each directory group it names becomes one, named per
	// organisation and team.
	bindings := rosterGitHub(m.GitHub, a)

	// Slack channels, the same way: one internal group per channel.
	slack := rosterSlack(m.Slack, a)

	// CI jobs. A job is a machine: its group has no members, only matchers
	// on what GitHub's token for the run says.
	for _, name := range sortedKeys(m.Jobs) {
		a.add(name, nil, nil, "")
		a.groups[name].GitHub = append(a.groups[name].GitHub, GitHubJobMatchers(m.Jobs[name])...)
	}

	rosterWorkloads(m, a)

	// The default lifetime is the shortest rung's, so that an identity
	// holding no rung is never given a longer session than the most
	// privileged person in the model.
	if shortest := shortestDuration(m.Rungs); shortest != "" {
		lifetimes[policy.LifetimeDefault] = shortest
	}

	sort.Slice(deferred, func(i, j int) bool {
		if deferred[i].Group != deferred[j].Group {
			return deferred[i].Group < deferred[j].Group
		}

		return deferred[i].Subject < deferred[j].Subject
	})

	return &Policy{
		Groups:    sortedGroups(a.groups),
		Lifetimes: sortedLifetimes(lifetimes),
		Deferred:  deferred,
		GitHub:    bindings,
		People:    people(m.Employees),
		Slack:     slack,
	}
}

// rosterProjects derives the namespace tier: a project's deploy population
// holds `<scope>:<project>:deployer` on each scope it holds a grant on, its
// approve population `approver`, the database levels where it runs, and its
// viewer where it asks for one.
func rosterProjects(m Model, c Conventions, a *adder) {
	for _, name := range sortedKeys(m.Projects) {
		project := m.Projects[name]
		approvers := project.Approvers
		if len(approvers) == 0 {
			approvers = project.Deployers
		}

		for _, scope := range project.AllScopes() {
			if !m.hasKubernetes(scope) {
				continue
			}

			a.add(GroupName(scope, name, ProjectDeployer), project.Deployers, nil, "")
			a.add(GroupName(scope, name, ProjectApprover), approvers, nil, "")

			// The database levels, only where the project RUNS: a project
			// that deploys nothing on a scope has no database there.
			if slices.Contains(project.Scopes, scope) {
				rosterProjectDatabaseLevels(m, c, scope, name, project.Deployers, a)
			}

			// Read-only, where the project asks for it: on a scope it runs on
			// the cluster's viewers, rung for rung, or the named users; on a
			// secret scope its own viewer population, because what is read
			// there is the team's credentials rather than its application.
			if slices.Contains(project.ViewerScopes, scope) {
				switch {
				case slices.Contains(project.SecretScopes, scope):
					a.add(GroupName(scope, name, ProjectViewer), project.Viewers, nil, "")
				case len(project.ViewerUsers) > 0:
					a.add(GroupName(scope, name, ProjectViewer), nil, project.ViewerUsers, "")
				default:
					rosterProjectViewer(m, c, scope, name, a)
				}
			}
		}
	}
}

// rosterProjectDatabaseLevels derives `<scope>:<project>:<level>` for each
// database level: the rungs the scope's matrix gives the matching generic
// level, rung for rung, so the right to ask and the role the credential
// opens come from one row. On the conventions' unit scopes the deploy
// population also holds the unit level.
func rosterProjectDatabaseLevels(m Model, c Conventions, scope, name string, deployers []string, a *adder) {
	if !m.hasDatabase(scope) {
		return
	}

	for rungName, generic := range m.Scopes[scope] {
		level, ok := c.databaseLevelOf(generic)
		rung, has := m.Rungs[rungName]

		if !ok || !has {
			continue
		}

		a.add(GroupName(scope, name, level), rung.Groups, rung.Users, rungName)
	}

	if c.UnitDatabaseLevel != "" && slices.Contains(c.UnitDatabaseScopes, scope) {
		a.add(GroupName(scope, name, c.UnitDatabaseLevel), deployers, nil, "")
	}
}

// rosterProjectViewer derives `<scope>:<project>:viewer` from the scope's
// cluster viewers: every rung the matrix gives a level the cluster tier
// narrows to the viewer role. Not a unit: units are not scope-aware, and a
// read grant on a development install is not one on production.
func rosterProjectViewer(m Model, c Conventions, scope, name string, a *adder) {
	if c.ClusterViewerRole == "" {
		return
	}

	for rungName, generic := range m.Scopes[scope] {
		rung, has := m.Rungs[rungName]
		if !has || c.ClusterRoles[generic] != c.ClusterViewerRole {
			continue
		}

		a.add(GroupName(scope, name, ProjectViewer), rung.Groups, rung.Users, rungName)
	}
}

// rosterAdminMirrors derives the groups that hold exactly a scope's cluster
// admins. It runs after the application grants, which validation keeps off
// the cluster tier, so the admins are exactly the rungs'.
func rosterAdminMirrors(m Model, c Conventions, a *adder) {
	if c.ClusterThing == "" || c.ClusterAdminRole == "" {
		return
	}

	for _, mirror := range c.AdminMirrors {
		if mirror.FromScope != "" {
			if admins, ok := a.groups[GroupName(mirror.FromScope, c.ClusterThing, c.ClusterAdminRole)]; ok {
				a.add(GroupName(policy.ScopeAll, mirror.Thing, mirror.Role), admins.Members, admins.Emails, "")
			}

			continue
		}

		for scope := range m.Scopes {
			if !m.hasKubernetes(scope) {
				continue
			}

			if admins, ok := a.groups[GroupName(scope, c.ClusterThing, c.ClusterAdminRole)]; ok {
				a.add(GroupName(scope, mirror.Thing, mirror.Role), admins.Members, admins.Emails, "")
			}
		}
	}
}

// rosterRungGrants derives the groups named rungs hold, by the hosts they
// open rather than by the matrix: a generic level says what a person is on
// a cluster, and a host is not the cluster.
func rosterRungGrants(m Model, c Conventions, a *adder) {
	for _, grant := range c.RungGrants {
		scopes := grant.Scopes
		if len(scopes) == 0 {
			scopes = sortedKeys(m.Scopes)
		}

		for _, scope := range scopes {
			if !m.hasKubernetes(scope) {
				continue
			}

			for _, name := range grant.Rungs {
				if rung, has := m.Rungs[name]; has {
					a.add(GroupName(scope, grant.Thing, grant.Role), rung.Groups, rung.Users, name)
				}
			}
		}
	}
}

// rosterApplications derives the application grants, sorted, so the
// deferred list comes out the same on every render.
//
// A partner directory's population does NOT reach an installation-wide
// application role: whoever holds the hub's own viewer role sees every
// connected directory, so granting it to a partner company's group would
// hand that company the others'. Only an INSTALLATION-WIDE grant (scope
// `all`) is deferred; a grant on one scope shows nobody another company's
// directory.
func rosterApplications(m Model, c Conventions, a *adder) []Deferred {
	var deferred []Deferred

	for _, name := range sortedKeys(m.Applications) {
		grant := m.Applications[name]
		members, emails := grant.Groups, grant.Users
		scope, _, _, _ := policy.SplitGroup(name)
		installationWide := scope == policy.ScopeAll

		if partners := partnerAddresses(c, grant.Groups); installationWide && len(partners) > 0 {
			members = ownAddresses(c, grant.Groups)
			deferred = append(deferred, deferGrant(name, partners)...)
		}

		if partners := partnerAddresses(c, grant.Users); installationWide && len(partners) > 0 {
			emails = ownAddresses(c, grant.Users)
			deferred = append(deferred, deferGrant(name, partners)...)
		}

		a.add(name, members, emails, "")
	}

	return deferred
}

// partnerAddresses are the addresses outside the installation's own
// directory.
func partnerAddresses(c Conventions, addresses []string) []string {
	var out []string

	for _, address := range addresses {
		if !c.own(address) {
			out = append(out, address)
		}
	}

	return out
}

// ownAddresses is the complement.
func ownAddresses(c Conventions, addresses []string) []string {
	var out []string

	for _, address := range addresses {
		if c.own(address) {
			out = append(out, address)
		}
	}

	return out
}

// deferGrant records why an installation-wide grant was not rendered.
func deferGrant(group string, subjects []string) []Deferred {
	thing, role := group, ""
	if _, t, r, ok := policy.SplitGroup(group); ok {
		thing, role = t, r
	}

	out := make([]Deferred, 0, len(subjects))

	for _, subject := range subjects {
		out = append(out, Deferred{
			Group:   group,
			Subject: subject,
			Reason:  "a partner directory cannot hold an installation-wide application role: needs <workspace>:" + thing + ":" + role,
		})
	}

	return out
}

// rosterGitHub mints the internal groups the `github` table names and
// returns the table, sorted.
func rosterGitHub(orgs map[string]GitHubOrg, a *adder) []GitHubBinding {
	out := make([]GitHubBinding, 0, len(orgs))

	for _, org := range sortedKeys(orgs) {
		binding := orgs[org]
		row := GitHubBinding{Org: org, Ignore: sortedUnique(binding.Ignore)}

		if len(binding.Members) > 0 {
			name := GroupName(org, GitHubOrgThing, GitHubMember)
			a.add(name, binding.Members, nil, "")
			row.Members = []string{name}
		}

		for _, slug := range sortedKeys(binding.Teams) {
			team := binding.Teams[slug]
			bound := GitHubTeamBinding{Slug: slug}

			if len(team.Members) > 0 {
				name := GroupName(org, slug, GitHubMember)
				a.add(name, team.Members, nil, "")
				bound.Members = []string{name}
			}

			if len(team.Maintainers) > 0 {
				name := GroupName(org, slug, GitHubMaintainer)
				a.add(name, team.Maintainers, nil, "")
				bound.Maintainers = []string{name}
			}

			row.Teams = append(row.Teams, bound)
		}

		out = append(out, row)
	}

	return out
}

// SlackThing is the thing of a channel's internal group.
func SlackThing(channel string) string {
	return SlackThingPrefix + channel
}

// rosterSlack mints the internal group each Slack channel names and returns
// the table, sorted. The mode is always written out: "extend" is the
// default, but a rendered policy that says so cannot change meaning if the
// default ever does.
func rosterSlack(workspaces map[string]SlackWorkspace, a *adder) []SlackBinding {
	out := make([]SlackBinding, 0, len(workspaces))

	for _, workspace := range sortedKeys(workspaces) {
		row := SlackBinding{Workspace: workspace}

		for _, name := range sortedKeys(workspaces[workspace].Channels) {
			channel := workspaces[workspace].Channels[name]
			group := GroupName(workspace, SlackThing(name), SlackMember)
			a.add(group, channel.Members, nil, "")

			mode := channel.Mode
			if mode == "" {
				mode = policy.SlackModeExtend
			}

			row.Channels = append(row.Channels, SlackChannelBinding{
				Name:    name,
				Private: channel.Private,
				Mode:    mode,
				Ignore:  sortedUnique(channel.Ignore),
				From:    []string{group},
				Adopt:   channel.Adopt,
			})
		}

		out = append(out, row)
	}

	return out
}

// rosterWorkloads derives the installation's own programs: a ServiceAccount
// matcher, or one `aws` matcher per IAM role.
func rosterWorkloads(m Model, a *adder) {
	for _, name := range sortedKeys(m.Workloads) {
		workload := m.Workloads[name]

		a.add(name, nil, nil, "")

		if len(workload.AWS) > 0 {
			for _, role := range workload.AWS {
				a.groups[name].AWS = append(a.groups[name].AWS, AWSMatcher(role))
			}

			continue
		}

		a.groups[name].ServiceAccounts = append(a.groups[name].ServiceAccounts, ServiceAccountMatcher{
			Cluster:   workload.Cluster,
			Namespace: workload.Namespace,
			Name:      workload.Name,
		})
	}
}

// people is the registry as the policy's `people` table.
func people(employees map[string][]string) []Person {
	out := make([]Person, 0, len(employees))

	for _, slug := range sortedKeys(employees) {
		out = append(out, Person{Name: slug, Addresses: sortedUnique(employees[slug])})
	}

	return out
}

// hasKubernetes reports whether a scope has a cluster.
func (m Model) hasKubernetes(scope string) bool {
	if m.ScopeFacts == nil {
		return true
	}

	facts, ok := m.ScopeFacts[scope]

	return ok && facts.Kubernetes
}

// hasDatabase reports whether a scope has project databases.
func (m Model) hasDatabase(scope string) bool {
	if m.ScopeFacts == nil {
		return true
	}

	facts, ok := m.ScopeFacts[scope]

	return ok && facts.Database
}

// Duration converts an ISO-8601 session duration to the Go duration string
// the policy file reads. Only the hour shape (`PT8H`) is accepted: a minute
// is refused with the day, week, month and year shapes, and a duration
// nobody writes is a duration nobody should be able to write by accident.
func Duration(iso string) (string, error) {
	rest, found := strings.CutPrefix(strings.TrimSpace(iso), "PT")
	if !found || rest == "" {
		return "", fmt.Errorf("session duration %q is not an ISO-8601 time duration like PT8H", iso)
	}

	lowered := strings.ToLower(rest)
	if strings.ContainsAny(lowered, "ymwd") {
		return "", fmt.Errorf("session duration %q is longer than a day", iso)
	}

	return lowered, nil
}

// shortestDuration is the shortest session duration any rung declares.
func shortestDuration(rungs map[string]Rung) string {
	best, bestMinutes := "", 0

	for _, rung := range rungs {
		duration, err := Duration(rung.SessionDuration)
		if err != nil {
			continue
		}

		minutes := durationMinutes(duration)
		if minutes == 0 {
			continue
		}

		if best == "" || minutes < bestMinutes {
			best, bestMinutes = duration, minutes
		}
	}

	return best
}

// durationMinutes reads a "4h" or "90m" duration in minutes, for comparison
// only.
func durationMinutes(duration string) int {
	var (
		value int
		unit  rune
	)

	if _, err := fmt.Sscanf(duration, "%d%c", &value, &unit); err != nil {
		return 0
	}

	switch unit {
	case 'h':
		return value * 60
	case 'm':
		return value
	default:
		return 0
	}
}

// sortedGroups sorts the derived groups and their contents, so the render
// is stable. A group nobody is in is not rendered: the issuer refuses a
// `requires` naming an undeclared group, and an empty one grants nothing
// anyway. A machine counts as somebody.
func sortedGroups(groups map[string]*Group) []Group {
	out := make([]Group, 0, len(groups))

	for _, group := range groups {
		if len(group.Members) == 0 && len(group.Emails) == 0 &&
			len(group.GitHub) == 0 && len(group.ServiceAccounts) == 0 && len(group.AWS) == 0 {
			continue
		}

		sort.Strings(group.Members)
		sort.Strings(group.Emails)
		sort.Strings(group.Rungs)
		out = append(out, *group)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

// sortedLifetimes sorts the lifetimes table, default first.
func sortedLifetimes(lifetimes map[string]string) []Lifetime {
	out := make([]Lifetime, 0, len(lifetimes))
	for group, duration := range lifetimes {
		out = append(out, Lifetime{Group: group, Duration: duration})
	}

	sort.Slice(out, func(i, j int) bool {
		if (out[i].Group == policy.LifetimeDefault) != (out[j].Group == policy.LifetimeDefault) {
			return out[i].Group == policy.LifetimeDefault
		}

		return out[i].Group < out[j].Group
	})

	return out
}

// appendUnique appends the values not already present, preserving the
// caller's order.
func appendUnique(into []string, values ...string) []string {
	for _, value := range values {
		if value != "" && !slices.Contains(into, value) {
			into = append(into, value)
		}
	}

	return into
}

// sortedUnique is a list sorted and without repeats.
func sortedUnique(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)

	return slices.Compact(out)
}

// sortedKeys is a map's keys, sorted, so the render is stable.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}

	sort.Strings(out)

	return out
}
