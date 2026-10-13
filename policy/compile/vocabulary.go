package compile

import (
	"fmt"
	"slices"
	"strings"

	"github.com/truvity/sluis/policy"
)

// The policy's OPTIONAL `vocabulary` table: which scopes and things the
// installation recognises, and each thing's role ladder. Declaring one moves
// the issuer from "any three-segment name in `groups` is a grant" to "a
// typo, an undeclared role or a stray scope is refused at load".
//
// Two halves build it: the HAND-WRITTEN half (scopes, and the ladders of the
// things nothing derives), and the GENERATED half, one thing per business
// project, per CI job's repository, per GitHub team or organisation binding
// and per Slack channel, read from the same tables [Roster] reads, so the
// two never drift. An installation adds families of its own with
// [Family].

// The families [BuildVocabulary] generates, named in its collision error:
// naming both sides is what makes the message actionable.
const (
	FamilyHandWritten = "the hand-written vocabulary"
	FamilyProjects    = "a generated business project"
	FamilyJobs        = "a generated CI job's repository/caller"
	FamilyGitHub      = "a generated GitHub team/org binding"
	FamilySlack       = "a generated Slack channel binding"
)

// Family is one more generated family an installation adds to the
// vocabulary: Name says where it comes from, for the collision error.
type Family struct {
	Name   string
	Things map[string]policy.ThingSpec
}

// vocabularyBuilder accumulates things by name, refusing any collision
// between two families.
type vocabularyBuilder struct {
	things map[string]policy.ThingSpec
	family map[string]string
}

// add merges spec into the builder under name, attributed to family.
func (b *vocabularyBuilder) add(name, family string, spec policy.ThingSpec) error {
	if existing, taken := b.family[name]; taken {
		return fmt.Errorf("thing %q is declared by both %s and %s -- generated vocabulary families must not name the same thing",
			name, existing, family)
	}

	b.things[name] = spec
	b.family[name] = family

	return nil
}

// BuildVocabulary derives the generated half of the vocabulary and merges it
// onto the hand-written half, or returns nil when hand is nil: the opt-in
// the issuer itself observes. extra are the installation's own families,
// added last. The assembled table goes through the policy's own validation,
// the implies-graph cycle check and the coverage rule, which need both
// halves to run.
func BuildVocabulary(m Model, c Conventions, hand *policy.Vocabulary, extra ...Family) (*policy.Vocabulary, error) {
	if hand == nil {
		return nil, nil
	}

	scopes := make(map[string]policy.ScopeSpec, len(hand.Scopes))
	for name, spec := range hand.Scopes {
		scopes[name] = spec
	}

	b := &vocabularyBuilder{things: map[string]policy.ThingSpec{}, family: map[string]string{}}

	for _, name := range sortedKeys(hand.Things) {
		if err := b.add(name, FamilyHandWritten, hand.Things[name]); err != nil {
			return nil, err
		}
	}

	steps := []func(Model, Conventions, *vocabularyBuilder) error{
		vocabularyProjects, vocabularyJobs, vocabularyGitHub, vocabularySlack,
	}

	for _, step := range steps {
		if err := step(m, c, b); err != nil {
			return nil, err
		}
	}

	for _, family := range extra {
		for _, name := range sortedKeys(family.Things) {
			if err := b.add(name, family.Name, family.Things[name]); err != nil {
				return nil, err
			}
		}
	}

	if err := checkVocabularyScopes(scopes, b.things); err != nil {
		return nil, err
	}

	assembled := &policy.Vocabulary{Scopes: scopes, Things: b.things}

	// A Policy with nothing but a Vocabulary validates every other table as
	// empty, so this checks the vocabulary's own shape and nothing else.
	if err := (&policy.Policy{Version: 1, Vocabulary: assembled}).Validate(); err != nil {
		return nil, err //nolint:wrapcheck // the caller names the table
	}

	return assembled, nil
}

