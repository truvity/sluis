package console

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/rpcmount"
	"github.com/truvity/sluis/internal/slackapp"
	slackcatalogue "github.com/truvity/sluis/internal/slackapp/catalogue"
	"github.com/truvity/sluis/internal/slackapp/catalogueapp"
	"github.com/truvity/sluis/storage/logattr"
)

// The states a catalogue Slack App is shown in.
const (
	slackAppDeclared      = "declared"
	slackAppCreated       = "created"
	slackAppInstalled     = "installed"
	slackAppScopesMissing = "scopes_missing"
)

// slackCatalogueStore refuses where the deployment keeps no catalogue Apps.
func (c *Service) slackCatalogueStore() (CatalogueApps, error) {
	if c.d().SlackCatalogueApps == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"this deployment keeps no state in Kubernetes, so a Slack App's bot token would not survive a restart")
	}
	return c.d().SlackCatalogueApps, nil
}

// connectedSlackTeam is the team id recorded when a workspace was first
// installed, or the refusal to say when it has none. A catalogue App is
// created in, and installed into, a workspace that is connected already: the
// policy names the workspace by its key alone, and the connection is where
// its team and its owner are recorded.
func (c *Service) connectedSlackTeam(ctx context.Context, workspace string) (string, Book, error) {
	if c.d().Authorizer == nil {
		return "", nil, connect.NewError(connect.CodeFailedPrecondition, "no policy is loaded")
	}
	if !c.d().Authorizer.Policy().SlackWorkspaceDeclared(workspace) {
		return "", nil, connect.Errorf(connect.CodeFailedPrecondition,
			"the policy's slack.workspaces does not name workspace %q", workspace)
	}
	book, err := c.book(ctx)
	if err != nil {
		return "", nil, err
	}
	if book.Team(workspace) == "" {
		return "", book, connect.Errorf(connect.CodeFailedPrecondition,
			"connect the workspace first: %s has no connected Slack workspace yet, and a catalogue App is installed into the team recorded when it is", workspace)
	}
	return book.Team(workspace), book, nil
}

