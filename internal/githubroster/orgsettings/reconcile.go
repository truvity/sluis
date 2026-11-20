// Package orgsettings reconciles one GitHub organisation's Actions
// variables, Actions secrets and code security configurations with the
// typed document the organisation keeps in its own account (ADR 0080).
//
// The package reads the document through a [Reader], checks it against the
// owner's ceiling with policy/tenant, and compares it with what GitHub
// holds. In `report` mode it says what differs and changes nothing. In
// `enforce` mode it applies the difference. It removes only a name it wrote
// itself and recorded in [status.Managed], and it never holds a secret's
// value beyond the one call that seals it: values are not logged, not
// stored and not recorded in the audit trail.
package orgsettings

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/orgconfig"
	"github.com/truvity/sluis/internal/githubroster/status"
	"github.com/truvity/sluis/policy/tenant"
)

// Mode is what the controller does with a difference.
type Mode = orgconfig.Mode

// The modes.
const (
	ModeReport  = orgconfig.ModeReport
	ModeEnforce = orgconfig.ModeEnforce
)

// Secrets reads the values the document's secrets refer to, from the
// organisation's own parameter store.
type Secrets interface {
	// Version is the parameter's version, read without decrypting it.
	Version(ctx context.Context, parameter string) (int64, error)
	// Value is the parameter's decrypted value and its version.
	Value(ctx context.Context, parameter string) (string, int64, error)
}

// Target is one organisation, with the installation token to act with.
type Target struct {
	Org   githubapp.Org
	Token string
}

// Desired is what the organisation should hold: the platform's variables and
// the document's, merged and checked. Variable and secret names are keyed as
// GitHub keeps them, folded to upper case ([tenant.FoldName]).
type Desired struct {
	Variables map[string]tenant.OrgVariable
	Secrets   map[string]tenant.OrgSecret
	Configs   map[string]tenant.SecurityConfiguration
	// Platform are the variables the installation sets, which the controller
	// writes whatever GitHub already holds under the name.
	Platform map[string]bool
}

// Input is one pass over one organisation.
type Input struct {
	Mode    Mode
	Target  Target
	Desired Desired
	// Ceiling says which names the platform owns: the controller never
	// removes one the installation does not set.
	Ceiling tenant.OrgCeiling
	// Previous is the record of what the controller wrote last time.
	Previous status.Managed
	Secrets  Secrets
}

// Result is what a pass found or did, and the record to keep.
type Result struct {
	Changes []status.SettingChange
	Managed status.Managed
}

// Reconcile compares the desired settings with GitHub's and, in enforce
// mode, applies the difference. An error means GitHub could not be read, so
// nothing was changed; a change that GitHub refuses is a row of the result
// and the pass goes on.
//
// A name GitHub holds that the controller did not write is the organisation's
// and not the controller's: it is reported as a conflict and never written,
// adopted or removed, except for the installation's own variables. A pass
// that would remove most of what the controller wrote removes none of it.
func Reconcile(ctx context.Context, in Input) (Result, error) {
	in.Desired = folded(in.Desired)
	in.Previous = foldedRecord(in.Previous)

	r := &run{in: in, enforce: in.Mode == ModeEnforce}
	r.managed = status.Managed{
		Variables:              slices.Clone(in.Previous.Variables),
		Secrets:                maps.Clone(in.Previous.Secrets),
		SecurityConfigurations: slices.Clone(in.Previous.SecurityConfigurations),
	}

	if r.managed.Secrets == nil {
		r.managed.Secrets = map[string]int64{}
	}

	r.wrote = struct{ vars, cfgs map[string]bool }{vars: setOf(in.Previous.Variables), cfgs: setOf(in.Previous.SecurityConfigurations)}

	if err := r.read(ctx); err != nil {
		return Result{}, err
	}

	r.variables(ctx)
	r.secrets(ctx)
	r.configurations(ctx)
	r.removals(ctx)

	slices.Sort(r.managed.Variables)
	slices.Sort(r.managed.SecurityConfigurations)
	r.managed.Variables = slices.Compact(r.managed.Variables)
	r.managed.SecurityConfigurations = slices.Compact(r.managed.SecurityConfigurations)

	if len(r.managed.Secrets) == 0 {
		r.managed.Secrets = nil
	}

	slices.SortStableFunc(r.changes, func(a, b status.SettingChange) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Action, b.Action))
	})

	return Result{Changes: r.changes, Managed: r.managed}, nil
}

