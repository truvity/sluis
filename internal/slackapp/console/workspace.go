package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/slackapp"
	slackcatalogue "github.com/truvity/sluis/internal/slackapp/catalogue"
	"github.com/truvity/sluis/internal/slackroster/connection"
	"github.com/truvity/sluis/internal/slackroster/status"
	"github.com/truvity/sluis/storage/logattr"
)

// Workspaces is where connected Slack workspaces are kept: each one's
// record, which the console shows, and credential, which only the Slack
// controller acts with. It also keeps the operators' confirmations of
// removal sets.
type Workspaces interface {
	// Put writes a record and its credential.
	Put(ctx context.Context, record connection.Record, credential connection.Credential) error
	List(ctx context.Context) ([]connection.Record, error)
	// Get is one workspace's record and credential; found is false when
	// there is no credential.
	Get(ctx context.Context, workspace string) (connection.Record, connection.Credential, bool, error)
	// SetOwner changes only a record's owner, under the object's version;
	// found is false when the workspace has no record.
	SetOwner(ctx context.Context, workspace, owner string) (previous string, found bool, err error)
	// RequestPass keeps an operator's request for a pass now; kept is false,
	// with the time of the last request, when that was under a minute ago.
	RequestPass(ctx context.Context, request connection.PassRequest) (kept bool, last time.Time, err error)
	// PassRequests are the last request for each workspace.
	PassRequests(ctx context.Context) (map[string]connection.PassRequest, error)
	// Delete forgets a workspace and what belongs to it.
	Delete(ctx context.Context, workspace string) error
	PutConfirmation(ctx context.Context, confirmation connection.Confirmation) error
	// Confirmations are every confirmation by connection.ConfirmationKey.
	Confirmations(ctx context.Context) (map[string]connection.Confirmation, error)
}

// WorkspaceCallbackPath is where Slack sends the browser back to after an owner
// installs the roster's App into a workspace: a route of the console.
const WorkspaceCallbackPath = "/connect/slack/workspace/callback"

// WorkspaceBind prefixes the workspace key in a workspace connect's signed
// state, so no other flow's state can finish it, and the other way round.
const WorkspaceBind = "slack-workspace:"

// The states a workspace's connection is shown in.
const (
	slackNotConnected = "not_connected"
	slackCreated      = "created"
	slackInstalled    = "installed"
)

// slackWorkspaceRedirect is where Slack sends the owner after installing
// the roster's App.
func (c *Service) slackWorkspaceRedirect(caller consolecall.Caller) string {
	return caller.Origin + WorkspaceCallbackPath
}

// WorkspaceStore is where connected workspaces are kept, or the refusal where the
// deployment keeps none.
func (c *Service) WorkspaceStore() (Workspaces, error) { return c.slackWorkspaceStore() }

func (c *Service) slackWorkspaceStore() (Workspaces, error) {
	if c.d().SlackWorkspaces == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this deployment keeps no state in Kubernetes, so a Slack bot token would not survive a restart"))
	}
	return c.d().SlackWorkspaces, nil
}

// slackWorkspaceApp is the App the roster is created as in a workspace,
// written into the same manifest shape the catalogue's Apps are.
func slackWorkspaceApp(workspace string) slackcatalogue.App {
	name := "sluis-" + workspace
	if len(name) > slackcatalogue.NameLimit {
		name = strings.TrimRight(name[:slackcatalogue.NameLimit], "-")
	}
	return slackcatalogue.App{
		ID: workspace, Workspace: workspace, Name: name,
		Description: "Keeps this workspace's channels in step with the directory.",
		BotScopes:   slices.Clone(connection.BotScopes),
	}
}

const tokenHint = "that is not an app configuration token: generate one at api.slack.com/apps " +
	"under \"Your App Configuration Tokens\" (one word, starting xoxe.)"

