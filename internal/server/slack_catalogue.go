package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/slackapp"
	slackcatalogue "github.com/truvity/sluis/internal/slackapp/catalogue"
	"github.com/truvity/sluis/internal/slackapp/catalogueapp"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
	"github.com/truvity/sluis/storage/logattr"
)

// The states a catalogue Slack App is shown in.
const (
	slackAppDeclared      = "declared"
	slackAppCreated       = "created"
	slackAppInstalled     = "installed"
	slackAppScopesMissing = "scopes_missing"
)

// slackTimeout bounds one call to Slack.
const slackTimeout = 15 * time.Second

// slackSetup is the calls made before a workspace has a bot token.
func (c *Console) slackSetup() *slackapp.Setup {
	return slackapp.NewSetup(c.deps.SlackAPI...)
}

// slackCatalogueStore refuses where the deployment keeps no catalogue Apps.
func (c *Console) slackCatalogueStore() (SlackCatalogueApps, error) {
	if c.deps.SlackCatalogueApps == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this deployment keeps no state in Kubernetes, so a Slack App's bot token would not survive a restart"))
	}
	return c.deps.SlackCatalogueApps, nil
}

// connectedSlackTeam is the team id recorded when a workspace was first
// installed, or the refusal to say when it has none. A catalogue App is
// created in, and installed into, a workspace that is connected already: the
// policy names the workspace by its key alone, and the connection is where
// its team and its owner are recorded.
func (c *Console) connectedSlackTeam(ctx context.Context, workspace string) (string, slackBook, error) {
	if c.deps.Authorizer == nil {
		return "", nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no policy is loaded"))
	}
	if !c.deps.Authorizer.Policy().SlackWorkspaceDeclared(workspace) {
		return "", nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("the policy's slack.workspaces does not name workspace %q", workspace))
	}
	book, err := c.slackBook(ctx)
	if err != nil {
		return "", nil, err
	}
	if book.Team(workspace) == "" {
		return "", book, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"connect the workspace first: %s has no connected Slack workspace yet, and a catalogue App is installed into the team recorded when it is", workspace))
	}
	return book.Team(workspace), book, nil
}