func setOf(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}

	return out
}

// folded keys the variables and secrets by the name GitHub keeps.
func folded(d Desired) Desired {
	out := Desired{
		Variables: map[string]tenant.OrgVariable{}, Secrets: map[string]tenant.OrgSecret{}, Configs: d.Configs,
		Platform: map[string]bool{},
	}

	for name, v := range d.Variables {
		out.Variables[tenant.FoldName(name)] = v
	}

	for name, s := range d.Secrets {
		out.Secrets[tenant.FoldName(name)] = s
	}

	for name := range d.Platform {
		out.Platform[tenant.FoldName(name)] = true
	}

	return out
}

func foldedRecord(m status.Managed) status.Managed {
	out := status.Managed{SecurityConfigurations: m.SecurityConfigurations}

	for _, n := range m.Variables {
		out.Variables = append(out.Variables, tenant.FoldName(n))
	}

	// foo and FOO are one name to GitHub and must not queue two removals.
	slices.Sort(out.Variables)
	out.Variables = slices.Compact(out.Variables)

	if m.Secrets != nil {
		out.Secrets = make(map[string]int64, len(m.Secrets))
		for n, v := range m.Secrets {
			out.Secrets[tenant.FoldName(n)] = v
		}
	}

	return out
}

// run is one pass.
type run struct {
	in      Input
	enforce bool
	managed status.Managed
	changes []status.SettingChange
	// wrote are the variables and configurations of the record.
	wrote struct{ vars, cfgs map[string]bool }

	repos    []githubapp.Repo
	byName   map[string]int64
	archived map[int64]bool
	vars     map[string]githubapp.Variable
	secs     map[string]githubapp.Secret
	cfgs     map[string]githubapp.SecurityConfiguration
	// defaults are the configurations that are the organisation's default for
	// new repositories, by id, read when the document states one.
	defaults map[int64]string
	// written is the version of the parameter the last secret written was
	// read from.
	written int64
}

func (r *run) org() githubapp.Org { return r.in.Target.Org }
func (r *run) tok() string        { return r.in.Target.Token }

// conflict is the reason of a setting GitHub holds that the controller did
// not write.
const conflict = "GitHub holds it and the controller did not write it: remove it on GitHub to let the controller manage it"

// read takes everything the pass compares against, or fails whole: a
// partial read acted on is how a setting is wrongly created or removed.
func (r *run) read(ctx context.Context) error {
	var err error

	vars, err := r.org().Variables(ctx, r.tok())
	if err != nil {
		return err //nolint:wrapcheck // the client names the call
	}

	r.vars = map[string]githubapp.Variable{}
	for _, v := range vars {
		r.vars[tenant.FoldName(v.Name)] = v
	}

	secs, err := r.org().Secrets(ctx, r.tok())
	if err != nil {
		return err //nolint:wrapcheck // the client names the call
	}

	r.secs = map[string]githubapp.Secret{}
	for _, s := range secs {
		r.secs[tenant.FoldName(s.Name)] = s
	}

	cfgs, err := r.org().SecurityConfigurations(ctx, r.tok())
	if err != nil {
		return err //nolint:wrapcheck // the client names the call
	}

	r.cfgs = map[string]githubapp.SecurityConfiguration{}
	for _, c := range cfgs {
		r.cfgs[c.Name] = c
	}

	if r.statesDefault() {
		if r.defaults, err = r.org().ConfigurationDefaults(ctx, r.tok()); err != nil {
			return err //nolint:wrapcheck // the client names the call
		}
	}

	if r.usesRepositories() {
		if r.repos, err = r.org().Repositories(ctx, r.tok()); err != nil {
			return err //nolint:wrapcheck // the client names the call
		}

		r.byName = make(map[string]int64, len(r.repos))
		r.archived = map[int64]bool{}

		for _, repo := range r.repos {
			r.byName[strings.ToLower(repo.Name)] = repo.ID
			r.archived[repo.ID] = repo.Archived
		}
	}

	return nil
}

