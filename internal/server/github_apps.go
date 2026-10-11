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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/githubroster/status"
	"github.com/truvity/sluis/storage/logattr"
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
		begun, err = c.beginRunnerAppConnect(ctx, id.Who(), app.GetOrg(), app.GetTier())
	default:
		begun, err = c.beginCatalogueAppConnect(ctx, id.Who(), app.GetId())
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
	githubCatalogueCallbackPath = "/connect/github/catalogue/callback"
	githubCatalogueSetupPath    = "/connect/github/catalogue/setup"
)

// githubCatalogueBind prefixes the App's id in a catalogue flow's signed
// state, so no other GitHub flow's state can finish it, and the other way
// round.

// catalogueStore refuses where the deployment keeps no catalogue Apps.
func (c *Console) catalogueStore() (GitHubCatalogueApps, error) {
	if c.deps.GitHubCatalogueApps == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this deployment keeps no state in Kubernetes, so a catalogue App's key would not survive a restart"))
	}
	return c.deps.GitHubCatalogueApps, nil
}

// requireCatalogueApp is [Console.requireOrg] for a catalogue App, by the
// organisation its entry declares — or, for one created from an entry since
// dropped, the organisation it was created under. An id nothing names is
// the installation-wide operator's to be told so: nobody owns it.
func (c *Console) requireCatalogueApp(ctx context.Context, want access.Role, id string) (access.Identity, error) {
	org := ""
	if c.deps.GitHubCatalogue != nil {
		if entry, declared := c.deps.GitHubCatalogue.Get(id); declared {
			org = entry.Org
		}
	}
	if org == "" && c.deps.GitHubCatalogueApps != nil {
		if record, _, created, err := c.deps.GitHubCatalogueApps.Get(ctx, id); err == nil && created {
			org = record.Org
		}
	}
	return c.requireOrg(ctx, want, org)
}

// BeginGitHubCatalogueAppConnect starts creating a catalogue App, or
// finishing installing one created before.
func (c *Console) BeginGitHubCatalogueAppConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubCatalogueAppConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubCatalogueAppConnectResponse], error) {
	id := strings.TrimSpace(req.Msg.GetId())
	who, err := c.requireCatalogueApp(ctx, access.RoleOperator, id)
	if err != nil {
		return nil, err
	}
	begun, err := c.beginCatalogueAppConnect(ctx, who.Who(), id)
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubCatalogueAppConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

// beginCatalogueAppConnect is the work of it, which
// [Console.BeginGitHubAppConnect] does too.
func (c *Console) beginCatalogueAppConnect(ctx context.Context, actor, id string) (githubBegin, error) {
	store, err := c.catalogueStore()
	if err != nil {
		return githubBegin{}, err
	}
	entry, declared := c.deps.GitHubCatalogue.Get(id)
	if !declared {
		return githubBegin{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the catalogue declares no App %q", id))
	}
	existing, _, created, err := store.Get(ctx, id)
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeUnavailable, err)
	}
	if created && existing.Installed() {
		return githubBegin{}, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%s is already created and installed as %s: disconnect it first, or a second App would sit beside the first", id, existing.AppSlug))
	}

	state, err := c.deps.State.IssueAs(access.Binding{Bind: githubCatalogueBind + id, Actor: actor})
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err)
	}
	out := githubBegin{state: state}
	if created {
		out.url = githubapp.InstallURL(existing.AppSlug, state)
		return out, nil
	}
	root := c.githubRoot()
	manifest, err := json.Marshal(githubapp.ManifestFor(entry, c.deps.PublicURL, root+githubCatalogueCallbackPath, root+githubCatalogueSetupPath, nil))
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err)
	}
	out.url, out.manifest = githubapp.CreateURL(entry.Org, state), string(manifest)
	return out, nil
}

// DisconnectGitHubCatalogueApp uninstalls a catalogue App, then forgets
// it. A failed uninstall still forgets, and says what is left to do by
// hand. The App stays on GitHub: the API cannot delete one.
func (c *Console) DisconnectGitHubCatalogueApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubCatalogueAppRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubCatalogueAppResponse], error) {
	id := strings.TrimSpace(req.Msg.GetId())
	if _, err := c.requireCatalogueApp(ctx, access.RoleOperator, id); err != nil {
		return nil, err
	}
	gone, err := c.disconnectCatalogueApp(ctx, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&directoryrosterv1.DisconnectGitHubCatalogueAppResponse{
		Uninstalled: gone.uninstalled, Detail: gone.detail, AppSettingsUrl: gone.settingsURL,
	}), nil
}