// ListSlackApps is every declared App, and every App created from an entry
// no longer declared, with where each stands.
func (c *Console) ListSlackApps(
	ctx context.Context, _ *connect.Request[directoryrosterv1.ListSlackAppsRequest],
) (*connect.Response[directoryrosterv1.ListSlackAppsResponse], error) {
	id, book, err := c.requireAnySlack(ctx, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	out := &directoryrosterv1.ListSlackAppsResponse{Available: c.deps.SlackCatalogueApps != nil}
	kept := map[string]catalogueapp.Record{}
	if c.deps.SlackCatalogueApps != nil {
		records, err := c.deps.SlackCatalogueApps.List(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		for i := range records {
			kept[records[i].ID] = records[i]
		}
	}
	if c.deps.SlackCatalogue != nil {
		for i := range c.deps.SlackCatalogue.Apps {
			entry := &c.deps.SlackCatalogue.Apps[i]
			record, has := kept[entry.ID]
			delete(kept, entry.ID)
			out.Apps = appendVisible(out.Apps, id, book, c.slackAppView(entry, recordPtr(record, has), book))
		}
	}
	for _, orphan := range slices.Sorted(maps.Keys(kept)) {
		record := kept[orphan]
		out.Apps = appendVisible(out.Apps, id, book, c.slackAppView(nil, &record, book))
	}
	return connect.NewResponse(out), nil
}

// appendVisible adds an App to the list if the caller may view its
// workspace, saying whether the caller may also operate it.
func appendVisible(
	apps []*directoryrosterv1.SlackApp, id access.Identity, book slackBook, app *directoryrosterv1.SlackApp,
) []*directoryrosterv1.SlackApp {
	if !book.May(id, access.RoleViewer, app.GetWorkspace()) {
		return apps
	}
	// An App is operated once its workspace is connected, and not before:
	// there is nobody yet whose directory it could be.
	_, connected := book[app.GetWorkspace()]
	app.CanOperate = connected && book.MayAct(id, app.GetWorkspace())
	return append(apps, app)
}

func recordPtr(r catalogueapp.Record, has bool) *catalogueapp.Record {
	if !has {
		return nil
	}
	return &r
}

// scopeList splits Slack's comma-separated grant.
func scopeList(granted string) []string {
	return strings.FieldsFunc(granted, func(r rune) bool { return r == ',' || r == ' ' })
}

// slackAppView is one App as the console shows it: the declaration, the
// record, and what follows from comparing them. Never a credential.
func (c *Console) slackAppView(entry *slackcatalogue.App, record *catalogueapp.Record, book slackBook) *directoryrosterv1.SlackApp {
	out := &directoryrosterv1.SlackApp{State: slackAppDeclared, Declared: entry != nil}
	var declaredScopes []string
	if entry != nil {
		out.Id, out.Workspace, out.Name, out.Description = entry.ID, entry.Workspace, entry.DisplayName(), entry.Description
		out.BotScopes = slices.Clone(entry.BotScopes)
		declaredScopes = entry.BotScopes
		out.TeamId = book.Team(entry.Workspace)
	}
	if record == nil {
		return out
	}
	if entry == nil {
		out.Id, out.Workspace, out.Name = record.ID, record.Workspace, record.ID
		out.TeamId = book.Team(record.Workspace)
	}
	out.State = slackAppCreated
	out.AppId = record.AppID
	out.AppSettingsUrl = slackconsole.AppSettingsURL(record.AppID)
	out.CreatedAt, out.CreatedBy = timestamppb.New(record.CreatedAt), record.CreatedBy
	out.NeedsConfigurationToken = len(slackapp.MissingScopes(strings.Join(record.ManifestScopes, ","), declaredScopes)) > 0
	if !record.Installed() {
		return out
	}
	out.State = slackAppInstalled
	out.InstalledTeamId, out.InstalledTeamName, out.BotUserId = record.TeamID, record.TeamName, record.BotUserID
	out.GrantedScopes = slices.Clone(record.Scopes)
	out.InstalledAt, out.InstalledBy = timestamppb.New(record.InstalledAt), record.InstalledBy
	if missing := slackapp.MissingScopes(strings.Join(record.Scopes, ","), declaredScopes); len(missing) > 0 {
		out.State, out.MissingScopes = slackAppScopesMissing, missing
	}
	return out
}

// declaredSlackApp is the entry the catalogue has under an id.
func (c *Console) declaredSlackApp(id string) (slackcatalogue.App, error) {
	entry, declared := c.deps.SlackCatalogue.Get(id)
	if !declared {
		return slackcatalogue.App{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the catalogue declares no Slack App %q", id))
	}
	return entry, nil
}

// slackRedirect is where Slack sends the owner after installing a
// catalogue App.
func (c *Console) slackRedirect() string { return c.githubRoot() + slackconsole.CatalogueCallbackPath }

// slackRevoke takes back a bot token this service will not keep: one minted
// for the wrong workspace, or one it is disconnecting. A token already dead
// is success (see [slackapp.Client.Revoke]).
func (c *Console) slackRevoke(ctx context.Context, token string) error {
	callCtx, cancel := context.WithTimeout(ctx, slackTimeout)
	defer cancel()
	return slackapp.New(token, c.deps.SlackAPI...).Revoke(callCtx)
}

// revokedWords is what became of a refused token, for the audit record.
func revokedWords(revokeErr error) string {
	if revokeErr != nil {
		return "the token was dropped but Slack would not revoke it"
	}
	return "the token was revoked and dropped"
}

// refusedToken is the sentence that ends a refusal of a token minted for
// the wrong workspace: what became of the token.
func refusedToken(revokeErr error) string {
	if revokeErr != nil {
		return "Nothing was kept, but Slack would not revoke the token: remove the App from that workspace in its Slack settings."
	}
	return "Nothing was kept, and the token was revoked."
}

// A configuration token is one word ("xoxe.xoxp-1-..."): anything with
// whitespace in it is a pasted sentence, refused before it travels.
func plausibleConfigurationToken(token string) bool {
	return token != "" && !strings.ContainsAny(token, " \t\r\n")
}

// CreateSlackApp creates a catalogue App in Slack.
//
// The configuration token is read from the request into one local, used
// for one call, and dropped: it is not written to the Secret or a record,
// not put into the signed state, not in any log line or audit record, and
// not in any error returned (errors from Slack never carry it).
func (c *Console) CreateSlackApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.CreateSlackAppRequest],
) (*connect.Response[directoryrosterv1.CreateSlackAppResponse], error) {
	// An operator of the installation or of some directory, for the answer
	// to "who are you"; the owner of THIS workspace is asked once the
	// entry, and so the workspace, is known.
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	store, err := c.slackCatalogueStore()
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.Msg.GetId())
	entry, err := c.declaredSlackApp(id)
	if err != nil {
		return nil, err
	}
	who, err := c.requireSlack(ctx, access.RoleOperator, entry.Workspace)
	if err != nil {
		return nil, err
	}
	_, book, err := c.connectedSlackTeam(ctx, entry.Workspace)
	if err != nil {
		return nil, err
	}
	configToken := strings.TrimSpace(req.Msg.GetConfigurationToken())
	if !plausibleConfigurationToken(configToken) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("that is not an app configuration token: generate one at api.slack.com/apps under \"Your App Configuration Tokens\" (one word, starting xoxe.)"))
	}
	if _, _, created, err := store.Get(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	} else if created {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%s is already created: install it, rather than create a second App beside the first", id))
	}
	manifest, err := slackcatalogue.Manifest(entry, c.slackRedirect())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, slackTimeout)
	defer cancel()
	app, err := c.slackSetup().CreateApp(callCtx, configToken, manifest)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("slack refused to create the App: %w", err))
	}
	record := catalogueapp.Record{
		ID: id, Workspace: entry.Workspace, AppID: app.AppID, ClientID: app.Credentials.ClientID,
		AuthorizeURL: app.OAuthAuthorizeURL, ManifestScopes: slices.Clone(entry.BotScopes),
		CreatedAt: time.Now().UTC(), CreatedBy: who.Who(),
	}
	if err = store.Put(ctx, record, catalogueapp.Credentials{ClientSecret: app.Credentials.ClientSecret}); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf(
			"slack created the App and it could not be saved here: delete it at %s and create it again: %w", slackconsole.AppSettingsURL(app.AppID), err))
	}
	c.record(ctx, audit.SlackCatalogueAppCreated(actorOf(ctx), audit.SlackCatalogueApp{ID: id, App: app.AppID, Workspace: entry.Workspace}))
	return connect.NewResponse(&directoryrosterv1.CreateSlackAppResponse{App: c.slackAppView(&entry, &record, book)}), nil
}