// statesDefault says whether a configuration of the document states what is the
// default for new repositories. One that does not leaves the default alone, so
// the pass does not even read it.
func (r *run) statesDefault() bool {
	for _, c := range r.in.Desired.Configs {
		if c.Attach.DefaultForNewRepos != "" {
			return true
		}
	}

	return false
}

// usesRepositories says whether the pass needs the organisation's
// repositories: some setting is selected, or a configuration is attached.
func (r *run) usesRepositories() bool {
	for _, v := range r.in.Desired.Variables {
		if v.Visibility == tenant.VisibilitySelected {
			return true
		}
	}

	for _, s := range r.in.Desired.Secrets {
		if s.Visibility == tenant.VisibilitySelected {
			return true
		}
	}

	return len(r.in.Desired.Configs) > 0
}

func (r *run) record(kind, name, action, state, reason string) {
	r.changes = append(r.changes, status.SettingChange{Kind: kind, Name: name, Action: action, State: state, Reason: reason})
}

// act records a change that was made or, in report mode, would be: apply is
// called only when enforcing, and its error makes the row failed.
func (r *run) act(kind, name, action string, apply func() error) bool {
	if !r.enforce {
		r.record(kind, name, action, status.SettingDrift, "")

		return false
	}

	if err := apply(); err != nil {
		r.record(kind, name, action, status.SettingFailed, err.Error())

		return false
	}

	r.record(kind, name, action, status.SettingApplied, "")

	return true
}

// resolve turns repository names into ids. A name the organisation has no
// repository for is counted as missing, and the setting is held.
func (r *run) resolve(names []string) (ids []int64, missing int) {
	for _, name := range names {
		if id, ok := r.byName[strings.ToLower(name)]; ok {
			ids = append(ids, id)
		} else {
			missing++
		}
	}

	slices.Sort(ids)

	return ids, missing
}

func idsOf(repos []githubapp.Repo) []int64 {
	out := make([]int64, 0, len(repos))
	for _, repo := range repos {
		out = append(out, repo.ID)
	}

	slices.Sort(out)

	return out
}

const heldMissing = "a repository the document names does not exist in the organisation"

// variables creates or updates each desired variable.
func (r *run) variables(ctx context.Context) {
	d := r.in.Desired

	for _, name := range slices.Sorted(maps.Keys(d.Variables)) {
		want := d.Variables[name]

		var (
			ids     []int64
			missing int
		)

		if want.Visibility == tenant.VisibilitySelected {
			ids, missing = r.resolve(want.Repositories)
		}

		got, exists := r.vars[name]
		action := status.SettingCreate

		if exists {
			action = status.SettingUpdate
		}

		switch {
		case missing > 0:
			r.record(status.KindVariable, name, action, status.SettingHeld, heldMissing)

			continue
		case exists && !d.Platform[name] && !r.wrote.vars[name]:
			// Somebody else's variable: the controller leaves it be, and
			// says so when the document would have it differ.
			if r.variableDiffers(ctx, got, want, ids) {
				r.record(status.KindVariable, name, action, status.SettingConflict, conflict)
			}

			continue
		case exists && !r.variableDiffers(ctx, got, want, ids):
			continue
		}

		v := githubapp.Variable{Name: name, Value: want.Value, Visibility: want.Visibility}

		done := r.act(status.KindVariable, name, action, func() error {
			if exists {
				return r.org().UpdateVariable(ctx, r.tok(), v, ids) //nolint:wrapcheck // the client names the call
			}

			return r.org().CreateVariable(ctx, r.tok(), v, ids) //nolint:wrapcheck // the client names the call
		})
		if done {
			r.managed.Variables = append(r.managed.Variables, name)
		}
	}
}

