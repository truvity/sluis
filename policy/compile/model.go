package compile

type (
	// Model is an installation's access model, resolved: every population
	// named in its own file replaced by the directory groups and addresses
	// it stands for. [Roster] and [BuildVocabulary] read this and nothing
	// else.
	Model struct {
		// Rungs are the access ladder, by rung name.
		Rungs map[string]Rung
		// Scopes is the matrix: scope, then rung, to a generic level. A
		// level the conventions do not map grants nothing on that tier.
		Scopes map[string]map[string]string
		// ScopeFacts says what each scope has. Nil takes every scope at its
		// word: each has a cluster and databases. Set, a scope missing from
		// it has neither.
		ScopeFacts map[string]ScopeFacts
		// Projects are the business projects, by name.
		Projects map[string]Project
		// Applications grant an application's own internal group,
		// `<scope>:<thing>:<role>`, to a population directly, outside the
		// rung ladder.
		Applications map[string]Population
		// GitHub binds organisations' members and teams to directory
		// groups, by organisation login.
		GitHub map[string]GitHubOrg
		// Slack binds channels to directory groups, by workspace key.
		Slack map[string]SlackWorkspace
		// Jobs are CI jobs, keyed by the internal group each becomes.
		Jobs map[string]GitHubJob
		// Workloads are the installation's own programs, keyed by the
		// internal group each becomes.
		Workloads map[string]Workload
		// Employees is the people registry: slug to every address the
		// person holds. Nil mints no employee groups.
		Employees map[string][]string
	}

	// Rung is one rung of the ladder.
	Rung struct {
		// Groups are the directory groups whose members hold the rung.
		Groups []string
		// Users are individual addresses holding it, the exception shape.
		Users []string
		// SessionDuration is ISO-8601, `PT8H`: how long a token held by
		// this rung lives. Only the hour shape is read ([Duration]).
		SessionDuration string
	}

	// Population is a set of directory groups and, as the exception,
	// individual addresses.
	Population struct {
		Groups []string
		Users  []string
	}

	// ScopeFacts is what one scope has.
	ScopeFacts struct {
		// Kubernetes: the scope has a cluster, so the cluster tier and
		// everything bound per namespace exist there.
		Kubernetes bool
		// Database: the scope has project databases, so the database
		// levels exist there.
		Database bool
	}

	// Project is one business project's delivery authority.
	Project struct {
		// Scopes are the scopes the project runs on.
		Scopes []string
		// SecretScopes are the scopes where the project keeps secrets and
		// runs nothing. Disjoint from Scopes.
		SecretScopes []string
		// ViewerScopes are the scopes on which the project mints a viewer.
		ViewerScopes []string
		// Deployers hold `<scope>:<project>:deployer` on every scope.
		Deployers []string
		// Approvers hold `<scope>:<project>:approver`. Empty falls back to
		// Deployers.
		Approvers []string
		// Viewers hold the viewer on a secret scope: directory groups.
		Viewers []string
		// ViewerUsers, when set, hold the viewer on a running scope in
		// place of the scope's cluster viewers.
		ViewerUsers []string
	}

	// GitHubOrg is one GitHub organisation's bindings. Every member is a
	// directory group, so the directory vouches for each holder exactly as
	// it does for a rung.
	GitHubOrg struct {
		// Members belong in the organisation without a team.
		Members []string `yaml:"members,omitempty"`
		// Teams, keyed by slug, never the display name.
		Teams map[string]GitHubTeam `yaml:"teams,omitempty"`
		// Ignore are work addresses and GitHub logins the controller
		// leaves alone in this organisation whatever the bindings say.
		Ignore []string `yaml:"ignore,omitempty"`
	}

	// GitHubTeam is one team's two roles.
	GitHubTeam struct {
		Members     []string `yaml:"members,omitempty"`
		Maintainers []string `yaml:"maintainers,omitempty"`
	}

	// SlackWorkspace is one Slack workspace's bindings. The workspace's
	// team id, email domains and owning directory are deliberately absent:
	// they are recorded when the workspace is connected, not declared.
	SlackWorkspace struct {
		// Channels keyed by the channel name as Slack spells it.
		Channels map[string]SlackChannel `yaml:"channels,omitempty"`
	}

	// SlackChannel is one channel's binding.
	SlackChannel struct {
		// Private is what the controller creates, and what an existing
		// channel of that name must already be.
		Private bool `yaml:"private,omitempty"`
		// Mode is "extend" (add only, the default) or "strict" (add and
		// remove; private channels only).
		Mode string `yaml:"mode,omitempty"`
		// Ignore are addresses or Slack user ids a strict channel never
		// removes.
		Ignore []string `yaml:"ignore,omitempty"`
		// Members are the directory groups whose holders belong in the
		// channel. At least one.
		Members []string `yaml:"members,omitempty"`
		// Adopt optionally names an existing channel by id.
		Adopt string `yaml:"adopt,omitempty"`
	}
)

// AllScopes is every scope the project holds a grant on: the ones it runs
// on and the ones where it only keeps secrets, sorted and without repeats,
// so a render is the same twice.
func (p Project) AllScopes() []string {
	return sortedUnique(append(append([]string{}, p.Scopes...), p.SecretScopes...))
}