// ListSlackApps is every declared App, and every App created from an entry
// no longer declared, with where each stands.
func (c *Service) ListSlackApps(
	ctx context.Context, caller consolecall.Caller, _ *directoryrosterv1.ListSlackAppsRequest,
) (*directoryrosterv1.ListSlackAppsResponse, error) {
	id, book, err := c.requireAnySlack(ctx, caller, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	out := &directoryrosterv1.ListSlackAppsResponse{Available: c.d().SlackCatalogueApps != nil}
	kept := map[string]catalogueapp.Record{}
	if c.d().SlackCatalogueApps != nil {
		records, err := c.d().SlackCatalogueApps.List(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
		}
		for i := range records {
			kept[records[i].ID] = records[i]
		}
	}
	if c.d().SlackCatalogue != nil {
		for i := range c.d().SlackCatalogue.Apps {
			entry := &c.d().SlackCatalogue.Apps[i]
			record, has := kept[entry.ID]
			delete(kept, entry.ID)
			out.Apps = appendVisible(out.Apps, id, book, c.slackAppView(entry, recordPtr(record, has), book))
		}
	}
	for _, orphan := range slices.Sorted(maps.Keys(kept)) {
		record := kept[orphan]
		out.Apps = appendVisible(out.Apps, id, book, c.slackAppView(nil, &record, book))
	}
	return out, nil
}

// appendVisible adds an App to the list if the caller may view its
// workspace, saying whether the caller may also operate it.
func appendVisible(
	apps []*directoryrosterv1.SlackApp, id access.Identity, book Book, app *directoryrosterv1.SlackApp,
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

// slackAppView is one App as the console shows it: the declaration, the
// record, and what follows from comparing them. Never a credential.
func (c *Service) slackAppView(entry *slackcatalogue.App, record *catalogueapp.Record, book Book) *directoryrosterv1.SlackApp {
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
	out.AppSettingsUrl = AppSettingsURL(record.AppID)
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
func (c *Service) declaredSlackApp(id string) (slackcatalogue.App, error) {
	entry, declared := c.d().SlackCatalogue.Get(id)
	if !declared {
		return slackcatalogue.App{}, connect.Errorf(connect.CodeInvalidArgument, "the catalogue declares no Slack App %q", id)
	}
	return entry, nil
}

// slackRedirect is where Slack sends the owner after installing a catalogue App.
func (c *Service) slackRedirect(caller consolecall.Caller) string {
	return caller.Origin + CatalogueCallbackPath
}

// CreateSlackApp creates a catalogue App in Slack.
//
// The configuration token is read from the request into one local, used
// for one call, and dropped: it is not written to the Secret or a record,
// not put into the signed state, not in any log line or audit record, and
// not in any error returned (errors from Slack never carry it).
func (c *Service) CreateSlackApp(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.CreateSlackAppRequest,
) (*directoryrosterv1.CreateSlackAppResponse, error) {
	// An operator of the installation or of some directory, for the answer
	// to "who are you"; the owner of THIS workspace is asked once the
	// entry, and so the workspace, is known.
	if _, err := consoleauthz.RequireAnywhere(caller, access.RoleOperator); err != nil {
		return nil, err
	}
	store, err := c.slackCatalogueStore()
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.GetId())
	entry, err := c.declaredSlackApp(id)
	if err != nil {
		return nil, err
	}
	who, err := c.requireSlack(ctx, caller, access.RoleOperator, entry.Workspace)
	if err != nil {
		return nil, err
	}
	_, book, err := c.connectedSlackTeam(ctx, entry.Workspace)
	if err != nil {
		return nil, err
	}
	configToken := strings.TrimSpace(req.GetConfigurationToken())
	if !plausibleConfigurationToken(configToken) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			"that is not an app configuration token: generate one at api.slack.com/apps under \"Your App Configuration Tokens\" (one word, starting xoxe.)")
	}
	if _, _, created, err := store.Get(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	} else if created {
		return nil, connect.Errorf(connect.CodeFailedPrecondition,
			"%s is already created: install it, rather than create a second App beside the first", id)
	}
	manifest, err := slackcatalogue.Manifest(entry, c.slackRedirect(caller))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}

	callCtx, cancel := context.WithTimeout(ctx, slackTimeout)
	defer cancel()
	app, err := c.slackSetup().CreateApp(callCtx, configToken, manifest)
	if err != nil {
		return nil, rpcmount.NewError(connect.CodeFailedPrecondition, fmt.Errorf("slack refused to create the App: %w", err))
	}
	record := catalogueapp.Record{
		ID: id, Workspace: entry.Workspace, AppID: app.AppID, ClientID: app.Credentials.ClientID,
		AuthorizeURL: app.OAuthAuthorizeURL, ManifestScopes: slices.Clone(entry.BotScopes),
		CreatedAt: time.Now().UTC(), CreatedBy: who.Who(),
	}
	if err = store.Put(ctx, record, catalogueapp.Credentials{ClientSecret: app.Credentials.ClientSecret}); err != nil {
		return nil, rpcmount.NewError(connect.CodeUnavailable, fmt.Errorf(
			"slack created the App and it could not be saved here: delete it at %s and create it again: %w", AppSettingsURL(app.AppID), err))
	}
	c.record(ctx, audit.SlackCatalogueAppCreated(consoleauthz.ActorOf(caller), audit.SlackCatalogueApp{ID: id, App: app.AppID, Workspace: entry.Workspace}))
	return &directoryrosterv1.CreateSlackAppResponse{App: c.slackAppView(&entry, &record, book)}, nil
}