func (r *run) variableDiffers(ctx context.Context, got githubapp.Variable, want tenant.OrgVariable, ids []int64) bool {
	if got.Value != want.Value || got.Visibility != want.Visibility {
		return true
	}

	if want.Visibility != tenant.VisibilitySelected {
		return false
	}

	have, err := r.org().VariableRepositories(ctx, r.tok(), got.Name)

	return err != nil || !slices.Equal(idsOf(have), ids)
}

// secrets writes each desired secret that is missing, differently shared, or
// whose parameter has a new version since the controller last wrote it. A
// secret GitHub holds and the controller did not write is a conflict: its
// value cannot be compared, so it is never overwritten.
func (r *run) secrets(ctx context.Context) {
	d := r.in.Desired

	for _, name := range slices.Sorted(maps.Keys(d.Secrets)) {
		want := d.Secrets[name]
		got, exists := r.secs[name]
		_, wrote := r.in.Previous.Secrets[name]
		action := status.SettingCreate

		if exists {
			action = status.SettingUpdate
		}

		if exists && !wrote {
			r.record(status.KindSecret, name, action, status.SettingConflict, conflict)

			continue
		}

		var (
			ids     []int64
			missing int
		)

		if want.Visibility == tenant.VisibilitySelected {
			ids, missing = r.resolve(want.Repositories)
		}

		if missing > 0 {
			r.record(status.KindSecret, name, action, status.SettingHeld, heldMissing)

			continue
		}

		version, err := r.in.Secrets.Version(ctx, want.Parameter)
		if err != nil {
			r.record(status.KindSecret, name, action, status.SettingHeld, "read the parameter "+want.Parameter+": "+err.Error())

			continue
		}

		if exists && !r.secretDiffers(ctx, name, got, want, ids, version) {
			continue
		}

		done := r.act(status.KindSecret, name, action, func() error { return r.writeSecret(ctx, name, want, ids) })
		if done {
			r.managed.Secrets[name] = r.written
		}
	}
}

func (r *run) secretDiffers(ctx context.Context, name string, got githubapp.Secret, want tenant.OrgSecret, ids []int64, version int64) bool {
	if got.Visibility != want.Visibility || r.in.Previous.Secrets[name] != version {
		return true
	}

	if want.Visibility != tenant.VisibilitySelected {
		return false
	}

	have, err := r.org().SecretRepositories(ctx, r.tok(), got.Name)

	return err != nil || !slices.Equal(idsOf(have), ids)
}

// writeSecret reads the value, seals it for GitHub and writes it. The value
// lives in this function only.
func (r *run) writeSecret(ctx context.Context, name string, want tenant.OrgSecret, ids []int64) error {
	value, version, err := r.in.Secrets.Value(ctx, want.Parameter)
	if err != nil {
		return errors.New("read the parameter " + want.Parameter + ": " + err.Error())
	}

	key, err := r.org().SecretsPublicKey(ctx, r.tok())
	if err != nil {
		return err //nolint:wrapcheck // the client names the call
	}

	sealed, err := key.Seal([]byte(value))
	if err != nil {
		return err //nolint:wrapcheck // the client names the call
	}

	if err = r.org().PutSecret(ctx, r.tok(), name, sealed, key, want.Visibility, ids); err != nil {
		return err //nolint:wrapcheck // the client names the call
	}

	r.written = version

	return nil
}

