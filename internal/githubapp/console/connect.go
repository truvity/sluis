package console

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/status"
)

// Connections is where connected organisations are kept: a record the console
// shows, a credential only the controller acts with.
type Connections = connection.Store

// githubBind prefixes the organisation in a flow's signed state, so that a
// GitHub state can never finish a directory's consent — whose callback
// would read it as a workspace id and match none — and the other way
// round.
const githubBind = consolewire.BindOrg

func (c *Service) githubHTTP() *http.Client {
	if c.deps.HTTP != nil {
		return c.deps.HTTP
	}
	return &http.Client{Timeout: githubTimeout}
}

// githubRoot is the origin the callbacks are at.
//
// The console says it in the snapshot of the call (its own address), so a module
// in another process needs no copy of it in its document; a configured one
// wins.
func (c *Service) githubRoot(ctx context.Context) string {
	switch {
	case c.deps.RootURL != "":
		return strings.TrimSuffix(c.deps.RootURL, "/")
	case callerOf(ctx).Origin != "":
		return strings.TrimSuffix(callerOf(ctx).Origin, "/")
	}
	return strings.TrimSuffix(c.deps.PublicURL, "/")
}

// BeginGitHubConnect starts connecting an organisation.
//
// Only one the policy binds. Connecting an organisation nothing will
// manage installs an App with members:write for no reason, and the
// likeliest cause is a typo in the login — which GitHub would answer
// with a 404 on its create page, after the operator has left this one.
//
// The URL it returns carries [consolewire.StatePlaceholder] where the signed
// state belongs: the issuer signs it, with the caller as its actor.
func (c *Service) BeginGitHubConnect(ctx context.Context, org, requestedOwner string) (consolewire.Begun, error) {
	org = strings.TrimSpace(org)
	id, err := requireAnywhere(ctx, access.RoleOperator)
	if err != nil {
		return consolewire.Begun{}, err
	}
	begun, err := c.beginOrganisationConnect(ctx, id, org, requestedOwner)
	if err != nil {
		return consolewire.Begun{}, err
	}
	return begun.wire(), nil
}

// beginOrganisationConnect is the work of it, which
// [Service.BeginGitHubAppConnect] does too.
//
// Who may begin, and who will own what it connects, is decided here against
// the record: an organisation already created is the recorded owner's to
// finish (the installation-wide operator's too); one not yet created is
// connectable by the installation-wide operator, or by an operator of a
// directory, and [resolveOwner] says who then owns it.
func (c *Service) beginOrganisationConnect(
	ctx context.Context, who access.Identity, org, requestedOwner string,
) (githubBegin, error) {
	switch {
	case !status.ValidOrg(org):
		return githubBegin{}, connect.Errorf(connect.CodeInvalidArgument, "%q is not an organisation login", org)
	case c.deps.Orgs == nil:
		return githubBegin{}, connect.NewError(connect.CodeFailedPrecondition,
			"this deployment keeps no state in Kubernetes, so a connected organisation would not survive a restart")
	}
	if _, bound := c.bindings()[org]; !bound {
		return githubBegin{}, connect.Errorf(connect.CodeFailedPrecondition,
			"the policy binds no organisation %s: bind its teams first, so nothing is connected that nothing manages", org)
	}
	records, err := c.deps.Orgs.List(ctx)
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	existing, connected := recordOf(records, org)
	owner := existing.Owner
	if connected {
		if _, err = requireOwner(ctx, access.RoleOperator, existing.Owner, org); err != nil {
			return githubBegin{}, err
		}
	} else {
		dirs, err := c.directories(ctx)
		if err != nil {
			return githubBegin{}, err
		}
		if owner, err = resolveOwner(who, requestedOwner, dirs); err != nil {
			return githubBegin{}, err
		}
	}
	if connected && existing.Installed() {
		return githubBegin{}, connect.Errorf(connect.CodeFailedPrecondition,
			"%s is already connected through %s: disconnect it first, or a second App would sit beside the first", org, existing.AppSlug)
	}

	// The state carries who asked: the callbacks are redirects from GitHub
	// and carry no identity of their own. The issuer signs it with the caller
	// as its actor, once this returns.
	state := consolewire.StatePlaceholder
	out := githubBegin{state: state, bind: githubBind + org, owner: owner}
	if connected {
		// Created and never installed: pick up where the owner left off
		// rather than creating a second App.
		out.url = githubapp.InstallURL(existing.AppSlug, state)
		return out, nil
	}
	root := c.githubRoot(ctx)
	manifest, err := json.Marshal(githubapp.NewManifestNamed(org, c.orgAppName(org), c.publicURL(ctx), root+consolewire.CallbackPath, root+consolewire.SetupPath))
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	out.url, out.manifest = githubapp.CreateURL(org, state), string(manifest)
	return out, nil
}