// vocabularyProjects derives one thing per business project: deployer and
// approver unrestricted, the database levels and viewer restricted to
// exactly the scopes [Roster] would ever grant them on. Where a project
// declares a viewer, each of the conventions' ViewerImpliedBy roles implies
// it, but only where the coverage rule allows the edge.
func vocabularyProjects(m Model, c Conventions, b *vocabularyBuilder) error {
	for _, name := range sortedKeys(m.Projects) {
		project := m.Projects[name]

		var thingScopes []string

		for _, scope := range project.AllScopes() {
			if m.hasKubernetes(scope) {
				thingScopes = append(thingScopes, scope)
			}
		}

		if len(thingScopes) == 0 {
			continue
		}

		roles := map[string]policy.RoleSpec{
			ProjectDeployer: {},
			ProjectApprover: {},
		}

		for _, level := range c.DatabaseLevelNames() {
			if scopes := vocabularyDatabaseLevelScopes(m, c, project, level); len(scopes) > 0 {
				roles[level] = restrictedRole(scopes, thingScopes)
			}
		}

		if len(project.ViewerScopes) > 0 {
			var viewer []string

			for _, scope := range project.ViewerScopes {
				if m.hasKubernetes(scope) {
					viewer = append(viewer, scope)
				}
			}

			if len(viewer) > 0 {
				roles[ProjectViewer] = restrictedRole(viewer, thingScopes)
			}
		}

		for _, role := range c.MachineReaderRoles {
			if scopes := vocabularyMachineReaderScopes(m, name, role); len(scopes) > 0 {
				roles[role] = restrictedRole(scopes, thingScopes)
			}
		}

		addViewerImplies(roles, c.ViewerImpliedBy, thingScopes)

		if err := b.add(name, FamilyProjects, policy.ThingSpec{Scopes: sortedUnique(thingScopes), Roles: roles}); err != nil {
			return err
		}
	}

	return nil
}

// addViewerImplies declares `<role>: [viewer]` on every role of impliedBy
// that roles declares and whose effective scopes are a subset of the
// viewer's. The coverage rule refuses an edge whose target covers less
// ground than its source, so such a role gets no edge at all.
func addViewerImplies(roles map[string]policy.RoleSpec, impliedBy, thingScopes []string) {
	viewer, ok := roles[ProjectViewer]
	if !ok {
		return
	}

	viewerScopes := effectiveRoleScopes(viewer, thingScopes)

	for _, role := range impliedBy {
		spec, ok := roles[role]
		if !ok {
			continue
		}

		if !isSubset(effectiveRoleScopes(spec, thingScopes), viewerScopes) {
			continue
		}

		spec.Implies = append(slices.Clone(spec.Implies), ProjectViewer)
		roles[role] = spec
	}
}

// effectiveRoleScopes is the role's own scopes when it restricts itself, or
// every scope of the thing otherwise.
func effectiveRoleScopes(spec policy.RoleSpec, thingScopes []string) []string {
	if len(spec.Scopes) > 0 {
		return spec.Scopes
	}

	return thingScopes
}

// isSubset reports whether every element of a is in b.
func isSubset(a, b []string) bool {
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}

	return true
}

// vocabularyDatabaseLevelScopes are the scopes the project runs on, with a
// database, where some rung holds the generic level this database level
// maps to, or where the deploy population holds it.
func vocabularyDatabaseLevelScopes(m Model, c Conventions, project Project, level string) []string {
	var out []string

	for _, scope := range project.AllScopes() {
		if !m.hasKubernetes(scope) || !slices.Contains(project.Scopes, scope) || !m.hasDatabase(scope) {
			continue
		}

		if c.unitHoldsDatabaseLevel(scope, level) {
			out = append(out, scope)

			continue
		}

		for _, generic := range m.Scopes[scope] {
			if got, ok := c.databaseLevelOf(generic); ok && got == level {
				out = append(out, scope)

				break
			}
		}
	}

	return out
}

