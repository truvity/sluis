package compile

import (
	"fmt"
	"strings"

	"github.com/truvity/sluis/policy"
)

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

	// Project is one business project's delivery authority. The key it
	// sits under in [Model.Projects] is its thing: one word (`shop`), or
	// `<vendor>:<project>:<customer>` (`finttech:landing:ie`), see
	// [ValidThing].
	Project struct {
		// Alias is the project's old one-word thing, optional. While it is
		// set the roster emits every group of the project under BOTH
		// things, with the same members, and the vocabulary declares both,
		// so a client's `requires` and `groups` may name either form during
		// the move. Dropping the alias drops the old groups. Empty changes
		// nothing.
		Alias string
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

// Things are the things the project's groups are named under: its key,
// then its alias when it has one. key is the project's row in
// [Model.Projects].
func (p Project) Things(key string) []string {
	if p.Alias == "" {
		return []string{key}
	}

	return []string{key, p.Alias}
}

// ValidThing reports whether s can be a project's thing: one word or exactly
// three colon-separated words, none empty. It is [policy.ValidThing].
func ValidThing(s string) bool { return policy.ValidThing(s) }

// ValidateProjects refuses a project whose key is not a thing, whose alias
// is not one word, or whose alias is the key of, or the alias of, another
// project: two projects would then mint the same groups. where names the
// table in the messages (`projects` gives `projects[<name>]: ...`).
func ValidateProjects(where string, projects map[string]Project) []error {
	var errs []error

	owner := map[string]string{}

	for _, key := range sortedKeys(projects) {
		owner[key] = key
	}

	for _, key := range sortedKeys(projects) {
		row := fmt.Sprintf("%s[%s]", where, key)

		if !ValidThing(key) {
			errs = append(errs, fmt.Errorf("%s: the key must be one word or <vendor>:<project>:<customer>, three words", row))
		}

		alias := projects[key].Alias

		switch {
		case alias == "":
		case alias == key:
			errs = append(errs, fmt.Errorf("%s: alias %q is the key itself", row, alias))
		case strings.Contains(alias, policy.Separator) || strings.TrimSpace(alias) != alias:
			errs = append(errs, fmt.Errorf("%s: alias %q must be one word, the project's old thing", row, alias))
		default:
			if other, taken := owner[alias]; taken {
				errs = append(errs, fmt.Errorf("%s: alias %q is already the thing of project %q", row, alias, other))

				continue
			}

			owner[alias] = key
		}
	}

	return errs
}

// AllScopes is every scope the project holds a grant on: the ones it runs
// on and the ones where it only keeps secrets, sorted and without repeats,
// so a render is the same twice.
func (p Project) AllScopes() []string {
	return sortedUnique(append(append([]string{}, p.Scopes...), p.SecretScopes...))
}