// disconnectCatalogueApp is the work of it, which
// [Console.DisconnectGitHubApp] does too.
func (c *Console) disconnectCatalogueApp(ctx context.Context, id string) (githubDisconnect, error) {
	store, err := c.catalogueStore()
	if err != nil {
		return githubDisconnect{}, err
	}
	if !catalogue.ValidID(id) {
		return githubDisconnect{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not a catalogue id", id))
	}
	record, key, created, err := store.Get(ctx, id)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err)
	}
	if !created {
		return githubDisconnect{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("no App %s has been created", id))
	}

	out := c.uninstall(ctx, record.AppID, record.InstallationID, key)
	out.settingsURL = appSettingsURL(record.Org, record.AppSlug)
	if err = store.Delete(ctx, id); err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err)
	}
	c.record(ctx, audit.CatalogueAppDisconnected(actorOf(ctx), record.Org,
		audit.App{Name: id, ID: record.AppID, Slug: record.AppSlug}, out.uninstalled, out.detail))
	return out, nil
}

// appSettingsURL is where an organisation's owner edits or deletes an App.
func appSettingsURL(org, slug string) string {
	if org == "" || slug == "" {
		return ""
	}
	return githubapp.WebBase + "/organizations/" + url.PathEscape(org) + "/settings/apps/" + url.PathEscape(slug)
}

// githubCatalogueFlow checks a catalogue flow's redirect and names its
// entry.
func (s *ConsoleServer) githubCatalogueFlow(w http.ResponseWriter, r *http.Request) (catalogue.App, string, bool) {
	bind, actor, _, ok := s.githubBound(w, r)
	if !ok {
		return catalogue.App{}, "", false
	}
	id, isCatalogue := strings.CutPrefix(bind, githubCatalogueBind)
	if !isCatalogue || !catalogue.ValidID(id) {
		s.githubProblem(w, r, http.StatusBadRequest, "This is not a catalogue App's connect.", "", nil)
		return catalogue.App{}, "", false
	}
	entry, declared := s.console.deps.GitHubCatalogue.Get(id)
	if s.console.deps.GitHubCatalogueApps == nil || !declared {
		s.githubProblem(w, r, http.StatusConflict, fmt.Sprintf("This deployment's catalogue declares no App %s.", id), "", nil)
		return catalogue.App{}, "", false
	}
	return entry, actor, true
}

// githubCatalogueCallback is where GitHub sends the owner after creating a
// catalogue App, with a one-time code for its key. The key is kept and the
// owner is sent straight on to Install.
func (s *ConsoleServer) githubCatalogueCallback(w http.ResponseWriter, r *http.Request) {
	entry, actor, ok := s.githubCatalogueFlow(w, r)
	if !ok {
		return
	}
	registration, err := githubapp.Convert(r.Context(), s.console.githubHTTP(), r.URL.Query().Get("code"))
	if err != nil {
		s.log.WarnContext(r.Context(), "a catalogue App was created and its key could not be collected",
			slog.String("id", entry.ID), slog.String("org", entry.Org), logattr.SafeError("error", err))
		s.githubProblem(w, r, http.StatusConflict,
			"GitHub created the App, and then would not hand over its key.", err.Error(), []string{
				"The page was reloaded: the code GitHub returns can be exchanged once.",
				"More than an hour passed between creating the App and returning here.",
				"This service cannot reach api.github.com: the cluster's egress policy has to allow it.",
			})
		return
	}
	if !strings.EqualFold(registration.Owner, entry.Org) {
		s.githubProblem(w, r, http.StatusConflict,
			fmt.Sprintf("The App was created under %s, and the catalogue declares it under %s. Delete it on GitHub and create it again.",
				registration.Owner, entry.Org), "", nil)
		return
	}
	record := catalogueapp.Record{
		ID: entry.ID, Org: entry.Org, AppID: registration.ID, AppSlug: registration.Slug, HTMLURL: registration.HTMLURL,
		ConnectedAt: time.Now().UTC(), ConnectedBy: actor,
	}
	// A declared webhook gets a secret this service makes, never the one the
	// conversion returns: it is generated before anything is kept, so a
	// failure to generate leaves the App as it was.
	var hookSecret string
	if entry.Webhook != nil {
		if hookSecret, err = clientcreds.Generate(); err != nil {
			s.githubProblem(w, r, http.StatusConflict,
				"GitHub created the App, and a webhook secret could not be generated. Delete it on GitHub and create it again.", err.Error(), nil)
			return
		}
	}
	if err = s.console.deps.GitHubCatalogueApps.Put(r.Context(), record, registration.PEM); err != nil {
		s.log.ErrorContext(r.Context(), "a catalogue App was created and could not be kept", slog.String("id", entry.ID), slog.String("org", entry.Org),
			logattr.SafeError("error", err))
		s.githubProblem(w, r, http.StatusConflict,
			"GitHub created the App, and it could not be saved here. Delete it on GitHub and create it again.", err.Error(), nil)
		return
	}
	s.log.InfoContext(r.Context(), "catalogue App created", slog.String("id", entry.ID), slog.String("org", entry.Org), slog.Int64("app", registration.ID),
		slog.String("slug", registration.Slug), logattr.SafeString("by", actor))
	app := audit.App{Name: entry.ID, ID: registration.ID, Slug: registration.Slug}
	s.console.record(r.Context(), audit.CatalogueAppCreated(audit.Identified(actor), entry.Org, app))
	if hookSecret != "" && !s.configureWebhook(w, r, entry, actor, record, registration.PEM, hookSecret, app) {
		return
	}

	state, err := s.state.IssueAs(access.Binding{Bind: githubCatalogueBind + entry.ID, Actor: actor})
	if err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App was created; start Create again to install it.", err.Error(), nil)
		return
	}
	access.SetCookie(w, access.ConnectCookie(state, s.sessions.Secure(), githubFlowWindow))
	http.Redirect(w, r, githubapp.InstallURL(registration.Slug, state), http.StatusFound)
}