// InstallSlackApp starts installing, or reinstalling, a created App: the
// response is Slack's authorize URL carrying signed state, and the header
// pins the flow to this browser.
func (c *Console) InstallSlackApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.InstallSlackAppRequest],
) (*connect.Response[directoryrosterv1.InstallSlackAppResponse], error) {
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	store, err := c.slackCatalogueStore()
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.Msg.GetId())
	entry, err := c.declaredSlackApp(id)
	if err != nil {
		return nil, err
	}
	who, err := c.requireSlack(ctx, access.RoleOperator, entry.Workspace)
	if err != nil {
		return nil, err
	}
	team, _, err := c.connectedSlackTeam(ctx, entry.Workspace)
	if err != nil {
		return nil, err
	}
	record, _, created, err := store.Get(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if !created {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s is not created yet: create it first", id))
	}

	// Scopes declared after the App was created reach it only through its
	// manifest, and Slack changes a manifest only for a configuration
	// token. Without one a reinstall would grant what it granted before.
	configToken := strings.TrimSpace(req.Msg.GetConfigurationToken())
	stale := len(slackapp.MissingScopes(strings.Join(record.ManifestScopes, ","), entry.BotScopes)) > 0
	switch {
	case configToken != "":
		if !plausibleConfigurationToken(configToken) {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("that is not an app configuration token: generate one at api.slack.com/apps under \"Your App Configuration Tokens\" (one word, starting xoxe.)"))
		}
		manifest, merr := slackcatalogue.Manifest(entry, c.slackRedirect())
		if merr != nil {
			return nil, connect.NewError(connect.CodeInternal, merr)
		}
		callCtx, cancel := context.WithTimeout(ctx, slackTimeout)
		defer cancel()
		if _, err = c.slackSetup().UpdateApp(callCtx, configToken, record.AppID, manifest); err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("slack refused to update the App: %w", err))
		}
		record.ManifestScopes = slices.Clone(entry.BotScopes)
		_, creds, _, err := store.Get(ctx, id)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		if err = store.Put(ctx, record, creds); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
	case stale:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s declares scopes the App was created without: paste an app configuration token so the App can be updated first, "+
				"or a reinstall would grant nothing new", id))
	}

	state, err := c.deps.State.IssueAs(access.Binding{Bind: slackconsole.CatalogueBind + id, Actor: who.Who()})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	target, err := slackAuthorizeURL(record.AuthorizeURL, state, c.slackRedirect(), team, entry.BotScopes)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	response := connect.NewResponse(&directoryrosterv1.InstallSlackAppResponse{Url: target})
	c.pinFlow(response.Header(), state)
	return response, nil
}