// configurations creates or updates each desired configuration and brings
// its attachment to the document's. One GitHub holds that the controller did
// not write is left as it is and reported when it differs from the document.
func (r *run) configurations(ctx context.Context) {
	d := r.in.Desired

	for _, name := range slices.Sorted(maps.Keys(d.Configs)) {
		want := d.Configs[name]
		got, exists := r.cfgs[name]

		var (
			ids     []int64
			missing int
		)

		if want.Attach.Scope == tenant.ScopeSelected {
			ids, missing = r.resolve(want.Attach.Repositories)
		}

		if missing > 0 {
			action := status.SettingCreate
			if exists {
				action = status.SettingUpdate
			}

			r.record(status.KindSecurityConfiguration, name, action, status.SettingHeld, heldMissing)

			continue
		}

		if exists && !r.wrote.cfgs[name] {
			missingIDs, extra, _, err := r.attachmentDiff(ctx, got.ID, true, want, ids)
			if err != nil || configurationDiffers(got, want) || len(missingIDs) > 0 || len(extra) > 0 || r.defaultDiffers(got.ID, want) {
				r.record(status.KindSecurityConfiguration, name, status.SettingUpdate, status.SettingConflict, conflict)
			}

			continue
		}

		id := got.ID

		switch {
		case !exists:
			created := r.act(status.KindSecurityConfiguration, name, status.SettingCreate, func() error {
				var err error

				id, err = r.org().CreateSecurityConfiguration(ctx, r.tok(), name, want.Description, want.Settings)

				return err //nolint:wrapcheck // the client names the call
			})

			if created {
				r.managed.SecurityConfigurations = append(r.managed.SecurityConfigurations, name)
			} else if r.enforce {
				// Refused: there is nothing to attach.
				continue
			}
		case configurationDiffers(got, want):
			done := r.act(status.KindSecurityConfiguration, name, status.SettingUpdate, func() error {
				return r.org().UpdateSecurityConfiguration(ctx, r.tok(), id, name, want.Description, want.Settings) //nolint:wrapcheck // the client names the call
			})
			if done {
				r.managed.SecurityConfigurations = append(r.managed.SecurityConfigurations, name)
			}
		}

		r.attachment(ctx, name, id, exists, want, ids)
		r.defaultForNew(ctx, name, id, want)
	}
}

// defaultSuffix names the row of a configuration's default for new
// repositories beside the rows of the configuration itself.
const defaultSuffix = ":default_for_new_repos"

// defaultDiffers says whether the document states a default for new
// repositories that GitHub does not hold for the configuration. One the
// document leaves out never differs.
func (r *run) defaultDiffers(id int64, want tenant.SecurityConfiguration) bool {
	value := want.Attach.DefaultForNewRepos
	if value == "" {
		return false
	}

	live := r.defaults[id]
	if live == "" {
		live = tenant.DefaultNone
	}

	return live != value
}

// defaultForNew makes the configuration the default for new repositories, as
// the document says, when GitHub holds another. It is reached only for a
// configuration the controller wrote, as every write of the pass is. A document
// that says nothing leaves the default as it is.
func (r *run) defaultForNew(ctx context.Context, name string, id int64, want tenant.SecurityConfiguration) {
	if !r.defaultDiffers(id, want) {
		return
	}

	r.act(status.KindSecurityConfiguration, name+defaultSuffix, status.SettingUpdate, func() error {
		return r.org().SetConfigurationDefault(ctx, r.tok(), id, want.Attach.DefaultForNewRepos) //nolint:wrapcheck // the client names the call
	})
}

func configurationDiffers(got githubapp.SecurityConfiguration, want tenant.SecurityConfiguration) bool {
	if got.Description != want.Description {
		return true
	}

	for key, value := range want.Settings {
		if got.Settings[key] != value {
			return true
		}
	}

	return false
}

// attached is the status of a repository that holds a configuration or is
// about to.
func attached(status string) bool {
	switch status {
	case "attached", "attaching", "updating", "enforced":
		return true
	}

	return false
}

// attachmentDiff is the repositories a configuration is missing from and the
// ones it holds that the document does not name. An archived repository is
// neither: it is never asked for, so asking would not end, and never detached,
// since the controller did not choose it. The archived ones that hold the
// configuration are the third answer, for the report.
func (r *run) attachmentDiff(ctx context.Context, id int64, exists bool, want tenant.SecurityConfiguration, ids []int64) (missing, extra, archived []int64, err error) {
	wanted := ids
	if want.Attach.Scope == tenant.ScopeAll {
		live := make([]githubapp.Repo, 0, len(r.repos))
		for _, repo := range r.repos {
			if !repo.Archived {
				live = append(live, repo)
			}
		}

		wanted = idsOf(live)
	}

	var have []int64

	if exists {
		rows, err := r.org().ConfigurationRepositories(ctx, r.tok(), id)
		if err != nil {
			return nil, nil, nil, err //nolint:wrapcheck // the client names the call
		}

		for _, row := range rows {
			if attached(row.Status) {
				have = append(have, row.ID)

				if r.archived[row.ID] {
					archived = append(archived, row.ID)
				}
			}
		}

		slices.Sort(have)
	}

	wanted = slices.DeleteFunc(slices.Clone(wanted), func(id int64) bool { return r.archived[id] })

	missing = difference(wanted, have)
	extra = slices.DeleteFunc(difference(have, wanted), func(id int64) bool { return r.archived[id] })

	slices.Sort(archived)

	return missing, extra, archived, nil
}

