// The GitHub Apps handler: every kind of App — the link App, an
// organisation's controller App, a tier's runner App and a catalogue App —
// is one App with a purpose, listed, read, checked, created, installed and
// disconnected by the calls below. The per-kind calls that came before
// (GetGitHubStatus's catalogue and runner sections, the catalogue and runner
// Begin, Disconnect and Check) are served from the same code, so a client
// written for them reads what it always read.

package server

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
)

// BeginGitHubAppConnect starts creating any App, or finishing installing
// one created before.
//
// It hands the work to the same code the per-kind calls run, so the
// signed state, the cookie it is pinned to and the callback that finishes
// the flow are the ones that kind of App has always used: a browser
// part-way through a flow is unaffected by which call started it.
func (c *Console) BeginGitHubAppConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubAppConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubAppConnectResponse], error) {
	app, err := c.operableApp(ctx, strings.TrimSpace(req.Msg.GetId()))
	if err != nil {
		return nil, err
	}
	id, _ := IdentityFrom(ctx)
	var begun githubBegin
	switch app.GetPurpose() {
	case appLink:
		begun, err = c.beginLinkAppConnect(ctx, id.Who(), strings.TrimSpace(req.Msg.GetOwner()))
	case appController:
		begun, err = c.beginOrganisationConnect(ctx, id, app.GetOrg(), req.Msg.GetOwnerDirectory())
	case appRunners:
		begun, err = c.beginRunnerAppConnect(ctx, app.GetOrg(), app.GetTier())
	default:
		begun, err = c.beginCatalogueAppConnect(ctx, app.GetId())
	}
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubAppConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

// DisconnectGitHubApp uninstalls any App, then forgets it.
func (c *Console) DisconnectGitHubApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubAppRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubAppResponse], error) {
	app, err := c.operableApp(ctx, strings.TrimSpace(req.Msg.GetId()))
	if err != nil {
		return nil, err
	}
	var gone githubDisconnect
	switch app.GetPurpose() {
	case appLink:
		gone, err = c.disconnectLinkApp(ctx)
	case appController:
		gone, err = c.disconnectOrganisation(ctx, app.GetOrg())
	case appRunners:
		gone, err = c.disconnectRunnerApp(ctx, app.GetOrg(), app.GetTier())
	default:
		gone, err = c.disconnectCatalogueApp(ctx, app.GetId())
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&directoryrosterv1.DisconnectGitHubAppResponse{
		Uninstalled:    gone.uninstalled,
		Detail:         gone.detail,
		AppSettingsUrl: gone.settingsURL,
		Invalidated:    int32(gone.invalidated), //nolint:gosec // a count of people
	}), nil
}

// The kinds of App, as the contract names them.
const (
	appLink       = directoryrosterv1.AppPurpose_APP_PURPOSE_LINK
	appController = directoryrosterv1.AppPurpose_APP_PURPOSE_CONTROLLER
	appRunners    = directoryrosterv1.AppPurpose_APP_PURPOSE_RUNNERS
)

// orgAppName is the configured name of an organisation's App, or empty.
func (c *Console) orgAppName(org string) string {
	if c.deps.GitHubOrgAppName == nil {
		return ""
	}
	return c.deps.GitHubOrgAppName(org)
}

// githubGroupGrants is the reverse of an App's grants: which Apps each
// internal group may mint tokens of, from the catalogue alone.
//
// From the catalogue and nothing else, on purpose. A group's page is read
// far more often than the Apps list, and what it needs — the App, its
// repositories and the most a token may carry — is declared here; asking
// GitHub about every App to colour a chip would put a network call on
// every read of the policy.
func (c *Console) githubGroupGrants() map[string][]*directoryrosterv1.GitHubGroupGrant {
	out := map[string][]*directoryrosterv1.GitHubGroupGrant{}
	if c.deps.GitHubCatalogue == nil {
		return out
	}
	for i := range c.deps.GitHubCatalogue.Apps {
		app := &c.deps.GitHubCatalogue.Apps[i]
		for _, grant := range app.Grants {
			row := &directoryrosterv1.GitHubGroupGrant{
				AppId: app.ID, AppName: app.DisplayName(), Org: app.Org,
				Repositories: slices.Clone(grant.Repositories), Permissions: maps.Clone(grant.Permissions),
			}
			// When the grant was last used, where this service still
			// remembers. It comes from the ring beside it — no store is
			// read and no call is made to GitHub — so it stays as cheap as
			// the rest of a group's page, and it is absent rather than
			// "never" where nothing is remembered.
			if at, minted := c.deps.GitHubMints.LastMinted(app.ID, grant.Group); minted {
				row.LastMinted = timestampOf(at)
			}
			out[grant.Group] = append(out[grant.Group], row)
		}
	}
	return out
}

// githubBegin is what starting any connect returns: where the browser
// goes, the manifest it POSTs there when the App is still to be created,
// and the signed state both are pinned to.
type githubBegin struct{ url, manifest, state string }

// githubDisconnect is what disconnecting any App returns.
type githubDisconnect struct {
	uninstalled bool
	detail      string
	settingsURL string
	// invalidated is how many links became unverifiable. The link App
	// alone.
	invalidated int
}

// pinFlow pins a flow to this browser for as long as the two clicks have.
func (c *Console) pinFlow(header http.Header, state string) {
	header.Add("Set-Cookie", access.ConnectCookie(state, c.deps.SecureCookie, githubFlowWindow).String())
}

// uninstall is the revoke every Disconnect makes: GitHub's API cannot
// delete an App, so the registration stays for its owner to delete and
// nothing can act through it once the installation is gone.
func (c *Console) uninstall(ctx context.Context, appID, installationID int64, key string) githubDisconnect {
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

// Where GitHub sends the browser back to while a catalogue App is created:
// after Create, and after Install.
const (
	githubCatalogueCallbackPath = consolewire.CatalogueCallbackPath
	githubCatalogueSetupPath    = consolewire.CatalogueSetupPath
)

// githubCatalogueBind prefixes the App's id in a catalogue flow's signed
// state, so no other GitHub flow's state can finish it, and the other way
// round.

// appSettingsURL is where an organisation's owner edits or deletes an App.
func appSettingsURL(org, slug string) string {
	if org == "" || slug == "" {
		return ""
	}
	return githubapp.WebBase + "/organizations/" + url.PathEscape(org) + "/settings/apps/" + url.PathEscape(slug)
}

// Where GitHub sends the browser back to while a runner App is created:
// after Create, and after Install.
const (
	githubRunnerCallbackPath = consolewire.RunnerCallbackPath
	githubRunnerSetupPath    = consolewire.RunnerSetupPath
)

// githubRunnerBind prefixes `<tier>:<org>` in a runner flow's signed state,
// so an organisation connect's state can never finish a runner App's flow,
// and the other way round.