// InstallSlackApp starts installing, or reinstalling, a created App: the
// response is Slack's authorize URL carrying signed state, and the header
// pins the flow to this browser.
func (c *Service) InstallSlackApp(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.InstallSlackAppRequest,
) (*directoryrosterv1.InstallSlackAppResponse, error) {
	if _, err := consoleauthz.RequireAnywhere(caller, access.RoleOperator); err != nil {
		return nil, err
	}
	store, err := c.slackCatalogueStore()
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.GetId())
	entry, err := c.declaredSlackApp(id)
	if err != nil {
		return nil, err
	}
	if _, err = c.requireSlack(ctx, caller, access.RoleOperator, entry.Workspace); err != nil {
		return nil, err
	}
	team, _, err := c.connectedSlackTeam(ctx, entry.Workspace)
	if err != nil {
		return nil, err
	}
	record, _, created, err := store.Get(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	if !created {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "%s is not created yet: create it first", id)
	}

	// Scopes declared after the App was created reach it only through its
	// manifest, and Slack changes a manifest only for a configuration
	// token. Without one a reinstall would grant what it granted before.
	configToken := strings.TrimSpace(req.GetConfigurationToken())
	stale := len(slackapp.MissingScopes(strings.Join(record.ManifestScopes, ","), entry.BotScopes)) > 0
	switch {
	case configToken != "":
		if !plausibleConfigurationToken(configToken) {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				"that is not an app configuration token: generate one at api.slack.com/apps under \"Your App Configuration Tokens\" (one word, starting xoxe.)")
		}
		manifest, merr := slackcatalogue.Manifest(entry, c.slackRedirect(caller))
		if merr != nil {
			return nil, connect.NewError(connect.CodeInternal, merr.Error()).WithCause(merr)
		}
		callCtx, cancel := context.WithTimeout(ctx, slackTimeout)
		defer cancel()
		if _, err = c.slackSetup().UpdateApp(callCtx, configToken, record.AppID, manifest); err != nil {
			return nil, rpcmount.NewError(connect.CodeFailedPrecondition, fmt.Errorf("slack refused to update the App: %w", err))
		}
		record.ManifestScopes = slices.Clone(entry.BotScopes)
		_, creds, _, err := store.Get(ctx, id)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
		}
		if err = store.Put(ctx, record, creds); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
		}
	case stale:
		return nil, rpcmount.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s declares scopes the App was created without: paste an app configuration token so the App can be updated first, "+
				"or a reinstall would grant nothing new", id))
	}

	target, err := slackAuthorizeURL(record.AuthorizeURL, c.slackRedirect(caller), team, entry.BotScopes)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &directoryrosterv1.InstallSlackAppResponse{Url: target}, nil
}