// configureWebhook gives a new App its webhook: the secret is kept first, so
// a failure after GitHub has it never leaves GitHub holding a secret nobody
// has, then GitHub is told it and the URL in one call. It says why on the
// page and returns false when it could not; the App then exists with no
// working webhook, which a rotation finishes.
func (s *ConsoleServer) configureWebhook(
	w http.ResponseWriter, r *http.Request, entry catalogue.App, actor string,
	record catalogueapp.Record, key, secret string, app audit.App,
) bool {
	ctx, store, who := r.Context(), s.console.deps.GitHubCatalogueApps, audit.Identified(actor)
	fail := func(step, summary string, err error) bool {
		s.log.WarnContext(ctx, "a catalogue App's webhook could not be set", slog.String("id", entry.ID), slog.String("org", entry.Org),
			slog.String("step", step), logattr.SafeError("error", err))
		s.console.record(ctx, audit.CatalogueAppWebhookChanged(who, entry.Org, app, step, audit.Failed(err.Error())))
		s.githubProblem(w, r, http.StatusConflict, summary, err.Error(), []string{
			"The App exists and its key is kept. Rotate its webhook secret from the console once it is installed to finish.",
		})
		return false
	}
	if err := store.PutWebhookSecret(ctx, entry.ID, secret); err != nil {
		return fail(audit.WebhookStaged, "GitHub created the App, and its webhook secret could not be kept here.", err)
	}
	target := entry.Webhook.Target(secret)
	token, err := githubapp.AppToken(record.AppID, key, time.Now())
	if err != nil {
		return fail(audit.WebhookConfigured, "GitHub created the App, and its webhook could not be set.", err)
	}
	if _, err = githubapp.PatchHookConfig(ctx, s.console.githubHTTP(), token, githubapp.HookUpdate{URL: target, Secret: secret}); err != nil {
		return fail(audit.WebhookConfigured, "GitHub created the App, and its webhook could not be set.", err)
	}
	record.WebhookURL, record.HookRotatedAt = target, time.Now().UTC()
	if err = store.Put(ctx, record, key); err != nil {
		return fail(audit.WebhookConfigured, "The App's webhook was set, and could not be recorded here.", err)
	}
	s.console.record(ctx, audit.CatalogueAppWebhookChanged(who, entry.Org, app, audit.WebhookConfigured, audit.Succeeded()))
	return true
}