// vocabularyMachineReaderScopes are the scopes on which a workload holds
// `<scope>:<project>:<role>`: a hand-declared row, so the vocabulary admits
// exactly the scopes it claims.
func vocabularyMachineReaderScopes(m Model, project, role string) []string {
	var out []string

	for _, name := range sortedKeys(m.Workloads) {
		scope, thing, workloadRole, ok := policy.SplitGroup(name)
		if !ok || workloadRole != role || thing != project {
			continue
		}

		out = append(out, scope)
	}

	return out
}

// restrictedRole declares a role on exactly roleScopes, or unrestricted when
// that is every scope the thing declares: the two are equivalent to the
// loader, and leaving Scopes nil keeps the render as plain as the data
// allows.
func restrictedRole(roleScopes, thingScopes []string) policy.RoleSpec {
	sorted := sortedUnique(roleScopes)
	if len(sorted) == len(sortedUnique(thingScopes)) {
		return policy.RoleSpec{}
	}

	return policy.RoleSpec{Scopes: sorted}
}

// vocabularyJobs derives one thing per repository/caller of the CI jobs:
// its scopes are every organisation a job under that thing uses, its roles
// every role a job under it declares, flat.
func vocabularyJobs(m Model, _ Conventions, b *vocabularyBuilder) error {
	type accum struct {
		scopes map[string]bool
		roles  map[string]bool
	}

	byThing := map[string]*accum{}

	for _, key := range sortedKeys(m.Jobs) {
		scope, thing, role, ok := policy.SplitGroup(key)
		if !ok {
			continue
		}

		a, known := byThing[thing]
		if !known {
			a = &accum{scopes: map[string]bool{}, roles: map[string]bool{}}
			byThing[thing] = a
		}

		a.scopes[scope] = true
		a.roles[role] = true
	}

	for _, thing := range sortedKeys(byThing) {
		a := byThing[thing]

		roles := make(map[string]policy.RoleSpec, len(a.roles))
		for role := range a.roles {
			roles[role] = policy.RoleSpec{}
		}

		if err := b.add(thing, FamilyJobs, policy.ThingSpec{Scopes: sortedKeys(a.scopes), Roles: roles}); err != nil {
			return err
		}
	}

	return nil
}

// vocabularyGitHub derives one thing per GitHub team or organisation
// binding: the organisation's own membership is the `org` thing, each team
// slug its own, both with member and maintainer declared whichever is
// populated: the vocabulary is a catalogue of what MAY be granted. Flat: a
// maintainer is not nested in the team's member group, so an implies edge
// would hand every maintainer a grant they do not hold.
func vocabularyGitHub(m Model, _ Conventions, b *vocabularyBuilder) error {
	orgScopes := map[string]bool{}
	teamScopes := map[string]map[string]bool{}

	for _, org := range sortedKeys(m.GitHub) {
		binding := m.GitHub[org]

		if len(binding.Members) > 0 {
			orgScopes[org] = true
		}

		for _, slug := range sortedKeys(binding.Teams) {
			team := binding.Teams[slug]
			if len(team.Members) == 0 && len(team.Maintainers) == 0 {
				continue
			}

			if teamScopes[slug] == nil {
				teamScopes[slug] = map[string]bool{}
			}

			teamScopes[slug][org] = true
		}
	}

	if len(orgScopes) > 0 {
		if err := b.add(GitHubOrgThing, FamilyGitHub, policy.ThingSpec{
			Scopes: sortedKeys(orgScopes),
			Roles:  map[string]policy.RoleSpec{GitHubMember: {}},
		}); err != nil {
			return err
		}
	}

	for _, slug := range sortedKeys(teamScopes) {
		if err := b.add(slug, FamilyGitHub, policy.ThingSpec{
			Scopes: sortedKeys(teamScopes[slug]),
			Roles: map[string]policy.RoleSpec{
				GitHubMember:     {},
				GitHubMaintainer: {},
			},
		}); err != nil {
			return err
		}
	}

	return nil
}

