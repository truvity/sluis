package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubroster/appid"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/githubroster/status"
)

// GitHubApp is one declared GitHub App of any purpose: the one list
// `apps.github.apps` (ADR 0072) that replaces the separate catalogue and runner
// tiers.
//
// A `catalogue` App carries the whole of [catalogue.App]. A `runner` entry
// declares a tier an operator may make a runner App for, in one organisation
// (`org`) or in any (no `org`), and its Apps are `runner-<tier>-<org>`. The
// `link` entry is the App that links a person's GitHub account; it has the id
// `link` and declares only its labels and whether it is exported.
type GitHubApp struct {
	// ID is the App's key in storage: `link`, a catalogue App's own id, or
	// `runner-<tier>` (`runner-<tier>-<org>` with an `org`), which a runner entry
	// may leave out.
	ID      string            `yaml:"id,omitempty"`
	Purpose appid.Purpose     `yaml:"purpose"`
	Labels  map[string]string `yaml:"labels,omitempty"`
	// Export places the installed App's key at external/github/<id>. A runner
	// App is always exported.
	Export bool `yaml:"export,omitempty"`
	// Org is the organisation a catalogue App is created under, or a runner
	// entry's only organisation.
	Org  string `yaml:"org,omitempty"`
	Tier string `yaml:"tier,omitempty"`

	// The rest is a catalogue App's alone.
	Name         string             `yaml:"name,omitempty"`
	Description  string             `yaml:"description,omitempty"`
	Public       bool               `yaml:"public,omitempty"`
	Permissions  map[string]string  `yaml:"permissions,omitempty"`
	Events       []string           `yaml:"events,omitempty"`
	Webhook      *catalogue.Webhook `yaml:"webhook,omitempty"`
	Installation string             `yaml:"installation,omitempty"`
	Grants       []catalogue.Grant  `yaml:"grants,omitempty"`
}

// Catalogue is the entry as a catalogue App.
func (a *GitHubApp) Catalogue() catalogue.App {
	return catalogue.App{
		ID: a.ID, Org: a.Org, Name: a.Name, Description: a.Description, Public: a.Public, Permissions: a.Permissions,
		Events: a.Events, Webhook: a.Webhook, Installation: a.Installation, Export: a.Export, Grants: a.Grants,
	}
}

// catalogueEntry is a catalogue App declared the old way, as an entry.
func catalogueEntry(app *catalogue.App) GitHubApp {
	return GitHubApp{
		ID: app.ID, Purpose: appid.Catalogue, Org: app.Org, Name: app.Name, Description: app.Description, Public: app.Public,
		Permissions: app.Permissions, Events: app.Events, Webhook: app.Webhook, Installation: app.Installation, Export: app.Export,
		Grants: app.Grants,
	}
}

// runnerID is the id of a runner entry: `runner-<tier>`, and with an
// organisation `runner-<tier>-<org>`.
func (a *GitHubApp) runnerID() string {
	if a.Org == "" {
		return appid.RunnerPrefix + a.Tier
	}
	return appid.RunnerID(a.Tier, a.Org)
}

// foldLegacy folds the sections this release still reads, `runnerTiers` and
// `catalogue`, into the list and empties them, so everything after the load
// sees the list only. It reports how many entries it made.
func (a *AppsGitHub) foldLegacy() int {
	n := 0
	for _, tier := range a.RunnerTiers {
		if tier = strings.TrimSpace(tier); tier != "" {
			a.Apps = append(a.Apps, GitHubApp{Purpose: appid.Runner, Tier: tier})
			n++
		}
	}
	for i := range a.Catalogue {
		a.Apps = append(a.Apps, catalogueEntry(&a.Catalogue[i]))
		n++
	}
	a.RunnerTiers, a.Catalogue = nil, nil
	a.legacy += n
	return n
}

// FoldLegacy folds the retired sections of the GitHub Apps into the list. The
// sections are read for one release (removed in v1.77, like the layout they come
// with); the canonical document `sluisctl policy render` writes carries the list
// only. How many entries it made is [PolicyDocument.LegacyAppEntries], for the
// process to warn on.
func (a *PolicyApps) FoldLegacy() {
	if a != nil && a.GitHub != nil {
		a.GitHub.foldLegacy()
	}
}

// LegacyAppEntries is how many entries of apps.github.apps came from the retired
// runnerTiers and catalogue sections: nonzero means the document is to be moved
// to the list.
func (d *PolicyDocument) LegacyAppEntries() int {
	if d == nil || d.Apps == nil || d.Apps.GitHub == nil {
		return 0
	}
	return d.Apps.GitHub.legacy
}

// GitHubApps are the declared GitHub Apps of every purpose, in declaration
// order.
func (d *PolicyDocument) GitHubApps() []GitHubApp {
	if d == nil || d.Apps == nil || d.Apps.GitHub == nil {
		return nil
	}
	return d.Apps.GitHub.Apps
}

// GitHubAppLabels are the labels declared for the link or catalogue App with
// the id, or nil.
func (d *PolicyDocument) GitHubAppLabels(id string) map[string]string {
	apps := d.GitHubApps()
	for i := range apps {
		a := &apps[i]
		if a.Purpose != appid.Runner && a.ID == id {
			return maps.Clone(a.Labels)
		}
	}
	return nil
}