// githubCatalogueSetup is where GitHub sends the owner after installing a
// catalogue App. What is kept is where GitHub says the App is installed,
// asked as the App.
func (s *ConsoleServer) githubCatalogueSetup(w http.ResponseWriter, r *http.Request) {
	entry, actor, ok := s.githubCatalogueFlow(w, r)
	if !ok {
		return
	}
	access.SetCookie(w, access.ConnectCookie("", s.sessions.Secure(), 0))
	store := s.console.deps.GitHubCatalogueApps
	record, key, created, err := store.Get(r.Context(), entry.ID)
	if err != nil || !created || key == "" {
		s.githubProblem(w, r, http.StatusConflict,
			fmt.Sprintf("There is no App %s to finish installing. Start Create again.", entry.ID), errString(err), nil)
		return
	}
	token, err := githubapp.AppToken(record.AppID, key, time.Now())
	if err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App's stored key is not usable. Disconnect and create it again.", err.Error(), nil)
		return
	}
	installation, err := githubapp.FindInstallation(r.Context(), s.console.githubHTTP(), token, record.Org)
	if err != nil {
		summary := "GitHub could not say where the App is installed."
		if errors.Is(err, githubapp.ErrNotInstalled) {
			summary = fmt.Sprintf("The App is not installed on %s yet. Finish installing from the console.", record.Org)
		}
		s.githubProblem(w, r, http.StatusConflict, summary, err.Error(), nil)
		return
	}
	record.InstallationID = installation
	if err = store.Put(r.Context(), record, key); err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App is installed and could not be recorded here.", err.Error(), nil)
		return
	}
	s.log.InfoContext(r.Context(), "catalogue App installed", slog.String("id", entry.ID), slog.String("org", record.Org),
		slog.Int64("installation", installation),
		logattr.SafeString("by", actor))
	s.console.record(r.Context(), audit.CatalogueAppInstalled(audit.Identified(actor), record.Org,
		audit.App{Name: entry.ID, ID: record.AppID, Slug: record.AppSlug}, installation))
	http.Redirect(w, r, s.at("/#/github/apps/catalogue"), http.StatusFound)
}

// Where GitHub sends the browser back to while a runner App is created:
// after Create, and after Install.
const (
	githubRunnerCallbackPath = "/connect/github/runner/callback"
	githubRunnerSetupPath    = "/connect/github/runner/setup"
)

// githubRunnerBind prefixes `<tier>:<org>` in a runner flow's signed state,
// so an organisation connect's state can never finish a runner App's flow,
// and the other way round.

// runnerTier checks a tier against the ones the deployment declares.
func (c *Console) runnerTier(tier string) error {
	switch {
	case c.deps.GitHubRunnerApps == nil || len(c.deps.GitHubRunnerTiers) == 0:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this deployment declares no runner tiers"))
	case !slices.Contains(c.deps.GitHubRunnerTiers, tier):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("%q is not a runner tier here; the deployment declares %s", tier, strings.Join(c.deps.GitHubRunnerTiers, ", ")))
	}
	return nil
}

// BeginGitHubRunnerAppConnect starts creating a runner App, or finishing
// installing one created before.
//
// Only in an organisation the policy binds, for the same reason as an
// organisation's App: a typo in the login would otherwise meet GitHub's
// 404 after the operator has left this page.
func (c *Console) BeginGitHubRunnerAppConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubRunnerAppConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubRunnerAppConnectResponse], error) {
	org := strings.TrimSpace(req.Msg.GetOrg())
	id, err := c.requireOrg(ctx, access.RoleOperator, org)
	if err != nil {
		return nil, err
	}
	begun, err := c.beginRunnerAppConnect(ctx, id.Who(), org, strings.TrimSpace(req.Msg.GetTier()))
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubRunnerAppConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

// beginRunnerAppConnect is the work of it, which
// [Console.BeginGitHubAppConnect] does too.
func (c *Console) beginRunnerAppConnect(ctx context.Context, actor, org, tier string) (githubBegin, error) {
	if !status.ValidOrg(org) {
		return githubBegin{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not an organisation login", org))
	}
	if err := c.runnerTier(tier); err != nil {
		return githubBegin{}, err
	}
	if _, bound := boundOrganisations(c.deps.Authorizer.Policy())[org]; !bound {
		return githubBegin{}, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("the policy binds no organisation %s: bind its teams first", org))
	}
	existing, created, err := c.runnerApp(ctx, tier, org)
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeUnavailable, err)
	}
	if created && existing.Installed() {
		return githubBegin{}, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%s already has a %s runner App, %s: disconnect it first, or a second App would sit beside the first", org, tier, existing.AppSlug))
	}

	state, err := c.deps.State.IssueAs(access.Binding{Bind: githubRunnerBind + tier + ":" + org, Actor: actor})
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err)
	}
	out := githubBegin{state: state}
	if created {
		out.url = githubapp.InstallURL(existing.AppSlug, state)
		return out, nil
	}
	root := c.githubRoot()
	manifest, err := json.Marshal(githubapp.NewRunnerManifest(org, tier, c.deps.PublicURL, root+githubRunnerCallbackPath, root+githubRunnerSetupPath))
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err)
	}
	out.url, out.manifest = githubapp.CreateURL(org, state), string(manifest)
	return out, nil
}

