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
// the document's, merged and checked.
type Desired struct {
	Variables map[string]tenant.OrgVariable
	Secrets   map[string]tenant.OrgSecret
	Configs   map[string]tenant.SecurityConfiguration
}

// Input is one pass over one organisation.
type Input struct {
	Mode    Mode
	Target  Target
	Desired Desired
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
func Reconcile(ctx context.Context, in Input) (Result, error) {
	r := &run{in: in, enforce: in.Mode == ModeEnforce}
	r.managed = status.Managed{
		Variables:              slices.Clone(in.Previous.Variables),
		Secrets:                maps.Clone(in.Previous.Secrets),
		SecurityConfigurations: slices.Clone(in.Previous.SecurityConfigurations),
	}
	if r.managed.Secrets == nil {
		r.managed.Secrets = map[string]int64{}
	}

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

// run is one pass.
type run struct {
	in      Input
	enforce bool
	managed status.Managed
	changes []status.SettingChange

	repos  []githubapp.Repo
	byName map[string]int64
	vars   map[string]githubapp.Variable
	secs   map[string]githubapp.Secret
	cfgs   map[string]githubapp.SecurityConfiguration
	// written is the version of the parameter the last secret written was
	// read from.
	written int64
}

func (r *run) org() githubapp.Org { return r.in.Target.Org }
func (r *run) tok() string        { return r.in.Target.Token }

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
		r.vars[v.Name] = v
	}

	secs, err := r.org().Secrets(ctx, r.tok())
	if err != nil {
		return err //nolint:wrapcheck // the client names the call
	}

	r.secs = map[string]githubapp.Secret{}
	for _, s := range secs {
		r.secs[s.Name] = s
	}

	cfgs, err := r.org().SecurityConfigurations(ctx, r.tok())
	if err != nil {
		return err //nolint:wrapcheck // the client names the call
	}

	r.cfgs = map[string]githubapp.SecurityConfiguration{}
	for _, c := range cfgs {
		r.cfgs[c.Name] = c
	}

	if r.usesRepositories() {
		if r.repos, err = r.org().Repositories(ctx, r.tok()); err != nil {
			return err //nolint:wrapcheck // the client names the call
		}

		r.byName = make(map[string]int64, len(r.repos))
		for _, repo := range r.repos {
			r.byName[repo.Name] = repo.ID
		}
	}

	return nil
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
// repository for is returned as missing, and the setting is held.
func (r *run) resolve(names []string) (ids []int64, missing []string) {
	for _, name := range names {
		if id, ok := r.byName[name]; ok {
			ids = append(ids, id)
		} else {
			missing = append(missing, name)
		}
	}

	slices.Sort(ids)
	slices.Sort(missing)

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

func heldNames(missing []string) string {
	return "the organisation has no repository " + strings.Join(missing, ", ")
}

// variables creates or updates each desired variable.
func (r *run) variables(ctx context.Context) {
	d := r.in.Desired

	for _, name := range slices.Sorted(maps.Keys(d.Variables)) {
		want := d.Variables[name]

		var (
			ids     []int64
			missing []string
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
		case len(missing) > 0:
			r.record(status.KindVariable, name, action, status.SettingHeld, heldNames(missing))

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
// whose parameter has a new version since the controller last wrote it.
func (r *run) secrets(ctx context.Context) {
	d := r.in.Desired

	for _, name := range slices.Sorted(maps.Keys(d.Secrets)) {
		want := d.Secrets[name]
		got, exists := r.secs[name]
		action := status.SettingCreate

		if exists {
			action = status.SettingUpdate
		}

		var (
			ids     []int64
			missing []string
		)

		if want.Visibility == tenant.VisibilitySelected {
			ids, missing = r.resolve(want.Repositories)
		}

		if len(missing) > 0 {
			r.record(status.KindSecret, name, action, status.SettingHeld, heldNames(missing))

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

	have, err := r.org().SecretRepositories(ctx, r.tok(), name)

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
// its attachment to the document's.
func (r *run) configurations(ctx context.Context) {
	d := r.in.Desired

	for _, name := range slices.Sorted(maps.Keys(d.Configs)) {
		want := d.Configs[name]
		got, exists := r.cfgs[name]

		var (
			ids     []int64
			missing []string
		)

		if want.Attach.Scope == tenant.ScopeSelected {
			ids, missing = r.resolve(want.Attach.Repositories)
		}

		if len(missing) > 0 {
			action := status.SettingCreate
			if exists {
				action = status.SettingUpdate
			}

			r.record(status.KindSecurityConfiguration, name, action, status.SettingHeld, heldNames(missing))

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
	}
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

// attachment brings the repositories a configuration is attached to to the
// document's: it attaches what is missing and, for a selected attachment,
// detaches what the document no longer names.
func (r *run) attachment(ctx context.Context, name string, id int64, exists bool, want tenant.SecurityConfiguration, ids []int64) {
	wanted := ids
	if want.Attach.Scope == tenant.ScopeAll {
		// An archived repository takes no configuration, and would be reported
		// as missing for ever.
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
			r.record(status.KindSecurityConfiguration, name, status.SettingAttach, status.SettingHeld, err.Error())

			return
		}

		for _, row := range rows {
			if attached(row.Status) {
				have = append(have, row.ID)
			}
		}

		slices.Sort(have)
	}

	missing := difference(wanted, have)
	extra := difference(have, wanted)

	if len(missing) > 0 {
		r.act(status.KindSecurityConfiguration, name, status.SettingAttach, func() error {
			return r.org().AttachSecurityConfiguration(ctx, r.tok(), id, want.Attach.Scope, missing) //nolint:wrapcheck // the client names the call
		})
	}

	if len(extra) > 0 {
		r.act(status.KindSecurityConfiguration, name, status.SettingDetach, func() error {
			return r.org().DetachSecurityConfigurations(ctx, r.tok(), extra) //nolint:wrapcheck // the client names the call
		})
	}
}

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

// removals deletes what the controller wrote and the document no longer
// names. Nothing else is ever deleted.
func (r *run) removals(ctx context.Context) {
	d := r.in.Desired

	for _, name := range r.in.Previous.Variables {
		if _, wanted := d.Variables[name]; wanted {
			continue
		}

		if _, exists := r.vars[name]; !exists {
			r.forget(&r.managed.Variables, name)

			continue
		}

		if r.act(status.KindVariable, name, status.SettingDelete, func() error {
			return r.org().DeleteVariable(ctx, r.tok(), name) //nolint:wrapcheck // the client names the call
		}) {
			r.forget(&r.managed.Variables, name)
		}
	}

	for _, name := range slices.Sorted(maps.Keys(r.in.Previous.Secrets)) {
		if _, wanted := d.Secrets[name]; wanted {
			continue
		}

		if _, exists := r.secs[name]; !exists {
			delete(r.managed.Secrets, name)

			continue
		}

		if r.act(status.KindSecret, name, status.SettingDelete, func() error {
			return r.org().DeleteSecret(ctx, r.tok(), name) //nolint:wrapcheck // the client names the call
		}) {
			delete(r.managed.Secrets, name)
		}
	}

	for _, name := range r.in.Previous.SecurityConfigurations {
		if _, wanted := d.Configs[name]; wanted {
			continue
		}

		cfg, exists := r.cfgs[name]
		if !exists {
			r.forget(&r.managed.SecurityConfigurations, name)

			continue
		}

		if r.act(status.KindSecurityConfiguration, name, status.SettingDelete, func() error {
			return r.org().DeleteSecurityConfiguration(ctx, r.tok(), cfg.ID) //nolint:wrapcheck // the client names the call
		}) {
			r.forget(&r.managed.SecurityConfigurations, name)
		}
	}
}

func (r *run) forget(list *[]string, name string) {
	*list = slices.DeleteFunc(*list, func(n string) bool { return n == name })
}