// BeginSlackWorkspaceConnect creates the roster's App in a workspace, or
// prepares the reinstall of one created earlier, and returns Slack's
// authorize URL.
//
// The configuration token is read from the request into one local, used
// for one call, and dropped: it is not written to the Secret or a record,
// not put into the signed state, not in any log line or audit record, and
// not in any error returned (errors from Slack never carry it).
func (c *Service) BeginSlackWorkspaceConnect(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.BeginSlackWorkspaceConnectRequest,
) (*directoryrosterv1.BeginSlackWorkspaceConnectResponse, error) {
	who, err := consoleauthz.RequireAnywhere(caller, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	store, err := c.slackWorkspaceStore()
	if err != nil {
		return nil, err
	}
	workspace := strings.TrimSpace(req.GetWorkspace())
	if !status.ValidWorkspace(workspace) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not a workspace key", workspace))
	}
	if err = c.requireDeclaredSlack(workspace); err != nil {
		return nil, err
	}
	record, credential, found, err := store.Get(ctx, workspace)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	// Who may connect depends on whether there is anything to own it yet.
	// A workspace already created belongs to its recorded owner (and to the
	// installation-wide operator); one not yet created is connectable by
	// either, and [consoleauthz.ResolveOwner] says who then owns it.
	var owner string
	if found {
		if who, err = c.requireSlack(ctx, caller, access.RoleOperator, workspace); err != nil {
			return nil, err
		}
		owner = record.Owner
	} else {
		dirs, err := c.directories(ctx)
		if err != nil {
			return nil, err
		}
		if owner, err = consoleauthz.ResolveOwner(who, req.GetOwner(), dirs); err != nil {
			return nil, err
		}
	}
	configToken := strings.TrimSpace(req.GetConfigurationToken())
	if configToken != "" && !plausibleConfigurationToken(configToken) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(tokenHint))
	}
	app := slackWorkspaceApp(workspace)
	manifest, err := slackcatalogue.Manifest(app, c.slackWorkspaceRedirect(caller))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, slackTimeout)
	defer cancel()
	switch {
	case !found:
		if configToken == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(tokenHint))
		}
		created, err := c.slackSetup().CreateApp(callCtx, configToken, manifest)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("slack refused to create the App: %w", err))
		}
		record = connection.Record{
			Workspace: workspace, Owner: owner, AppID: created.AppID, AuthorizeURL: created.OAuthAuthorizeURL,
			ManifestScopes: slices.Clone(connection.BotScopes), ConnectedAt: time.Now().UTC(), ConnectedBy: who.Who(),
		}
		credential = connection.Credential{
			Workspace: workspace, AppID: created.AppID,
			ClientID: created.Credentials.ClientID, ClientSecret: created.Credentials.ClientSecret,
		}
		if err = store.Put(ctx, record, credential); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf(
				"slack created the App and it could not be saved here: delete it at %s and connect again: %w",
				AppSettingsURL(created.AppID), err))
		}
	case configToken != "":
		// Scopes the roster asks for now reach an App created earlier only
		// through its manifest, and Slack changes a manifest only for a
		// configuration token.
		if _, err = c.slackSetup().UpdateApp(callCtx, configToken, record.AppID, manifest); err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("slack refused to update the App: %w", err))
		}
		record.ManifestScopes = slices.Clone(connection.BotScopes)
		if err = store.Put(ctx, record, credential); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
	case len(slackapp.MissingScopes(strings.Join(record.ManifestScopes, ","), connection.BotScopes)) > 0:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"the roster now asks for scopes %s was created without: paste an app configuration token so the App can be updated first, "+
				"or a reinstall would grant nothing new", workspace))
	}

	// The team is preselected once it is known; the first install has none
	// to offer, and the team Slack reports back becomes the recorded one. The
	// state is the console's to add: it signs, and pins the browser.
	target, err := slackAuthorizeURL(record.AuthorizeURL, c.slackWorkspaceRedirect(caller), record.TeamID, connection.BotScopes)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &directoryrosterv1.BeginSlackWorkspaceConnectResponse{Url: target}, nil
}

// RequestSlackPass asks the Slack controller to pass over a workspace now.
// The request is a marker the controller notices within its poll (see the
// controller's watch); a second one under a minute after the first is
// refused, so the button cannot be leaned on.
func (c *Service) RequestSlackPass(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.RequestSlackPassRequest,
) (*directoryrosterv1.RequestSlackPassResponse, error) {
	if _, err := consoleauthz.RequireAnywhere(caller, access.RoleOperator); err != nil {
		return nil, err
	}
	store, err := c.slackWorkspaceStore()
	if err != nil {
		return nil, err
	}
	workspace := strings.TrimSpace(req.GetWorkspace())
	if !status.ValidWorkspace(workspace) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not a workspace key", workspace))
	}
	who, err := c.requireSlack(ctx, caller, access.RoleOperator, workspace)
	if err != nil {
		return nil, err
	}
	book, err := c.book(ctx)
	if err != nil {
		return nil, err
	}
	if !book[workspace].Installed() {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("slack workspace %s is not installed: there is nothing for a pass to act with", workspace))
	}
	now := time.Now().UTC()
	kept, last, err := store.RequestPass(ctx, connection.PassRequest{Workspace: workspace, At: now, By: who.Who()})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if !kept {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf(
			"a pass over %s was asked for %s ago: the controller notices a request within a minute, so wait for it",
			workspace, now.Sub(last).Round(time.Second)))
	}
	c.notify(ctx, port.ControllerSlack, workspace)
	c.log().InfoContext(ctx, "a Slack pass was requested", logattr.SafeString("workspace", workspace), logattr.SafeString("by", who.Who()))
	return &directoryrosterv1.RequestSlackPassResponse{RequestedAt: timestampOf(now)}, nil
}

