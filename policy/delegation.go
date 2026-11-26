package policy

import (
	"fmt"
	"reflect"
	"slices"
)

// DelegatedPerson is the one kind of subject a [Delegation] may name today:
// a person's access token this issuer signed, under a sign-in that still
// stands.
const DelegatedPerson = "person"

// DelegatedSubjects are the subject kinds a [Delegation] accepts.
var DelegatedSubjects = []string{DelegatedPerson}

// Delegation lets a named workload act for somebody else at one client: an
// RFC 8693 token exchange whose `subject_token` is the caller's access token
// and whose `actor_token` is the workload's own proof. The token it mints is
// the subject's, with the subject's current groups, and names the workload
// in `act`.
//
// It is about the target client, as `requires` is: a client that declares
// none accepts no delegated exchange at all, and one that does accepts it
// only from the actors it names, and only for the subject kinds it names.
// It grants the actor nothing of its own: the subject must still be admitted
// by the client's `requires`, and the actor's own exchange limits still
// apply.
type Delegation struct {
	// Actors are the identities that may act for a subject, each one
	// [Matcher] matched against the actor's proof alone, as an exchange
	// limit's is. Only a machine proof acts: a `service_account`, `github`
	// or `aws` matcher.
	Actors []Matcher `yaml:"actors"`
	// Subjects are the kinds of subject such an actor may act for. Absent
	// is [DelegatedPerson], the only kind there is.
	Subjects []string `yaml:"subjects,omitempty"`
}

// AdmitsActor reports whether in, an actor's proof, is one of the actors.
// A nil delegation admits nobody.
func (d *Delegation) AdmitsActor(in Input) bool {
	if d == nil {
		return false
	}
	return slices.ContainsFunc(d.Actors, func(m Matcher) bool { return m.matches(in) })
}

// AllowsSubject reports whether a subject of this kind may be acted for.
func (d *Delegation) AllowsSubject(kind string) bool {
	if d == nil {
		return false
	}
	if len(d.Subjects) == 0 {
		return kind == DelegatedPerson
	}
	return slices.Contains(d.Subjects, kind)
}

// AllowedSubjects is [Delegation.Subjects] with its default filled in.
func (d *Delegation) AllowedSubjects() []string {
	if d == nil {
		return nil
	}
	if len(d.Subjects) == 0 {
		return []string{DelegatedPerson}
	}
	return slices.Clone(d.Subjects)
}

// validate checks the rule at load. An empty actor list would read as a
// delegation nobody can use, and an address as an actor could never match,
// because a person is never an actor: both are typos found only when a
// workload's calls start failing, so both are refused here.
func (d *Delegation) validate(id string) error {
	if d == nil {
		return nil
	}
	where := fmt.Sprintf("client %q: delegation", id)
	if len(d.Actors) == 0 {
		return fmt.Errorf("%s names no actors: a delegation names the workloads that may act for a subject", where)
	}
	for i := range d.Actors {
		m := d.Actors[i]
		at := fmt.Sprintf("%s: actors[%d]", where, i)
		if err := m.validate(at); err != nil {
			return err
		}
		if m.Email != "" || m.EmailDomain != "" {
			return fmt.Errorf("%s matches a person: an actor is a workload, CI job or AWS role, never a sign-in", at)
		}
		for j := range i {
			if reflect.DeepEqual(m, d.Actors[j]) {
				return fmt.Errorf("%s repeats actors[%d]", at, j)
			}
		}
	}
	seen := map[string]bool{}
	for _, kind := range d.Subjects {
		if !slices.Contains(DelegatedSubjects, kind) {
			return fmt.Errorf("%s: subjects: %q is not one of %v", where, kind, DelegatedSubjects)
		}
		if seen[kind] {
			return fmt.Errorf("%s: subjects: %q is listed twice", where, kind)
		}
		seen[kind] = true
	}
	return nil
}