// RunnerLabels are the labels of the runner App of a tier in an organisation:
// those of the entry for the tier in any organisation, and then those of the
// entry for this organisation, which win.
func (d *PolicyDocument) RunnerLabels(tier, org string) map[string]string {
	var out map[string]string
	for _, pass := range []string{"", org} {
		apps := d.GitHubApps()
		for i := range apps {
			a := &apps[i]
			if a.Purpose == appid.Runner && a.Tier == tier && a.Org == pass && len(a.Labels) > 0 {
				if out == nil {
					out = map[string]string{}
				}
				maps.Copy(out, a.Labels)
			}
		}
	}
	return out
}

// GitHubAppExported reports whether the link or catalogue App with the id is
// declared `export: true`. A runner App is always exported and is not asked
// here.
func (d *PolicyDocument) GitHubAppExported(id string) bool {
	apps := d.GitHubApps()
	for i := range apps {
		a := &apps[i]
		if a.Purpose != appid.Runner && a.ID == id {
			return a.Export
		}
	}
	return false
}

// AppRef is the id of the App an organisation uses (`app_ref` of its record),
// or empty when the configuration names none.
func (d *PolicyDocument) AppRef(org string) string {
	if d == nil || d.Controllers == nil || d.Controllers.GitHub == nil {
		return ""
	}
	return d.Controllers.GitHub.AppRefs[org]
}

// validateGitHubApps holds the list to its rules, and says everything wrong at
// once.
func (d *PolicyDocument) validateGitHubApps() []error {
	var errs []error
	ids := map[string]int{}
	links := 0
	for i := range d.GitHubApps() {
		app := &d.Apps.GitHub.Apps[i]
		where := fmt.Sprintf("apps.github.apps[%d]", i)
		if app.ID != "" {
			where += " (" + app.ID + ")"
		}
		fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(where+": "+format, args...)) }
		if err := appid.CheckLabels(app.Labels); err != nil {
			fail("%w", err)
		}
		switch app.Purpose {
		case appid.Link:
			links++
			if app.ID != appid.LinkID {
				fail("the link App's id is %q", appid.LinkID)
			}
			if app.Tier != "" || app.hasCatalogueFields() {
				fail("the link App declares only labels, export and org")
			}
		case appid.Runner:
			if !runnerapp.ValidTier(app.Tier) {
				fail("tier %q is not lower-case letters, digits and dashes, at most 16", app.Tier)
			}
			if app.Org != "" && !status.ValidOrg(app.Org) {
				fail("org %q is not an organisation login", app.Org)
			}
			if app.ID != "" && app.ID != app.runnerID() {
				fail("a runner entry's id is %q", app.runnerID())
			}
			if app.hasCatalogueFields() {
				fail("a runner entry declares only tier, org, labels and export: a runner App is always exported")
			}
		case appid.Catalogue:
			if app.Tier != "" {
				fail("tier belongs to a runner entry")
			}
			if err := appid.CheckCatalogueID(app.ID); err != nil {
				fail("%w", err)
			}
			entry := app.Catalogue()
			if err := entry.Validate(); err != nil {
				fail("%w", err)
			}
		default:
			fail("purpose %q is none of link, catalogue and runner", app.Purpose)
			continue
		}
		id := app.ID
		if app.Purpose == appid.Runner {
			id = app.runnerID()
		}
		if first, dup := ids[id]; dup {
			fail("id %q is declared twice (apps[%d])", id, first)
		}
		ids[id] = i
	}
	if links > 1 {
		errs = append(errs, errors.New("apps.github.apps: there is one link App"))
	}
	// A grant naming a group the policy does not declare would read as though
	// somebody may ask for a token, and nobody could.
	if undeclared := d.GitHubCatalogue().UndeclaredGroups(func(g string) bool { _, ok := d.Policy.Groups[g]; return ok }); len(undeclared) > 0 {
		errs = append(errs, fmt.Errorf("apps.github.apps: grants name groups the policy does not declare: %s", strings.Join(undeclared, "; ")))
	}
	return errs
}

// hasCatalogueFields reports whether the entry declares what only a catalogue
// App has.
func (a *GitHubApp) hasCatalogueFields() bool {
	return a.Name != "" || a.Description != "" || a.Public || len(a.Permissions) > 0 || len(a.Events) > 0 ||
		a.Webhook != nil || a.Installation != "" || len(a.Grants) > 0
}

// validateAppRefs holds `controllers.github.appRefs` to the list: an
// organisation the policy binds names a catalogue App declared for it.
func (d *PolicyDocument) validateAppRefs() []error {
	if d.Controllers == nil || d.Controllers.GitHub == nil {
		return nil
	}
	var errs []error
	for _, org := range slices.Sorted(maps.Keys(d.Controllers.GitHub.AppRefs)) {
		ref := d.Controllers.GitHub.AppRefs[org]
		if _, bound := d.Policy.GitHub[org]; !bound {
			errs = append(errs, fmt.Errorf("controllers.github.appRefs names %s, which the policy's github table does not bind", org))
		}
		var found *GitHubApp
		apps := d.GitHubApps()
		for i := range apps {
			if apps[i].Purpose == appid.Catalogue && apps[i].ID == ref {
				found = &apps[i]
			}
		}
		switch {
		case found == nil:
			errs = append(errs, fmt.Errorf("controllers.github.appRefs: %s names %q, which is not a catalogue App in apps.github.apps", org, ref))
		case !strings.EqualFold(found.Org, org):
			errs = append(errs, fmt.Errorf("controllers.github.appRefs: %s names %q, which is created under %s", org, ref, found.Org))
		}
	}
	return errs
}