// DisconnectGitHubOrganisation revokes, then forgets.
//
// The revoke is uninstalling: GitHub's API cannot delete an App, so the
// registration stays for its owner to delete, and the response says where.
// Once uninstalled and forgotten nothing can act through it — nobody else
// ever held its key. A failed uninstall still forgets: the operator asked
// for this organisation to stop being managed, and the detail says what
// is left to do by hand.
func (c *Service) DisconnectGitHubOrganisation(
	ctx context.Context, req *directoryrosterv1.DisconnectGitHubOrganisationRequest,
) (*directoryrosterv1.DisconnectGitHubOrganisationResponse, error) {
	org := strings.TrimSpace(req.GetOrg())
	if _, err := c.requireOrg(ctx, access.RoleOperator, org); err != nil {
		return nil, err
	}
	gone, err := c.disconnectOrganisation(ctx, org)
	if err != nil {
		return nil, err
	}
	return &directoryrosterv1.DisconnectGitHubOrganisationResponse{
		Uninstalled: gone.uninstalled, Detail: gone.detail, AppSettingsUrl: gone.settingsURL,
	}, nil
}

// disconnectOrganisation is the work of it, which
// [Service.DisconnectGitHubApp] does too.
func (c *Service) disconnectOrganisation(ctx context.Context, org string) (githubDisconnect, error) {
	switch {
	case !status.ValidOrg(org):
		return githubDisconnect{}, connect.Errorf(connect.CodeInvalidArgument, "%q is not an organisation login", org)
	case c.deps.Orgs == nil:
		return githubDisconnect{}, connect.NewError(connect.CodeFailedPrecondition, "this deployment keeps no connected organisations")
	}
	records, err := c.deps.Orgs.List(ctx)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	record, connected := recordOf(records, org)
	credential, hasCredential, err := c.deps.Orgs.Credential(ctx, org)
	if err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	if !connected && !hasCredential {
		return githubDisconnect{}, connect.Errorf(connect.CodeNotFound, "%s is not connected", org)
	}

	out := c.uninstall(ctx, credential.AppID, credential.InstallationID, credential.PrivateKey)
	out.settingsURL = appSettingsURL(org, record.AppSlug)
	if err = c.deps.Orgs.Delete(ctx, org); err != nil {
		return githubDisconnect{}, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	c.record(ctx, audit.GitHubOrgDisconnected(actorOf(ctx), org, out.uninstalled, out.detail))
	return out, nil
}

func recordOf(records []connection.Record, org string) (connection.Record, bool) {
	for i := range records {
		if records[i].Org == org {
			return records[i], true
		}
	}
	return connection.Record{}, false
}

// ChangeGitHubOrganisationOwner changes the directory recorded as a
// connected organisation's owner, or removes it. The installation-wide
// operator alone: an owner that could hand its organisation to another, or
// take one, would make "who may operate this" a thing its operators decide.
func (c *Service) ChangeGitHubOrganisationOwner(
	ctx context.Context, req *directoryrosterv1.ChangeGitHubOrganisationOwnerRequest,
) (*directoryrosterv1.ChangeGitHubOrganisationOwnerResponse, error) {
	org, owner := strings.TrimSpace(req.GetOrg()), strings.TrimSpace(req.GetOwnerDirectory())
	who, err := c.requireOwnerChange(ctx, owner)
	if err != nil {
		return nil, err
	}
	switch {
	case !status.ValidOrg(org):
		return nil, connect.Errorf(connect.CodeInvalidArgument, "%q is not an organisation login", org)
	case c.deps.Orgs == nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, "this deployment keeps no connected organisations")
	}
	previous, connected, err := c.deps.Orgs.SetOwner(ctx, org, owner)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err.Error()).WithCause(err)
	}
	if !connected {
		return nil, connect.Errorf(connect.CodeFailedPrecondition,
			"%s is not connected: its owner is chosen when it is connected", org)
	}
	if previous == owner {
		return &directoryrosterv1.ChangeGitHubOrganisationOwnerResponse{}, nil
	}
	c.record(ctx, audit.GitHubOrgOwnerChanged(identityActor(who), org, previous, owner))
	return &directoryrosterv1.ChangeGitHubOrganisationOwnerResponse{}, nil
}

// publicURL is where a browser reaches the console: configured, else the
// address the console gave in the call.
func (c *Service) publicURL(ctx context.Context) string {
	if c.deps.PublicURL != "" {
		return c.deps.PublicURL
	}
	return callerOf(ctx).Origin
}