// DisconnectGitHubRunnerApp uninstalls a runner App, then forgets it. A
// failed uninstall still forgets, and says what is left to do by hand.
func (c *Console) DisconnectGitHubRunnerApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubRunnerAppRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubRunnerAppResponse], error) {
	org := strings.TrimSpace(req.Msg.GetOrg())
	if _, err := c.requireOrg(ctx, access.RoleOperator, org); err != nil {
		return nil, err
	}
	gone, err := c.disconnectRunnerApp(ctx, org, strings.TrimSpace(req.Msg.GetTier()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&directoryrosterv1.DisconnectGitHubRunnerAppResponse{
		Uninstalled: gone.uninstalled, Detail: gone.detail, AppSettingsUrl: gone.settingsURL,
	}), nil
}

// disconnectRunnerApp is the work of it, which
// [Console.DisconnectGitHubApp] does too.
func (c *Console) disconnectRunnerApp(ctx context.Context, org, tier string) (githubDisconnect, error) {
	switch {
	case !status.ValidOrg(org) || !runnerapp.ValidTier(tier):
		return githubDisconnect{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q/%q is not an organisation and a tier", org, tier))
	case c.deps.GitHubRunnerApps == nil:
		return githubDisconnect{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("this deployment keeps no runner Apps"))
	}
	record, created, err := c.runnerApp(ctx, tier, org)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err)
	}
	key, hasKey, err := c.deps.GitHubRunnerApps.PrivateKey(ctx, tier, org)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err)
	}
	if !created && !hasKey {
		return githubDisconnect{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s has no %s runner App", org, tier))
	}

	out := c.uninstall(ctx, record.AppID, record.InstallationID, key)
	out.settingsURL = appSettingsURL(org, record.AppSlug)
	if err = c.deps.GitHubRunnerApps.Delete(ctx, tier, org); err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err)
	}
	c.record(ctx, audit.RunnerAppDisconnected(actorOf(ctx), org,
		audit.App{ID: record.AppID, Slug: record.AppSlug, Tier: tier}, out.uninstalled, out.detail))
	return out, nil
}

// githubRunnerFlow checks a runner flow's redirect and names its tier and
// organisation.
func (s *ConsoleServer) githubRunnerFlow(w http.ResponseWriter, r *http.Request) (tier, org, actor string, ok bool) {
	bind, actor, _, ok := s.githubBound(w, r)
	if !ok {
		return "", "", "", false
	}
	rest, isRunner := strings.CutPrefix(bind, githubRunnerBind)
	tier, org, split := strings.Cut(rest, ":")
	if !isRunner || !split || !runnerapp.ValidTier(tier) || !status.ValidOrg(org) {
		s.githubProblem(w, r, http.StatusBadRequest, "This is not a runner App's connect.", "", nil)
		return "", "", "", false
	}
	if s.console.deps.GitHubRunnerApps == nil || !slices.Contains(s.console.deps.GitHubRunnerTiers, tier) {
		s.githubProblem(w, r, http.StatusConflict, fmt.Sprintf("This deployment keeps no %s runner Apps.", tier), "", nil)
		return "", "", "", false
	}
	return tier, org, actor, true
}