// slackAuthorizeURL is the authorize URL Slack gave at creation, with what
// only this flow knows: the signed state, where to come back to, the scopes
// and the workspace to preselect.
func slackAuthorizeURL(authorize, state, redirect, team string, scopes []string) (string, error) {
	u, err := url.Parse(authorize)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("the App's authorize URL %q is not usable", authorize)
	}
	q := u.Query()
	q.Set("state", state)
	q.Set("redirect_uri", redirect)
	q.Set("scope", strings.Join(scopes, ","))
	if team != "" {
		q.Set("team", team)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// slackCatalogueCallback is where Slack sends the owner after installing a
// catalogue App, with a one-time code for the bot token.
//
// The token is kept only if it belongs to the workspace the policy names
// for the App. Any other workspace is refused: the token is dropped
// without being stored, and the refusal is recorded.
func (s *ConsoleServer) slackCatalogueCallback(w http.ResponseWriter, r *http.Request) {
	flow := slackCatalogueFlow
	console := s.console
	app := audit.SlackCatalogueApp{ID: "unknown", App: "unknown", Workspace: "unknown"}
	actor := ""
	refuse := func(code int, summary, detail, reason string, causes []string) {
		s.slackRefused(flow, r, actor, reason)
		console.record(r.Context(), audit.SlackCatalogueAppInstallRefused(audit.Identified(actor), app, reason))
		s.slackProblem(flow, w, r, code, summary, detail, causes)
	}
	learn := func(bind, who string) {
		actor = who
		if id, ok := strings.CutPrefix(bind, slackconsole.CatalogueBind); ok && slackcatalogue.ValidID(id) {
			app.ID = id
		}
	}
	bind, actor, current, ok := s.slackBound(r, refuse, learn)
	if !ok {
		return
	}
	if current != nil {
		// The role question again, now, of the workspace the App is in.
		if _, err := console.requireSlack(r.Context(), access.RoleOperator, console.slackWorkspaceOfBind(bind)); err != nil {
			said := connectMessage(err)
			refuse(http.StatusForbidden, said+".", "", "the signed-in operator may not operate the workspace: "+said, nil)
			return
		}
	}
	if actor == "" {
		refuse(http.StatusForbidden, "Installing a Slack App needs the operator role.", "", "the state names no operator", nil)
		return
	}
	// The flow ends here, whichever way it goes.
	access.SetCookie(w, access.ConnectCookie("", s.sessions.Secure(), 0))
	id, isCatalogue := strings.CutPrefix(bind, slackconsole.CatalogueBind)
	if !isCatalogue || !slackcatalogue.ValidID(id) {
		refuse(http.StatusBadRequest, "This is not a catalogue Slack App's install.", "", "the state is for another flow", nil)
		return
	}
	entry, declared := console.deps.SlackCatalogue.Get(id)
	if console.deps.SlackCatalogueApps == nil || !declared {
		refuse(http.StatusConflict, fmt.Sprintf("This deployment's catalogue declares no Slack App %s.", id), "", "the catalogue declares no such App", nil)
		return
	}
	app.Workspace = entry.Workspace
	if denied := r.URL.Query().Get("error"); denied != "" {
		refuse(http.StatusBadRequest, "The installation was not approved in Slack.", denied, "the install was not approved in Slack: "+denied, nil)
		return
	}
	team, _, err := console.connectedSlackTeam(r.Context(), entry.Workspace)
	if err != nil {
		refuse(http.StatusConflict, "This App's workspace is not connected.", err.Error(), "the App's workspace is not connected: "+err.Error(), nil)
		return
	}
	store := console.deps.SlackCatalogueApps
	record, creds, created, err := store.Get(r.Context(), id)
	if err != nil || !created {
		refuse(http.StatusConflict, fmt.Sprintf("There is no Slack App %s to finish installing. Create it first.", id), errString(err),
			"the App was not created", nil)
		return
	}
	app.App = record.AppID

	callCtx, cancel := context.WithTimeout(r.Context(), slackTimeout)
	defer cancel()
	installed, err := console.slackSetup().OAuthAccess(callCtx, record.ClientID, creds.ClientSecret, r.URL.Query().Get("code"), console.slackRedirect())
	if err != nil {
		refuse(http.StatusConflict, "Slack accepted the install, and then would not hand over the bot token.", err.Error(),
			"slack's oauth.v2.access refused: "+logattr.Error(err), []string{
				"The page was reloaded: the code Slack returns can be exchanged once.",
				"More than ten minutes passed between approving and returning here.",
				"This service cannot reach slack.com: the cluster's egress policy has to allow it.",
			})
		return
	}
	app.Team = installed.TeamID
	if installed.TeamID != team {
		// The token was minted for somebody else's workspace: take it back
		// before refusing, so a token this service will not keep is not
		// left working in Slack.
		revokeErr := console.slackRevoke(r.Context(), installed.BotToken)
		refuse(http.StatusConflict, fmt.Sprintf(
			"The App was installed into workspace %s (%s), and %q was first installed as %s. %s "+
				"Install again from the right workspace.",
			installed.TeamID, installed.TeamName, entry.Workspace, team, refusedToken(revokeErr)), "",
			fmt.Sprintf("installed into team %s, and the workspace was first installed as %s; %s", installed.TeamID, team, revokedWords(revokeErr)), nil)
		return
	}

	now := time.Now().UTC()
	record.TeamID, record.TeamName, record.BotUserID = installed.TeamID, installed.TeamName, installed.BotUserID
	record.Scopes, record.InstalledAt, record.InstalledBy = scopeList(installed.Scope), now, actor
	creds.BotToken = installed.BotToken
	if err = store.Put(r.Context(), record, creds); err != nil {
		// A token nobody keeps is one nobody can revoke later.
		revokeErr := console.slackRevoke(r.Context(), installed.BotToken)
		refuse(http.StatusConflict, "The App is installed and its token could not be saved here. Install it again.", err.Error(),
			"the token could not be kept: "+logattr.Error(err)+"; "+revokedWords(revokeErr), nil)
		return
	}
	app.Scopes = record.Scopes
	s.log.InfoContext(r.Context(), "catalogue Slack App installed", logattr.SafeString("id", id), logattr.SafeString("workspace", entry.Workspace),
		logattr.SafeString("team", installed.TeamID), logattr.SafeString("scopes", strings.Join(record.Scopes, ",")),
		logattr.SafeString("by", actor))
	console.record(r.Context(), audit.SlackCatalogueAppInstalled(audit.Identified(actor), app))
	http.Redirect(w, r, s.at("/#/slack-apps"), http.StatusFound)
}