// attachment brings the repositories a configuration is attached to to the
// document's: it attaches what is missing and, for a selected attachment,
// detaches what the document no longer names.
func (r *run) attachment(ctx context.Context, name string, id int64, exists bool, want tenant.SecurityConfiguration, ids []int64) {
	missing, extra, archived, err := r.attachmentDiff(ctx, id, exists, want, ids)
	if err != nil {
		r.record(status.KindSecurityConfiguration, name, status.SettingAttach, status.SettingHeld, err.Error())

		return
	}

	if len(missing) > 0 {
		// Always as a selected set, even for scope all: GitHub's own scope
		// `all` takes archived repositories too, and the document's `all` is
		// every repository that is not archived.
		r.act(status.KindSecurityConfiguration, name, status.SettingAttach, func() error {
			return r.org().AttachSecurityConfiguration(ctx, r.tok(), id, tenant.ScopeSelected, missing) //nolint:wrapcheck // the client names the call
		})
	}

	if len(archived) > 0 {
		r.record(status.KindSecurityConfiguration, name, status.SettingDetach, status.SettingHeld, r.archivedHolding(archived))
	}

	if len(extra) > 0 {
		r.act(status.KindSecurityConfiguration, name, status.SettingDetach, func() error {
			return r.org().DetachSecurityConfigurations(ctx, r.tok(), extra) //nolint:wrapcheck // the client names the call
		})
	}
}

// archivedHolding says which archived repositories hold a configuration that
// the document does not attach to them: an earlier pass attached them with
// GitHub's scope `all`. The controller leaves them, and the row stays until
// somebody detaches them or unarchives them.
func (r *run) archivedHolding(ids []int64) string {
	names := make([]string, 0, len(ids))

	for _, repo := range r.repos {
		if slices.Contains(ids, repo.ID) {
			names = append(names, repo.Name)
		}
	}

	slices.Sort(names)

	shown := names
	if len(shown) > maxArchivedNamed {
		shown = shown[:maxArchivedNamed]
	}

	text := strings.Join(shown, ", ")
	if len(names) > len(shown) {
		text += fmt.Sprintf(" and %d more", len(names)-len(shown))
	}

	return fmt.Sprintf("archived repositories hold it (%s): the controller does not attach an archived repository and does not detach one; "+
		"detach them on GitHub to clear this row", text)
}

// maxArchivedNamed is how many archived repositories a row names.
const maxArchivedNamed = 5

// difference is the ids in a that are not in b, both sorted.
func difference(a, b []int64) []int64 {
	var out []int64

	for _, id := range a {
		if _, found := slices.BinarySearch(b, id); !found {
			out = append(out, id)
		}
	}

	return out
}

// removal is one name the controller wrote and the document no longer names.
type removal struct {
	kind, name string
	remove     func() error
	forget     func()
}

// documentOwned counts the recorded names the document owns and GitHub still
// holds: what a removal could take. The installation's variables are always
// wanted, and a name GitHub no longer holds is nothing to remove, so neither
// is in the count that the mass-removal guard weighs.
func (r *run) documentOwned() int {
	n := 0

	for _, name := range r.in.Previous.Variables {
		if _, exists := r.vars[name]; exists && !r.in.Desired.Platform[name] {
			n++
		}
	}

	for name := range r.in.Previous.Secrets {
		if _, exists := r.secs[name]; exists {
			n++
		}
	}

	for _, name := range r.in.Previous.SecurityConfigurations {
		if _, exists := r.cfgs[name]; exists {
			n++
		}
	}

	return n
}