// githubRunnerCallback is where GitHub sends the owner after creating a
// runner App, with a one-time code for its key. The key is kept and the
// owner is sent straight on to Install.
func (s *ConsoleServer) githubRunnerCallback(w http.ResponseWriter, r *http.Request) {
	tier, org, actor, ok := s.githubRunnerFlow(w, r)
	if !ok {
		return
	}
	registration, err := githubapp.Convert(r.Context(), s.console.githubHTTP(), r.URL.Query().Get("code"))
	if err != nil {
		s.log.WarnContext(r.Context(), "a runner App was created and its key could not be collected",
			logattr.SafeString("org", org), logattr.SafeString("tier", tier), logattr.SafeError("error", err))
		s.githubProblem(w, r, http.StatusConflict,
			"GitHub created the App, and then would not hand over its key.", err.Error(), []string{
				"The page was reloaded: the code GitHub returns can be exchanged once.",
				"More than an hour passed between creating the App and returning here.",
				"This service cannot reach api.github.com: the cluster's egress policy has to allow it.",
			})
		return
	}
	if !strings.EqualFold(registration.Owner, org) {
		s.githubProblem(w, r, http.StatusConflict,
			fmt.Sprintf("The App was created under %s, not %s.", registration.Owner, org), "", nil)
		return
	}
	record := runnerapp.Record{
		Tier: tier, Org: org, AppID: registration.ID, AppSlug: registration.Slug, HTMLURL: registration.HTMLURL,
		ConnectedAt: time.Now().UTC(), ConnectedBy: actor,
	}
	if err = s.console.deps.GitHubRunnerApps.Put(r.Context(), record, registration.PEM); err != nil {
		s.log.ErrorContext(r.Context(), "a runner App was created and could not be kept",
			logattr.SafeString("org", org), logattr.SafeString("tier", tier), logattr.SafeError("error", err))
		s.githubProblem(w, r, http.StatusConflict,
			"GitHub created the App, and it could not be saved here. Delete it on GitHub and create it again.", err.Error(), nil)
		return
	}
	s.log.InfoContext(r.Context(), "runner App created", logattr.SafeString("org", org), logattr.SafeString("tier", tier),
		slog.Int64("app", registration.ID), logattr.SafeString("slug", registration.Slug), logattr.SafeString("by", actor))
	s.console.record(r.Context(), audit.RunnerAppCreated(audit.Identified(actor), org,
		audit.App{ID: registration.ID, Slug: registration.Slug, Tier: tier}))

	state, err := s.state.IssueAs(access.Binding{Bind: githubRunnerBind + tier + ":" + org, Actor: actor})
	if err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App was created; start Create again to install it.", err.Error(), nil)
		return
	}
	access.SetCookie(w, access.ConnectCookie(state, s.sessions.Secure(), githubFlowWindow))
	http.Redirect(w, r, githubapp.InstallURL(registration.Slug, state), http.StatusFound)
}

// githubRunnerSetup is where GitHub sends the owner after installing a
// runner App. What is kept is where GitHub says the App is installed, asked
// as the App.
func (s *ConsoleServer) githubRunnerSetup(w http.ResponseWriter, r *http.Request) {
	tier, org, actor, ok := s.githubRunnerFlow(w, r)
	if !ok {
		return
	}
	access.SetCookie(w, access.ConnectCookie("", s.sessions.Secure(), 0))
	store := s.console.deps.GitHubRunnerApps
	record, created, err := s.console.runnerApp(r.Context(), tier, org)
	key, hasKey, keyErr := store.PrivateKey(r.Context(), tier, org)
	if err = errors.Join(err, keyErr); err != nil || !created || !hasKey {
		s.githubProblem(w, r, http.StatusConflict,
			fmt.Sprintf("There is no %s runner App for %s to finish installing. Start Create again.", tier, org), errString(err), nil)
		return
	}
	token, err := githubapp.AppToken(record.AppID, key, time.Now())
	if err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App's stored key is not usable. Disconnect and create it again.", err.Error(), nil)
		return
	}
	installation, err := githubapp.FindInstallation(r.Context(), s.console.githubHTTP(), token, org)
	if err != nil {
		summary := "GitHub could not say where the App is installed."
		if errors.Is(err, githubapp.ErrNotInstalled) {
			summary = fmt.Sprintf("The App is not installed on %s yet. Finish installing from the console.", org)
		}
		s.githubProblem(w, r, http.StatusConflict, summary, err.Error(), nil)
		return
	}
	record.InstallationID = installation
	if err = store.Put(r.Context(), record, key); err != nil {
		s.githubProblem(w, r, http.StatusConflict, "The App is installed and could not be recorded here.", err.Error(), nil)
		return
	}
	s.log.InfoContext(r.Context(), "runner App installed", logattr.SafeString("org", org), logattr.SafeString("tier", tier),
		slog.Int64("installation", installation), logattr.SafeString("by", actor))
	s.console.record(r.Context(), audit.RunnerAppInstalled(audit.Identified(actor), org,
		audit.App{ID: record.AppID, Slug: record.AppSlug, Tier: tier}, installation))
	http.Redirect(w, r, s.at("/#/github/apps"), http.StatusFound)
}