// DisconnectSlackWorkspace revokes the bot token and forgets the
// connection.
func (c *Service) DisconnectSlackWorkspace(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.DisconnectSlackWorkspaceRequest,
) (*directoryrosterv1.DisconnectSlackWorkspaceResponse, error) {
	if _, err := consoleauthz.RequireAnywhere(caller, access.RoleOperator); err != nil {
		return nil, err
	}
	store, err := c.slackWorkspaceStore()
	if err != nil {
		return nil, err
	}
	workspace := strings.TrimSpace(req.GetWorkspace())
	if !status.ValidWorkspace(workspace) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not a workspace key", workspace))
	}
	who, err := c.requireSlack(ctx, caller, access.RoleOperator, workspace)
	if err != nil {
		return nil, err
	}
	record, credential, found, err := store.Get(ctx, workspace)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if !found {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("slack workspace %s is not connected", workspace))
	}

	revoked, reason := false, ""
	if credential.BotToken != "" {
		if err = c.slackRevoke(ctx, credential.BotToken); err != nil {
			if !req.GetForgetAnyway() {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf(
					"slack would not revoke the bot token, so the connection was kept: try again, or forget it anyway and remove the App in Slack: %w", err))
			}
			reason = "forgotten without revoking the bot token, which Slack would not revoke"
		} else {
			revoked = true
		}
	}
	if err = store.Delete(ctx, workspace); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	c.record(ctx, audit.SlackWorkspaceDisconnected(consoleauthz.Actor(who),
		audit.SlackWorkspace{Key: workspace, Team: record.TeamID, App: record.AppID, Owner: record.Owner}, revoked, reason))
	return &directoryrosterv1.DisconnectSlackWorkspaceResponse{Revoked: revoked}, nil
}

// CompleteWorkspace finishes a workspace's install: the console has verified
// where Slack sent the owner after installing the roster's App, and forwards the
// one-time code for the bot token.
//
// The first install records which Slack team the workspace is. Every later
// install must belong to the same team; a token for any other is revoked and
// dropped without being stored, and the refusal is recorded.
func (c *Service) CompleteWorkspace(ctx context.Context, caller consolecall.Caller, req CompleteRequest) (Completion, error) {
	subject := audit.SlackWorkspace{Key: "unknown", App: "unknown"}
	actor := caller.Actor
	refuse := func(code int, summary, detail, reason string, causes []string) (Completion, error) {
		c.record(ctx, audit.SlackWorkspaceConnectRefused(audit.Identified(actor), subject, reason))
		return refused(code, summary, detail, reason, causes), nil
	}
	if key, ok := strings.CutPrefix(req.Bind, WorkspaceBind); ok && status.ValidWorkspace(key) {
		subject.Key = key
	}
	if r := c.recheck(ctx, caller, req); r != nil {
		return refuse(r.Status, r.Summary, r.Detail, r.Reason, r.Causes)
	}
	if actor == "" {
		return refuse(http.StatusForbidden, "Installing a Slack App needs the operator role.", "", "the state names no operator", nil)
	}
	workspace, isWorkspace := strings.CutPrefix(req.Bind, WorkspaceBind)
	if !isWorkspace || !status.ValidWorkspace(workspace) {
		return refuse(http.StatusBadRequest, "This is not a Slack workspace's connect.", "", "the state is for another flow", nil)
	}
	if c.d().SlackWorkspaces == nil {
		return refuse(http.StatusConflict, "This deployment keeps no Slack connections.", "", "no store for Slack connections", nil)
	}
	if req.Denied != "" {
		return refuse(http.StatusBadRequest, "The installation was not approved in Slack.", req.Denied, "the install was not approved in Slack: "+req.Denied, nil)
	}
	if err := c.requireDeclaredSlack(workspace); err != nil {
		return refuse(http.StatusConflict, "The policy no longer names this workspace.", said(err), "the policy no longer names the workspace", nil)
	}
	store := c.d().SlackWorkspaces
	record, credential, found, err := store.Get(ctx, workspace)
	if err != nil || !found {
		return refuse(http.StatusConflict, fmt.Sprintf("There is no App for %s to finish installing. Connect it first.", workspace), errString(err),
			"the workspace has no App to finish installing", nil)
	}
	subject.App, subject.Owner = record.AppID, record.Owner

	callCtx, cancel := context.WithTimeout(ctx, slackTimeout)
	defer cancel()
	installed, err := c.slackSetup().OAuthAccess(callCtx, credential.ClientID, credential.ClientSecret, req.Code, c.slackWorkspaceRedirect(caller))
	if err != nil {
		return refuse(http.StatusConflict, "Slack accepted the install, and then would not hand over the bot token.", err.Error(),
			"slack's oauth.v2.access refused: "+logattr.Error(err), exchangeCauses)
	}
	subject.Team = installed.TeamID
	team := record.TeamID
	if team == "" {
		// The first install: the team Slack reports is the one recorded, as
		// long as no other workspace key already is that team, which would
		// make an install ambiguous.
		book, bookErr := c.book(ctx)
		if bookErr != nil {
			revokeErr := c.slackRevoke(ctx, installed.BotToken)
			return refuse(http.StatusConflict, "The connection records could not be read.", said(bookErr),
				"the connection records could not be read: "+logattr.Error(bookErr)+"; "+revokedWords(revokeErr), nil)
		}
		for other := range book {
			if other != workspace && book[other].TeamID == installed.TeamID {
				revokeErr := c.slackRevoke(ctx, installed.BotToken)
				return refuse(http.StatusConflict, fmt.Sprintf(
					"The App was installed into workspace %s (%s), which is already connected as %q. %s",
					installed.TeamID, installed.TeamName, other, refusedToken(revokeErr)), "",
					fmt.Sprintf("team %s is already connected as %s; %s", installed.TeamID, other, revokedWords(revokeErr)), nil)
			}
		}
		team = installed.TeamID
	}
	if installed.TeamID != team {
		revokeErr := c.slackRevoke(ctx, installed.BotToken)
		return refuse(http.StatusConflict, fmt.Sprintf(
			"The App was installed into workspace %s (%s), and %q was first installed as %s. %s "+
				"Install again from the right workspace.",
			installed.TeamID, installed.TeamName, workspace, team, refusedToken(revokeErr)), "",
			fmt.Sprintf("installed into team %s, and the workspace was first installed as %s; %s", installed.TeamID, team, revokedWords(revokeErr)), nil)
	}

	record.TeamID, record.BotUserID = installed.TeamID, installed.BotUserID
	record.Scopes, record.ConnectedAt, record.ConnectedBy = scopeList(installed.Scope), time.Now().UTC(), actor
	credential.BotToken = installed.BotToken
	if err = store.Put(ctx, record, credential); err != nil {
		// A token nobody keeps is one nobody can revoke later.
		revokeErr := c.slackRevoke(ctx, installed.BotToken)
		return refuse(http.StatusConflict, "The App is installed and its token could not be saved here. Install it again.", err.Error(),
			"the token could not be kept: "+logattr.Error(err)+"; "+revokedWords(revokeErr), nil)
	}
	c.log().InfoContext(ctx, "a Slack workspace was connected", logattr.SafeString("workspace", workspace),
		logattr.SafeString("team", installed.TeamID),
		logattr.SafeString("scopes", strings.Join(record.Scopes, ",")), logattr.SafeString("by", actor))
	c.record(ctx, audit.SlackWorkspaceConnected(audit.Identified(actor), subject))
	return Completion{}, nil
}