// removals deletes what the controller wrote and the document no longer
// names. Nothing else is ever deleted. A recorded name that is not a name
// GitHub accepts, or is one the platform owns and the installation does not
// set, is not trusted: the record is storage, and storage can be wrong.
// A pass that would remove more than half of the names the document owns, and
// at least two, removes none and says so: a document that lost its
// lines is more likely than an organisation that meant to drop most of them.
func (r *run) removals(ctx context.Context) {
	d := r.in.Desired

	var due []removal

	total := r.documentOwned()

	for _, name := range r.in.Previous.Variables {
		if _, wanted := d.Variables[name]; wanted {
			continue
		}

		name := name

		switch {
		case !tenant.ValidActionsName(name):
			r.record(status.KindVariable, name, status.SettingDelete, status.SettingHeld, "the record names something that is not a variable name: not removed")
			r.forget(&r.managed.Variables, name)
		case r.in.Ceiling.PlatformOwns(name) && !d.Platform[name]:
			r.record(status.KindVariable, name, status.SettingDelete, status.SettingHeld, "a name the platform owns and the installation does not set: not removed")
			r.forget(&r.managed.Variables, name)
		default:
			if _, exists := r.vars[name]; !exists {
				r.forget(&r.managed.Variables, name)

				continue
			}

			due = append(due, removal{status.KindVariable, name,
				func() error { return r.org().DeleteVariable(ctx, r.tok(), r.vars[name].Name) }, //nolint:wrapcheck // the client names the call
				func() { r.forget(&r.managed.Variables, name) }})
		}
	}

	for _, name := range slices.Sorted(maps.Keys(r.in.Previous.Secrets)) {
		if _, wanted := d.Secrets[name]; wanted {
			continue
		}

		name := name

		switch {
		case !tenant.ValidActionsName(name):
			r.record(status.KindSecret, name, status.SettingDelete, status.SettingHeld, "the record names something that is not a secret name: not removed")
			delete(r.managed.Secrets, name)
		default:
			if _, exists := r.secs[name]; !exists {
				delete(r.managed.Secrets, name)

				continue
			}

			due = append(due, removal{status.KindSecret, name,
				func() error { return r.org().DeleteSecret(ctx, r.tok(), r.secs[name].Name) }, //nolint:wrapcheck // the client names the call
				func() { delete(r.managed.Secrets, name) }})
		}
	}

	for _, name := range r.in.Previous.SecurityConfigurations {
		if _, wanted := d.Configs[name]; wanted {
			continue
		}

		name := name

		cfg, exists := r.cfgs[name]

		switch {
		case !exists:
			r.forget(&r.managed.SecurityConfigurations, name)
		case strings.TrimSpace(name) != name || name == "":
			r.record(status.KindSecurityConfiguration, name, status.SettingDelete, status.SettingHeld,
				"the record names something that is not a configuration name: not removed")
			r.forget(&r.managed.SecurityConfigurations, name)
		default:
			due = append(due, removal{status.KindSecurityConfiguration, name,
				func() error { return r.org().DeleteSecurityConfiguration(ctx, r.tok(), cfg.ID) }, //nolint:wrapcheck // the client names the call
				func() { r.forget(&r.managed.SecurityConfigurations, name) }})
		}
	}

	if len(due) >= 2 && len(due)*2 > total {
		for _, d := range due {
			r.record(d.kind, d.name, status.SettingDelete, status.SettingHeld,
				fmt.Sprintf("removal held: %d of the %d names the document owns would be removed at once; "+
					"restore the document, or remove them on GitHub", len(due), total))
		}

		return
	}

	for _, d := range due {
		if r.act(d.kind, d.name, status.SettingDelete, d.remove) {
			d.forget()
		}
	}
}

func (r *run) forget(list *[]string, name string) {
	*list = slices.DeleteFunc(*list, func(n string) bool { return n == name })
}