// vocabularySlack derives one thing per Slack channel name, in every
// workspace that binds one of that name, with the one flat role.
func vocabularySlack(m Model, _ Conventions, b *vocabularyBuilder) error {
	scopes := map[string]map[string]bool{}

	for _, workspace := range sortedKeys(m.Slack) {
		for name := range m.Slack[workspace].Channels {
			if scopes[name] == nil {
				scopes[name] = map[string]bool{}
			}

			scopes[name][workspace] = true
		}
	}

	for _, name := range sortedKeys(scopes) {
		if err := b.add(SlackThing(name), FamilySlack, policy.ThingSpec{
			Scopes: sortedKeys(scopes[name]),
			Roles:  map[string]policy.RoleSpec{SlackMember: {}},
		}); err != nil {
			return err
		}
	}

	return nil
}

// checkVocabularyScopes refuses a thing or role naming an undeclared scope,
// the loader's own rule, caught here with a message that names the thing.
func checkVocabularyScopes(scopes map[string]policy.ScopeSpec, things map[string]policy.ThingSpec) error {
	for _, name := range sortedKeys(things) {
		thing := things[name]

		for _, scope := range thing.Scopes {
			if _, ok := scopes[scope]; !ok {
				return fmt.Errorf("thing %q names scope %q, which the vocabulary's scopes do not declare", name, scope)
			}
		}

		for _, role := range sortedKeys(thing.Roles) {
			for _, scope := range thing.Roles[role].Scopes {
				if _, ok := scopes[scope]; !ok {
					return fmt.Errorf("thing %q: role %q names scope %q, which the vocabulary's scopes do not declare", name, role, scope)
				}
			}
		}
	}

	return nil
}

// ValidateHandWrittenVocabulary checks the hand-written half on its own,
// before anything generated is merged onto it: every scope and thing name
// is non-blank, every thing declares a scope and a role, a role's own scopes
// are a non-empty subset of its thing's, and every implies edge names a
// role of the same thing. A nil vocabulary validates as nothing.
//
// It is not the full rule set: the cycle check and the coverage rule need
// the generated things too, and [BuildVocabulary] runs them. This gives an
// author a fast, local error for the mistakes that do not need the whole
// picture.
func ValidateHandWrittenVocabulary(v *policy.Vocabulary) error {
	if v == nil {
		return nil
	}

	for _, scope := range sortedKeys(v.Scopes) {
		if strings.TrimSpace(scope) == "" {
			return fmt.Errorf("scopes names a blank scope")
		}
	}

	for _, name := range sortedKeys(v.Things) {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("things names a blank thing")
		}

		if err := validateHandWrittenThing(v, name); err != nil {
			return err
		}
	}

	return nil
}

func validateHandWrittenThing(v *policy.Vocabulary, name string) error {
	thing := v.Things[name]

	if len(thing.Scopes) == 0 {
		return fmt.Errorf("things[%s] declares no scopes", name)
	}

	for _, scope := range thing.Scopes {
		if _, ok := v.Scopes[scope]; !ok {
			return fmt.Errorf("things[%s] names scope %q, which scopes does not declare", name, scope)
		}
	}

	if len(thing.Roles) == 0 {
		return fmt.Errorf("things[%s] declares no roles", name)
	}

	for _, role := range sortedKeys(thing.Roles) {
		if strings.TrimSpace(role) == "" {
			return fmt.Errorf("things[%s] names a blank role", name)
		}

		spec := thing.Roles[role]

		if spec.Scopes != nil && len(spec.Scopes) == 0 {
			return fmt.Errorf("things[%s]: role %q declares an empty scopes list", name, role)
		}

		for _, scope := range spec.Scopes {
			if !slices.Contains(thing.Scopes, scope) {
				return fmt.Errorf("things[%s]: role %q names scope %q, which is not among its own scopes %v", name, role, scope, thing.Scopes)
			}
		}

		for _, implied := range spec.Implies {
			if _, ok := thing.Roles[implied]; !ok {
				return fmt.Errorf("things[%s]: role %q implies %q, which is not a declared role of %q", name, role, implied, name)
			}
		}
	}

	return nil
}