// requireDeclaredSlack refuses a workspace key the policy does not name. The
// policy knows a workspace by its key alone (and the channels bound in it);
// everything else about it is recorded when it is connected.
func (c *Service) requireDeclaredSlack(workspace string) error {
	if c.d().Authorizer == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("no policy is loaded"))
	}
	if !c.d().Authorizer.Policy().SlackWorkspaceDeclared(workspace) {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("the policy's slack.workspaces does not name workspace %q", workspace))
	}
	return nil
}

// ChangeSlackWorkspaceOwner changes the directory recorded as a connected
// workspace's owner, or removes it. The installation-wide operator alone: an
// owner that could hand its workspace to another, or take one, would make
// "who may operate this" a thing its operators decide.
func (c *Service) ChangeSlackWorkspaceOwner(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.ChangeSlackWorkspaceOwnerRequest,
) (*directoryrosterv1.ChangeSlackWorkspaceOwnerResponse, error) {
	workspace, owner := strings.TrimSpace(req.GetWorkspace()), strings.TrimSpace(req.GetOwner())
	who, err := consoleauthz.RequireOwnerChange(caller, owner, func() ([]consoleauthz.Directory, error) { return c.directories(ctx) })
	if err != nil {
		return nil, err
	}
	store, err := c.slackWorkspaceStore()
	if err != nil {
		return nil, err
	}
	if !status.ValidWorkspace(workspace) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not a workspace key", workspace))
	}
	previous, found, err := store.SetOwner(ctx, workspace, owner)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if !found {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("slack workspace %s is not connected: its owner is chosen when it is connected", workspace))
	}
	if previous == owner {
		return &directoryrosterv1.ChangeSlackWorkspaceOwnerResponse{}, nil
	}
	c.record(ctx, audit.SlackWorkspaceOwnerChanged(consoleauthz.Actor(who), workspace, previous, owner))
	return &directoryrosterv1.ChangeSlackWorkspaceOwnerResponse{}, nil
}
