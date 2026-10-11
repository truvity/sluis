package console

import (
	"context"
	"net/url"
	"time"

	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/policy"
)

// githubTimeout bounds one call to GitHub on a request path.
const githubTimeout = 15 * time.Second

// githubBegin is what starting any connect returns: where the browser
// goes, the manifest it POSTs there when the App is still to be created,
// and what the state both are pinned to binds. The URL carries
// [consolewire.StatePlaceholder] where the issuer's signed state belongs.
type githubBegin struct {
	url, manifest, state string
	bind, owner          string
}

func (b githubBegin) wire() consolewire.Begun {
	return consolewire.Begun{URL: b.url, Manifest: b.manifest, Bind: b.bind, Owner: b.owner}
}

// githubDisconnect is what disconnecting any App returns.
type githubDisconnect struct {
	uninstalled bool
	detail      string
	settingsURL string
	// invalidated is how many links became unverifiable. The link App
	// alone.
	invalidated int
}

// uninstall is the revoke every Disconnect makes: GitHub's API cannot
// delete an App, so the registration stays for its owner to delete and
// nothing can act through it once the installation is gone.
func (c *Service) uninstall(ctx context.Context, appID, installationID int64, key string) githubDisconnect {
	var out githubDisconnect
	if key == "" || installationID == 0 {
		out.detail = "The App was never installed, so there was nothing to uninstall."
		return out
	}
	token, err := githubapp.AppToken(appID, key, time.Now())
	if err == nil {
		err = githubapp.DeleteInstallation(ctx, c.githubHTTP(), token, installationID)
	}
	if err != nil {
		out.detail = "The App could not be uninstalled, so uninstall it on GitHub: " + err.Error()
		return out
	}
	out.uninstalled = true
	return out
}

// appSettingsURL is where an organisation's owner edits or deletes an App.
func appSettingsURL(org, slug string) string {
	if org == "" || slug == "" {
		return ""
	}
	return githubapp.WebBase + "/organizations/" + url.PathEscape(org) + "/settings/apps/" + url.PathEscape(slug)
}

// orgAppName is the configured name of an organisation's App, or empty.
func (c *Service) orgAppName(org string) string {
	if c.deps.OrgAppName == nil {
		return ""
	}
	return c.deps.OrgAppName(org)
}

// binding is one organisation's side of the policy.
type binding struct {
	members []string
	ignore  []string
	teams   map[string]policy.TeamView
}

// boundOrganisations collects the policy's GitHub table by organisation.
// An organisation binding only teams has no org-level row, so both views
// are read.
func boundOrganisations(set *policy.Set) map[string]*binding {
	out := map[string]*binding{}
	get := func(org string) *binding {
		if out[org] == nil {
			out[org] = &binding{teams: map[string]policy.TeamView{}}
		}
		return out[org]
	}
	for _, team := range set.GitHubTeams() {
		get(team.Org).teams[team.Team] = team
	}
	for _, org := range set.GitHubOrgs() {
		get(org.Org).members = org.Members
		get(org.Org).ignore = org.Ignore
	}
	return out
}
