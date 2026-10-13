package compile

import (
	"slices"

	"github.com/truvity/sluis/policy"
)

// The roles a business project's groups end in: `<scope>:<project>:<role>`.
const (
	// ProjectDeployer is held by the project's deploy population on each of
	// its scopes.
	ProjectDeployer = "deployer"
	// ProjectApprover is held by its approve population: a promotion gate.
	ProjectApprover = "approver"
	// ProjectViewer is read-only on the project's own application, on the
	// scopes the project asks for one.
	ProjectViewer = "viewer"
)

// The prefixes of the two families that are not grants, as written in a
// group name.
const (
	// RungPrefix starts a rung's own group, which carries the rung's
	// session lifetime and grants nothing: `rung:<name>`.
	RungPrefix = policy.FamilyRung + policy.Separator
	// EmployeePrefix starts a person's own group: `emp:<slug>`.
	EmployeePrefix = policy.FamilyEmp + policy.Separator
)

// The segments of the groups the GitHub and Slack bindings mint.
const (
	// GitHubMember and GitHubMaintainer are the roles of a team's groups.
	GitHubMember     = "member"
	GitHubMaintainer = "maintainer"
	// GitHubOrgThing is the thing of an organisation's own member group,
	// `<org>:org:member`. No team may take the slug.
	GitHubOrgThing = "org"
	// SlackThingPrefix starts a channel's thing, `<workspace>:slack-<channel>:member`,
	// so a channel never shares a thing with a GitHub team of the same name.
	SlackThingPrefix = "slack-"
	// SlackMember is the one role of a channel's group.
	SlackMember = "member"
)

type (
	// Conventions are an installation's own naming decisions: which thing
	// spells the cluster tier, which generic level opens which cluster
	// role and database level, and the groups it derives from a rung or
	// from the cluster admins rather than from the matrix. The zero value
	// derives the rungs, the projects' deployer and approver, the
	// application grants and the directory and machine rows, and nothing
	// that needs a decision.
	Conventions struct {
		// ClusterThing is the thing of the cluster tier: `<scope>:<thing>:<role>`.
		// Empty derives no cluster tier, and so no project viewer, no admin
		// mirror and no rung-derived database level.
		ClusterThing string
		// ClusterRoles maps a generic level to the cluster role it collapses
		// to (`admin`, `viewer`, `auditor`). A level not here grants nothing
		// on the cluster tier.
		ClusterRoles map[string]string
		// ClusterViewerRole is the cluster role whose holders are, rung for
		// rung, a project's viewers on a scope it runs on.
		ClusterViewerRole string
		// ClusterAdminRole is the cluster role [AdminMirror] copies.
		ClusterAdminRole string
		// DatabaseLevels are a project's database levels, in ladder order,
		// each opened by one generic level of the matrix.
		DatabaseLevels []DatabaseLevel
		// UnitDatabaseLevel and UnitDatabaseScopes: on these scopes the
		// project's deploy population also holds this database level,
		// beside the rungs the matrix gives it.
		UnitDatabaseLevel  string
		UnitDatabaseScopes []string
		// ViewerImpliedBy are the project roles that imply the project's
		// viewer in the vocabulary, wherever the coverage rule allows it.
		ViewerImpliedBy []string
		// MachineReaderRoles are project roles only a workload holds: the
		// vocabulary admits each on exactly the scopes a workload row
		// claims it.
		MachineReaderRoles []string
		// AdminMirrors are groups holding exactly a scope's cluster admins.
		AdminMirrors []AdminMirror
		// RungGrants are groups held by named rungs, by the host they open
		// rather than by the matrix.
		RungGrants []RungGrant
		// OwnDirectory reports whether an address lives in the
		// installation's own directory. A partner directory's population
		// cannot hold an installation-wide application role: such a grant
		// is deferred, and said out loud. Nil treats every address as own.
		OwnDirectory func(address string) bool
	}

	// DatabaseLevel is one database level of a project, and the generic
	// level of the matrix that opens it.
	DatabaseLevel struct {
		// Name is the level, the role of `<scope>:<project>:<level>`.
		Name string
		// From is the generic level that holds it, rung for rung.
		From string
	}

	// AdminMirror is a group whose holders are a scope's cluster admins,
	// member for member: `<thing>:<role>` on every scope with a cluster, or,
	// with FromScope, one installation-wide `all:<thing>:<role>` holding
	// that one scope's admins.
	AdminMirror struct {
		Thing     string
		Role      string
		FromScope string
	}

	// RungGrant is a group held by named rungs: `<scope>:<thing>:<role>`, on
	// each of Scopes that has a cluster, or on every scope of the matrix
	// with a cluster when Scopes is empty. A rung the model does not
	// declare feeds nobody.
	RungGrant struct {
		Thing  string
		Role   string
		Rungs  []string
		Scopes []string
	}
)

// DatabaseLevelNames are the database levels, in ladder order.
func (c Conventions) DatabaseLevelNames() []string {
	out := make([]string, 0, len(c.DatabaseLevels))
	for _, level := range c.DatabaseLevels {
		out = append(out, level.Name)
	}

	return out
}

// databaseLevelOf is the database level a generic level opens.
func (c Conventions) databaseLevelOf(generic string) (string, bool) {
	for _, level := range c.DatabaseLevels {
		if level.From == generic {
			return level.Name, true
		}
	}

	return "", false
}

// unitHoldsDatabaseLevel reports whether the deploy population holds this
// database level on this scope.
func (c Conventions) unitHoldsDatabaseLevel(scope, level string) bool {
	return c.UnitDatabaseLevel != "" && level == c.UnitDatabaseLevel && slices.Contains(c.UnitDatabaseScopes, scope)
}

// own reports whether an address is in the installation's own directory.
func (c Conventions) own(address string) bool {
	return c.OwnDirectory == nil || c.OwnDirectory(address)
}

// GroupName names a grant: `<scope>:<thing>:<role>`.
func GroupName(scope, thing, role string) string {
	return scope + policy.Separator + thing + policy.Separator + role
}