// CompleteCatalogue finishes a catalogue App's install: the console has verified
// where Slack sent the owner after installing it, and forwards the one-time code
// for the bot token.
//
// The token is kept only if it belongs to the workspace the policy names for the
// App. Any other workspace is refused: the token is dropped without being
// stored, and the refusal is recorded.
func (c *Service) CompleteCatalogue(ctx context.Context, caller consolecall.Caller, req CompleteRequest) (Completion, error) {
	app := audit.SlackCatalogueApp{ID: "unknown", App: "unknown", Workspace: "unknown"}
	actor := caller.Actor
	refuse := func(code int, summary, detail, reason string, causes []string) (Completion, error) {
		c.record(ctx, audit.SlackCatalogueAppInstallRefused(audit.Identified(actor), app, reason))
		return refused(code, summary, detail, reason, causes), nil
	}
	if id, ok := strings.CutPrefix(req.Bind, CatalogueBind); ok && slackcatalogue.ValidID(id) {
		app.ID = id
	}
	if r := c.recheck(ctx, caller, req); r != nil {
		return refuse(r.Status, r.Summary, r.Detail, r.Reason, r.Causes)
	}
	if actor == "" {
		return refuse(http.StatusForbidden, "Installing a Slack App needs the operator role.", "", "the state names no operator", nil)
	}
	id, isCatalogue := strings.CutPrefix(req.Bind, CatalogueBind)
	if !isCatalogue || !slackcatalogue.ValidID(id) {
		return refuse(http.StatusBadRequest, "This is not a catalogue Slack App's install.", "", "the state is for another flow", nil)
	}
	entry, declared := c.d().SlackCatalogue.Get(id)
	if c.d().SlackCatalogueApps == nil || !declared {
		return refuse(http.StatusConflict, fmt.Sprintf("This deployment's catalogue declares no Slack App %s.", id), "", "the catalogue declares no such App", nil)
	}
	app.Workspace = entry.Workspace
	if req.Denied != "" {
		return refuse(http.StatusBadRequest, "The installation was not approved in Slack.", req.Denied, "the install was not approved in Slack: "+req.Denied, nil)
	}
	team, _, err := c.connectedSlackTeam(ctx, entry.Workspace)
	if err != nil {
		return refuse(http.StatusConflict, "This App's workspace is not connected.", said(err), "the App's workspace is not connected: "+said(err), nil)
	}
	store := c.d().SlackCatalogueApps
	record, creds, created, err := store.Get(ctx, id)
	if err != nil || !created {
		return refuse(http.StatusConflict, fmt.Sprintf("There is no Slack App %s to finish installing. Create it first.", id), errString(err),
			"the App was not created", nil)
	}
	app.App = record.AppID

	callCtx, cancel := context.WithTimeout(ctx, slackTimeout)
	defer cancel()
	installed, err := c.slackSetup().OAuthAccess(callCtx, record.ClientID, creds.ClientSecret, req.Code, c.slackRedirect(caller))
	if err != nil {
		return refuse(http.StatusConflict, "Slack accepted the install, and then would not hand over the bot token.", err.Error(),
			"slack's oauth.v2.access refused: "+logattr.Error(err), exchangeCauses)
	}
	app.Team = installed.TeamID
	if installed.TeamID != team {
		// The token was minted for somebody else's workspace: take it back
		// before refusing, so a token this service will not keep is not
		// left working in Slack.
		revokeErr := c.slackRevoke(ctx, installed.BotToken)
		return refuse(http.StatusConflict, fmt.Sprintf(
			"The App was installed into workspace %s (%s), and %q was first installed as %s. %s "+
				"Install again from the right workspace.",
			installed.TeamID, installed.TeamName, entry.Workspace, team, refusedToken(revokeErr)), "",
			fmt.Sprintf("installed into team %s, and the workspace was first installed as %s; %s", installed.TeamID, team, revokedWords(revokeErr)), nil)
	}

	now := time.Now().UTC()
	record.TeamID, record.TeamName, record.BotUserID = installed.TeamID, installed.TeamName, installed.BotUserID
	record.Scopes, record.InstalledAt, record.InstalledBy = scopeList(installed.Scope), now, actor
	creds.BotToken = installed.BotToken
	if err = store.Put(ctx, record, creds); err != nil {
		// A token nobody keeps is one nobody can revoke later.
		revokeErr := c.slackRevoke(ctx, installed.BotToken)
		return refuse(http.StatusConflict, "The App is installed and its token could not be saved here. Install it again.", err.Error(),
			"the token could not be kept: "+logattr.Error(err)+"; "+revokedWords(revokeErr), nil)
	}
	app.Scopes = record.Scopes
	c.log().InfoContext(ctx, "catalogue Slack App installed", logattr.SafeString("id", id), logattr.SafeString("workspace", entry.Workspace),
		logattr.SafeString("team", installed.TeamID), logattr.SafeString("scopes", strings.Join(record.Scopes, ",")),
		logattr.SafeString("by", actor))
	c.record(ctx, audit.SlackCatalogueAppInstalled(audit.Identified(actor), app))
	return Completion{}, nil
}
