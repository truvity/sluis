package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/gen/directoryroster/v1/directoryrosterv1connect"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consolecall"
	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/settings"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
	"github.com/truvity/sluis/internal/version"
)

// The connector seams live with the Google module, which owns the OAuth client
// and the keys (internal/googleapp/console); the console names them here.
type (
	// Connector starts and finishes an admin-consent flow for one backend.
	Connector = googleconsole.Connector
	// ClientVerifier is a connector that can say whether the registered client
	// would be accepted, without a person.
	ClientVerifier = googleconsole.ClientVerifier
	// KeyConnector is a connector that takes a service-account key instead of
	// a consent flow.
	KeyConnector = googleconsole.KeyConnector
	// CredentialReopener is a connector that can turn a stored credential back
	// into a reader.
	CredentialReopener = googleconsole.CredentialReopener
	// SignInConnector is a connector that can also say who somebody is.
	SignInConnector = googleconsole.SignInConnector
)

// ConsoleDeps is everything the operator services need.
type ConsoleDeps struct {
	// Log is where the console logs what is not an audit record; nil is the
	// default logger.
	Log        *slog.Logger
	Hub        *hub.Hub
	Authorizer *access.Authorizer
	Settings   settings.Store
	State      *access.StateCodec
	Connectors []Connector
	// Recovery is the way in when the ordinary one is broken; nil is a
	// deployment with none. The console reports its shape, because only
	// a stored password is a standing credential worth a banner.
	Recovery     Recovery
	LoginSources []string
	CacheBackend string
	SecureCookie bool
	// PublicURL is where a browser reaches this console,
	// including route.pathPrefix, when one is set, because this is the
	// console's own address and the console may sit under a path.
	//
	// It is what makes the redirect URI reportable: a runbook can only
	// say "your hostname plus this path", and an operator retyping a
	// hostname into a cloud console is exactly where a day-one setup goes
	// wrong.
	PublicURL string
	// RootURL is the host's ROOT, never carrying route.pathPrefix even
	// when PublicURL does. The bootstrap surface (route.bootstrapPaths:
	// /login, /connect) is deliberately never moved under the console's
	// path -- it is what the FIRST operator reaches before there is a
	// console session to authenticate, on its own HTTPRoute, and the
	// registered OAuth redirect URIs for it are at the domain root. Empty
	// falls back to PublicURL, which is exactly right wherever no prefix
	// is configured: the two are then the same address.
	RootURL string
	// IssuerURL is the token service this console is behind, when it is
	// behind one. With a shared OAuth client the sign-in redirect belongs
	// to that host rather than this one, and only this side knows it.
	IssuerURL string
	// SignIn reports whether this console signs people in itself, which
	// is the other place a sign-in redirect can land.
	SignIn bool
	// GitHub is where the GitHub controller's reports are read. Nil is a
	// deployment keeping no state in Kubernetes, which has nowhere for a
	// controller to report to; the GitHub page then shows the bindings
	// alone and says why.
	GitHub GitHubReports
	// GitHubOrgs is where connected organisations are kept. Nil is a
	// deployment keeping no state in Kubernetes, which can connect none.
	GitHubOrgs GitHubConnections
	// GitHubLinkApp is where the link App is kept. Nil is a deployment
	// where nobody can link an account.
	GitHubLinkApp GitHubLinkApp
	// GitHubLinks is where people's links are kept. Nil, likewise.
	GitHubLinks GitHubLinks
	// GitHubConfirmations is where operators confirm removal sets over the
	// limit. Nil confirms nothing.
	GitHubConfirmations GitHubConfirmations
	// GitHubOrgAppName is the name the organisation's App is created with,
	// empty for the default (<org>-access-roster). Nil is all defaults.
	GitHubOrgAppName func(org string) string
	// GitHubLinkAppName is the name the link App is created with, empty for
	// the default (<owner>-access-roster-link).
	GitHubLinkAppName string
	// GitHubRunnerApps is where runner Apps are kept. Nil is a deployment
	// that keeps none.
	GitHubRunnerApps GitHubRunnerApps
	// GitHubRunnerTiers are the runner tiers an operator may create an App
	// for. Empty keeps no runner Apps, whatever the store.
	GitHubRunnerTiers []string
	// GitHubCatalogue is every App the deployment declares. Nil declares
	// none.
	GitHubCatalogue *ghconsole.Catalogue
	// GitHubCatalogueApps is where catalogue Apps are kept. Nil is a
	// deployment keeping no state in Kubernetes, which can create none.
	GitHubCatalogueApps GitHubCatalogueApps
	// SlackCatalogue is every Slack App the deployment declares. Nil
	// declares none.
	SlackCatalogue *slackconsole.Catalogue
	// SlackCatalogueApps is where catalogue Slack Apps are kept. Nil is a
	// deployment keeping no state in Kubernetes, which can create none.
	SlackCatalogueApps SlackCatalogueApps
	// SlackShared is where Slack Connect channel definitions are kept. Nil is
	// a deployment keeping no state in Kubernetes, which can define none.
	SlackShared SlackSharedRecords
	// SlackChannels is where console channels' records are kept. Nil is a
	// deployment keeping no state in Kubernetes, which can manage none.
	SlackChannels SlackChannelRecords
	// SlackStatus is what the Slack controller last reported. Nil shows every
	// channel as not reported.
	SlackStatus SlackStatusReports
	// SlackWorkspaces is where connected Slack workspaces and operators'
	// confirmations are kept. Nil connects and confirms nothing. The
	// controller's report is SlackStatus.
	SlackWorkspaces SlackWorkspaces
	// SlackAPI configures the calls to Slack that creating and installing
	// an App make: a test points them at a fake. Nil is Slack.
	SlackAPI []slackconsole.APIOption
	// GitHubMints is the last installation tokens asked of each App, kept
	// by the half of this service that mints them. Nil is a deployment
	// that mints none, or one whose issuer is another process; an App's
	// page then says it keeps none rather than showing an empty table.
	GitHubMints *ghconsole.MintRing
	// GitHubHTTP makes the calls to GitHub that connecting and
	// disconnecting need. Nil is a client with a short timeout.
	GitHubHTTP *http.Client
	// Cloudflare is the minter of Cloudflare credentials. Nil is a
	// deployment with no `cloudflare` section and no Cloudflare page; it is
	// connected late, by [ConsoleServer.UseCloudflare].
	Cloudflare CloudflareModule
	// Backup is the backup module and the restore function, read through
	// modcall as the caller class `console`. Nil is a deployment with neither
	// and no Backups page; it is connected late, by [ConsoleServer.UseBackup].
	Backup BackupModule
	// WebhookHTTP sends the signed ping a webhook rotation checks a new
	// target with. Nil is a client with a short timeout that follows no
	// redirect, so a target that moves counts as one that refused.
	WebhookHTTP *http.Client
	// WebhookPingWindow is how long a rotation waits for the new target to
	// accept the new secret, and WebhookPingEvery how often it asks. Zero is
	// two minutes and five seconds.
	WebhookPingWindow, WebhookPingEvery time.Duration
	// Audit records what an identity did through the console. Nil records
	// nothing.
	Audit audit.Recorder
	// Trigger is told which target a write of the console concerns, so its
	// tick runs soon (docs/decisions/0029). With the legacy adapter the
	// trigger is in this process, and the controllers are other processes: the
	// notification reaches nobody there, and the controller notices the same
	// write through the records it mounts (see docs/concepts/sluis/ports.md). Nil
	// notifies nobody.
	Trigger port.Trigger
	// Modules is how the console reaches the provider modules for the
	// operations that move into them (docs/decisions/0071, v1.76 D-v176-1 b),
	// each as the caller class `console`. A nil caller is the in-process
	// servers, [modcall.Local]; the wiring sets one per module it split out.
	Modules consolecall.Callers
}

// Console serves WorkspaceService, SettingsService and AccessService on
// the console listener.
type Console struct {
	deps ConsoleDeps
	// github, slack and google are the provider modules in this process, built
	// over the console's own stores when the first page asks, and the way to call
	// each as the console, for a deployment that has not split it out.
	githubOnce  sync.Once
	github      *ghconsole.Service
	githubLocal modcall.Caller
	slackOnce   sync.Once
	slack       *slackconsole.Service
	slackLocal  modcall.Caller
	connectors  map[string]Connector
	// googleOnce makes googleClient: the module's calls, remote or in process.
	googleOnce   sync.Once
	googleClient *googleconsole.Client
}

var (
	_ directoryrosterv1connect.WorkspaceServiceHandler          = (*Console)(nil)
	_ directoryrosterv1connect.SettingsServiceHandler           = (*Console)(nil)
	_ directoryrosterv1connect.AccessServiceHandler             = (*Console)(nil)
	_ directoryrosterv1connect.GitHubServiceHandler             = (*Console)(nil)
	_ directoryrosterv1connect.SlackAppServiceHandler           = (*Console)(nil)
	_ directoryrosterv1connect.SlackSharedChannelServiceHandler = (*Console)(nil)
	_ directoryrosterv1connect.SlackChannelServiceHandler       = (*Console)(nil)
	_ directoryrosterv1connect.SlackServiceHandler              = (*Console)(nil)
	_ directoryrosterv1connect.CloudflareServiceHandler         = (*Console)(nil)
	_ directoryrosterv1connect.BackupServiceHandler             = (*Console)(nil)
)

// NewConsole returns the operator services.
//
// It used to install a console layer of the policy here, so that a
// membership added before a restart was in force after it. There is no
// console layer any more: who is in which internal group is
// the policy, rendered from the installation's own access model and
// reviewed in git. A console that could disagree with git was a second
// source of truth and a merge to reconcile them.
func NewConsole(_ context.Context, deps ConsoleDeps) (*Console, error) {
	// The GitHub, Slack and Google modules in this process are built over the
	// console's own stores when the first page asks ([Console.githubModule],
	// [Console.slackModule], [Console.google]), so they are left unset here, not
	// replaced by an empty one.
	github, google, slack := deps.Modules.GitHub, deps.Modules.Google, deps.Modules.Slack
	deps.Modules = deps.Modules.OrLocal(modcall.Local{})
	deps.Modules.GitHub, deps.Modules.Google, deps.Modules.Slack = github, google, slack
	c := &Console{deps: deps, connectors: map[string]Connector{}}
	for _, conn := range deps.Connectors {
		c.connectors[conn.Kind()] = conn
	}
	return c, nil
}

// log is the console's logger: the one it was given, or the default.
func (c *Console) log() *slog.Logger {
	if c.deps.Log != nil {
		return c.deps.Log
	}
	return slog.Default()
}

// ------------------------------------------------------- WorkspaceService

// The Workspace service is the Google module's (internal/googleapp/console).
// The console authenticates the person, takes the coarse gate (the role needed
// anywhere), and asks the module as that snapshot; the module checks the
// workspace and does the work. A person's refusal reaches them in the module's
// fixed words.

// google is the console's calls to the Google module. When the document sets
// no `google.remote` the module is this process: its service runs over the
// hub and the connectors this console holds, through the same JSON as a
// remote one.
func (c *Console) google() *googleconsole.Client {
	c.googleOnce.Do(func() {
		caller := c.deps.Modules.Google
		if caller == nil {
			svc := googleconsole.NewService(c.deps.Hub, slices.Collect(maps.Values(c.connectors)), c.deps.Audit, c.log())
			caller = modcall.Local{googleconsole.Module: svc.Server()}.As(consolecall.Class)
		}
		c.googleClient = googleconsole.NewClient(caller)
	})
	return c.googleClient
}

// ListWorkspaces implements the operator contract.
func (c *Console) ListWorkspaces(
	ctx context.Context, _ *connect.Request[directoryrosterv1.ListWorkspacesRequest],
) (*connect.Response[directoryrosterv1.ListWorkspacesResponse], error) {
	id, err := requireAnywhere(ctx, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	out, err := c.google().ListWorkspaces(ctx, consolecall.Snapshot(id))
	if err != nil {
		return nil, googleconsole.ConnectError(err)
	}
	return connect.NewResponse(out), nil
}

// BeginConnect implements the operator contract: it returns the consent
// URL and sets the state cookie the callback checks.
func (c *Console) BeginConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginConnectRequest],
) (*connect.Response[directoryrosterv1.BeginConnectResponse], error) {
	url, cookie, err := c.beginFlow(ctx, req.Msg.GetBackend(), "")
	if err != nil {
		return nil, err
	}
	resp := connect.NewResponse(&directoryrosterv1.BeginConnectResponse{ConsentUrl: url})
	resp.Header().Add("Set-Cookie", cookie)
	return resp, nil
}

// Reconnect implements the operator contract: the same flow, bound to an
// existing workspace so the callback can refuse a different tenant.
func (c *Console) Reconnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.ReconnectRequest],
) (*connect.Response[directoryrosterv1.ReconnectResponse], error) {
	id := req.Msg.GetWorkspaceId()
	if _, err := requireWorkspace(ctx, access.RoleOperator, id); err != nil {
		return nil, err
	}
	url, cookie, err := c.beginFlow(ctx, directoryrosterv1.Backend_BACKEND_UNSPECIFIED, id)
	if err != nil {
		return nil, err
	}
	resp := connect.NewResponse(&directoryrosterv1.ReconnectResponse{ConsentUrl: url})
	resp.Header().Add("Set-Cookie", cookie)
	return resp, nil
}

// beginFlow starts a consent. The state carries who asked and is signed here:
// the session key stays with the console, the module only puts the state in
// the address it builds.
func (c *Console) beginFlow(
	ctx context.Context, want directoryrosterv1.Backend, bind string,
) (consentURL, setCookie string, err error) {
	id, err := requireRole(ctx, access.RoleOperator)
	if err != nil {
		return "", "", err
	}
	// The state carries who asked. This request is the one the gateway
	// authenticates; the callback is a redirect from Google that need not
	// land on a route the gateway covers at all.
	state, err := c.deps.State.IssueAs(access.Binding{Bind: bind, Actor: id.Who()})
	if err != nil {
		return "", "", connect.NewError(connect.CodeInternal, err)
	}
	cookie := access.ConnectCookie(state, c.deps.SecureCookie, 10*time.Minute)
	if bind != "" {
		consentURL, err = c.google().Reconnect(ctx, consolecall.Snapshot(id), bind, state)
	} else {
		consentURL, err = c.google().BeginConnect(ctx, consolecall.Snapshot(id), backendKind(want), state)
	}
	if err != nil {
		return "", "", googleconsole.ConnectError(err)
	}
	return consentURL, cookie.String(), nil
}

// completeConsent forwards the callback's code to the module. The code is
// single-use and the call is never retried.
func (c *Console) completeConsent(ctx context.Context, caller consolecall.Caller, kind, code, bind string) (googleconsole.CompleteResponse, error) {
	return c.google().CompleteConsent(ctx, caller, googleconsole.CompleteRequest{Backend: kind, Code: code, Bind: bind})
}

// UploadKey implements the operator contract. The key passes through this
// handler in memory on its way to the module, which is the only holder of it: it
// is bounded here at 64 KB, and no log line or error carries it.
func (c *Console) UploadKey(
	ctx context.Context, req *connect.Request[directoryrosterv1.UploadKeyRequest],
) (*connect.Response[directoryrosterv1.UploadKeyResponse], error) {
	id, err := requireRole(ctx, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetKey()) > googleconsole.MaxKeyBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("a key is at most %d KB", googleconsole.MaxKeyBytes>>10))
	}
	out, err := c.google().UploadKey(ctx, consolecall.Snapshot(id), req.Msg)
	if err != nil {
		return nil, googleconsole.ConnectError(err)
	}
	return connect.NewResponse(out), nil
}

// SetServedDomains implements the operator contract: which of a tenant's
// domains this hub answers for.
func (c *Console) SetServedDomains(
	ctx context.Context, req *connect.Request[directoryrosterv1.SetServedDomainsRequest],
) (*connect.Response[directoryrosterv1.SetServedDomainsResponse], error) {
	id, err := requireWorkspace(ctx, access.RoleOperator, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}
	out, err := c.google().SetServedDomains(ctx, consolecall.Snapshot(id), req.Msg)
	if err != nil {
		return nil, googleconsole.ConnectError(err)
	}
	return connect.NewResponse(out), nil
}

// SetSyncedGroups implements the operator contract: which of a tenant's
// groups this hub keeps.
func (c *Console) SetSyncedGroups(
	ctx context.Context, req *connect.Request[directoryrosterv1.SetSyncedGroupsRequest],
) (*connect.Response[directoryrosterv1.SetSyncedGroupsResponse], error) {
	id, err := requireWorkspace(ctx, access.RoleOperator, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}
	out, err := c.google().SetSyncedGroups(ctx, consolecall.Snapshot(id), req.Msg)
	if err != nil {
		return nil, googleconsole.ConnectError(err)
	}
	return connect.NewResponse(out), nil
}

// Probe implements the operator contract.
func (c *Console) Probe(
	ctx context.Context, req *connect.Request[directoryrosterv1.ProbeRequest],
) (*connect.Response[directoryrosterv1.ProbeResponse], error) {
	id, err := requireWorkspace(ctx, access.RoleOperator, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}
	out, err := c.google().Probe(ctx, consolecall.Snapshot(id), req.Msg)
	if err != nil {
		return nil, googleconsole.ConnectError(err)
	}
	return connect.NewResponse(out), nil
}

// Refresh implements the operator contract: the operator's max_age of zero.
func (c *Console) Refresh(
	ctx context.Context, req *connect.Request[directoryrosterv1.RefreshRequest],
) (*connect.Response[directoryrosterv1.RefreshResponse], error) {
	id, err := requireWorkspace(ctx, access.RoleOperator, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}
	out, err := c.google().Refresh(ctx, consolecall.Snapshot(id), req.Msg)
	if err != nil {
		return nil, googleconsole.ConnectError(err)
	}
	return connect.NewResponse(out), nil
}

// Disconnect implements the operator contract.
func (c *Console) Disconnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectRequest],
) (*connect.Response[directoryrosterv1.DisconnectResponse], error) {
	id, err := requireWorkspace(ctx, access.RoleOperator, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, err
	}
	out, err := c.google().Disconnect(ctx, consolecall.Snapshot(id), req.Msg)
	if err != nil {
		return nil, googleconsole.ConnectError(err)
	}
	return connect.NewResponse(out), nil
}

// -------------------------------------------------------- SettingsService

// GetSettings implements the operator contract. It never returns the
// client secret.
func (c *Console) GetSettings(
	ctx context.Context, _ *connect.Request[directoryrosterv1.GetSettingsRequest],
) (*connect.Response[directoryrosterv1.GetSettingsResponse], error) {
	if _, err := requireRole(ctx, access.RoleViewer); err != nil {
		return nil, err
	}
	client, err := c.deps.Settings.OAuthClient(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	source := directoryrosterv1.ClientSource_CLIENT_SOURCE_CONSOLE
	if client.Declared {
		source = directoryrosterv1.ClientSource_CLIENT_SOURCE_DECLARED
	}
	cfg := c.deps.Hub.Config()
	return connect.NewResponse(&directoryrosterv1.GetSettingsResponse{
		OauthClient: &directoryrosterv1.OAuthClient{
			ClientId:   client.ID,
			Configured: client.Configured(),
			Source:     source,
		},
		RefreshInterval: durationpb.New(cfg.RefreshInterval),
		FreshnessWindow: durationpb.New(cfg.FreshnessWindow),
		ProbeInterval:   durationpb.New(cfg.ProbeInterval),
		CacheBackend:    c.deps.CacheBackend,
		Connectors:      c.connectorKinds(),
		KeyConnectors:   c.keyConnectorKinds(),
		Version:         version.String(),
		Setup:           c.setupGuidance(),
	}), nil
}

// connectorKinds is the backends this deployment can connect, so the
// console offers a button per provider instead of a menu of things that
// may not work.
func (c *Console) connectorKinds() []directoryrosterv1.Backend {
	out := make([]directoryrosterv1.Backend, 0, len(c.connectors))
	for _, kind := range slices.Sorted(maps.Keys(c.connectors)) {
		out = append(out, backendEnum(kind))
	}
	return out
}

// consentRedirects, signInRedirects and backendScopes are the Google module's:
// what each flow returns to and what it asks for is read from the backend by
// that package, so there is one copy to drift.
var (
	consentRedirects = googleconsole.ConsentRedirects
	signInRedirects  = googleconsole.SignInRedirects
	backendScopes    = googleconsole.Scopes
)

// setupGuidance is what must be registered with a backend before a
// workspace can be connected, with this installation's own values filled
// in. It is reported for every backend the hub knows how to guide, not
// only those already configured: the whole point is to be readable before
// an OAuth client exists, because registering one is the step it guides.
func (c *Console) setupGuidance() []*directoryrosterv1.ConnectorSetup {
	// RootURL falls back to PublicURL: wherever route.pathPrefix is unset
	// the two are the same address, which is today's shape exactly.
	root := c.deps.RootURL
	if root == "" {
		root = c.deps.PublicURL
	}

	out := make([]*directoryrosterv1.ConnectorSetup, 0, len(backendScopes))
	for _, kind := range slices.Sorted(maps.Keys(backendScopes)) {
		var uris []string
		if path, ok := consentRedirects[kind]; ok {
			uris = append(uris, root+path)
		}
		// The sign-in redirect, at whichever host actually runs the
		// sign-in: the issuer this console is behind, or this console
		// itself where it signs people in on its own -- and, on its own,
		// also at the host ROOT, because /login is a bootstrap path too.
		// Neither, where nobody signs in with this backend at all — and
		// an operator is then not told to register a URI nothing returns
		// to.
		if path, ok := signInRedirects[kind]; ok {
			switch {
			case c.deps.IssuerURL != "":
				uris = append(uris, strings.TrimSuffix(c.deps.IssuerURL, "/")+path)
			case c.deps.SignIn:
				uris = append(uris, root+path)
			}
		}
		out = append(out, &directoryrosterv1.ConnectorSetup{
			Backend:      backendEnum(kind),
			RedirectUris: uris,
			Scopes:       backendScopes[kind],
		})
	}
	return out
}

// keyConnectorKinds names the backends that take an uploaded key, so the
// console offers that path only where it works.
func (c *Console) keyConnectorKinds() []directoryrosterv1.Backend {
	var out []directoryrosterv1.Backend
	for _, kind := range slices.Sorted(maps.Keys(c.connectors)) {
		if _, ok := c.connectors[kind].(KeyConnector); ok {
			out = append(out, backendEnum(kind))
		}
	}
	return out
}

// ---------------------------------------------------------- AccessService

// WhoAmI implements the operator contract.
func (c *Console) WhoAmI(
	ctx context.Context, _ *connect.Request[directoryrosterv1.WhoAmIRequest],
) (*connect.Response[directoryrosterv1.WhoAmIResponse], error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
	}
	return connect.NewResponse(&directoryrosterv1.WhoAmIResponse{
		Identity: identityProto(id),
		Version:  version.String(),
	}), nil
}

// errNotAuthorised is the one refusal text of Explain, the same for every
// reason, so a refusal tells a caller nothing about the access it asked of.
var errNotAuthorised = errors.New("not authorised")

// Explain implements the operator contract. It discloses what a proof
// would effectively get, so it is for two callers only: an operator, whose
// every look-up is recorded, and the roster controllers, which are
// workloads (a ServiceAccount or an AWS role) the policy places in
// policy.GroupRosterControllers, which ask in bulk and are not recorded.
// Any other workload, whatever role it holds, is treated as a person: an
// operator is served and recorded. Anybody else, a person asking about themselves
// included, is refused with a fixed text and the refusal is recorded.
func (c *Console) Explain(
	ctx context.Context, req *connect.Request[directoryrosterv1.ExplainRequest],
) (*connect.Response[directoryrosterv1.ExplainResponse], error) {
	caller, ok := IdentityFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
	}
	proof := proofFromRequest(req.Msg)
	kind, subject := proofSubject(proof)
	controller := caller.IsRosterController()
	if !controller && !caller.Can(access.RoleOperator) {
		c.record(ctx, audit.AccessExplained(actorOf(ctx), audit.Denied("not authorised"), kind, subject, 0, 0, 0))
		return nil, connect.NewError(connect.CodePermissionDenied, errNotAuthorised)
	}

	explained, err := c.deps.Authorizer.Explain(ctx, proof)
	if err != nil {
		return nil, rpcError(err)
	}
	out := explanationProto(explained, caller, false)
	out.PolicyDigest = c.deps.Authorizer.Policy().Digest()
	if !controller {
		admitted := 0
		for _, client := range out.GetClients() {
			if client.GetAdmitted() {
				admitted++
			}
		}
		c.record(ctx, audit.AccessExplained(actorOf(ctx), audit.Succeeded(), kind, subject,
			len(out.GetHeld()), len(out.GetClients()), admitted))
	}
	return connect.NewResponse(out), nil
}

// proofSubject names what a look-up is about, as the record keeps it.
func proofSubject(p access.Proof) (kind, subject string) {
	switch {
	case p.GitHub != nil:
		return "ci", "github:" + p.GitHub.Repository
	case p.ServiceAccount != nil:
		return "workload", "system:serviceaccount:" + p.ServiceAccount.Namespace + ":" + p.ServiceAccount.Name
	default:
		return "person", p.Email
	}
}

// GetPolicy implements the operator contract.
func (c *Console) GetPolicy(
	ctx context.Context, _ *connect.Request[directoryrosterv1.GetPolicyRequest],
) (*connect.Response[directoryrosterv1.GetPolicyResponse], error) {
	if _, err := requireRole(ctx, access.RoleViewer); err != nil {
		return nil, err
	}
	set := c.deps.Authorizer.Policy()
	groups := set.Groups()
	out := &directoryrosterv1.GetPolicyResponse{
		RecoveryEnabled: recoveryEnabled(c.deps.Recovery),
		RecoveryKind:    recoveryKindOf(c.deps.Recovery),
		// The location, not the password: the console's setup step names the
		// parameter of this instance and layout rather than an example's.
		RecoveryPasswordLocation: recoveryLocationOf(c.deps.Recovery),
		LoginSources:             c.deps.LoginSources,
		Groups:                   make([]*directoryrosterv1.PolicyGroup, 0, len(groups)),
	}
	githubGrants := c.githubGroupGrants()
	for i := range groups {
		group, err := policyGroupProto(&groups[i], githubGrants[groups[i].Name])
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		out.Groups = append(out.Groups, group)
	}
	clients := set.Clients()
	out.Clients = make([]*directoryrosterv1.PolicyClient, 0, len(clients))
	for i := range clients {
		out.Clients = append(out.Clients, clientProto(&clients[i]))
	}
	for _, team := range set.GitHubTeams() {
		out.Teams = append(out.Teams, &directoryrosterv1.PolicyTeam{
			Org:         team.Org,
			Team:        team.Team,
			Members:     team.Members,
			Maintainers: team.Maintainers,
		})
	}
	for _, org := range set.GitHubOrgs() {
		out.Orgs = append(out.Orgs, &directoryrosterv1.PolicyOrg{
			Org: org.Org, Members: org.Members,
		})
	}
	return connect.NewResponse(out), nil
}

// ListDirectoryGroups implements the operator contract: the groups the hub
// has snapshotted, so that a membership is a click rather than a typed
// address.
func (c *Console) ListDirectoryGroups(
	ctx context.Context, req *connect.Request[directoryrosterv1.ListDirectoryGroupsRequest],
) (*connect.Response[directoryrosterv1.ListDirectoryGroupsResponse], error) {
	id, err := requireAnywhere(ctx, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	groups, served, err := c.deps.Hub.ListGroups(ctx, req.Msg.GetDomain(), nil)
	if err != nil {
		return nil, rpcError(err)
	}
	workspaceOf := make(map[string]string, len(served))
	for _, s := range served {
		workspaceOf[s.Name] = s.Workspace
	}
	visible := id.Workspaces(access.RoleViewer)
	out := &directoryrosterv1.ListDirectoryGroupsResponse{
		Groups: make([]*directoryrosterv1.DirectoryGroupSummary, 0, len(groups)),
	}
	for _, g := range groups {
		if visible != nil && !slices.Contains(visible, workspaceOf[g.Domain]) {
			continue
		}
		out.Groups = append(out.Groups, &directoryrosterv1.DirectoryGroupSummary{
			Email:       g.Email,
			Domain:      g.Domain,
			WorkspaceId: workspaceOf[g.Domain],
			Members:     int32(len(g.Members)), //nolint:gosec // a membership count never overflows
		})
	}
	return connect.NewResponse(out), nil
}

// ListHolders implements the operator contract: who holds an internal
// group, or who reaches a client, right now.
func (c *Console) ListHolders(
	ctx context.Context, req *connect.Request[directoryrosterv1.ListHoldersRequest],
) (*connect.Response[directoryrosterv1.ListHoldersResponse], error) {
	caller, err := requireAnywhere(ctx, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	group, client := req.Msg.GetGroup(), req.Msg.GetClient()
	switch {
	case group == "" && client == "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("name an internal group or a client"))
	case group != "" && client != "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("name an internal group or a client, not both"))
	}

	// Every account is examined, because holding a group is a property of
	// the whole policy rather than of one table; the limit bounds what is
	// returned, not what is considered.
	people, total, err := c.deps.Hub.People(ctx,
		hub.PeopleQuery{Workspaces: caller.Workspaces(access.RoleViewer)}, maxExamined)
	if err != nil {
		return nil, rpcError(err)
	}
	holders := c.deps.Authorizer.HoldersOf(people, group, client)

	limit := int(req.Msg.GetLimit())
	if limit <= 0 || limit > len(holders) {
		limit = len(holders)
	}
	out := &directoryrosterv1.ListHoldersResponse{
		Examined: int32(len(people)), //nolint:gosec // a snapshot's account count never overflows
		// Truncated by either bound. It used to report only the limit, so
		// an installation past the examined cap got an answer that looked
		// complete and silently left holders out — the one shape of wrong
		// a consumer that removes access cannot tell from the right one.
		Truncated:    limit < len(holders) || total > len(people),
		Holders:      make([]*directoryrosterv1.Holder, 0, limit),
		PolicyDigest: c.deps.Authorizer.Policy().Digest(),
	}
	for i := range holders[:limit] {
		out.Holders = append(out.Holders, holderProto(&holders[i]))
	}
	return connect.NewResponse(out), nil
}

// SearchPeople implements the operator contract.
func (c *Console) SearchPeople(
	ctx context.Context, req *connect.Request[directoryrosterv1.SearchPeopleRequest],
) (*connect.Response[directoryrosterv1.SearchPeopleResponse], error) {
	id, err := requireAnywhere(ctx, access.RoleViewer)
	if err != nil {
		return nil, err
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	query := hub.PeopleQuery{
		Text:      req.Msg.GetQuery(),
		Workspace: req.Msg.GetWorkspaceId(),
		Domain:    req.Msg.GetDomain(),
		// Nil for an installation-wide role, so it stays every tenant.
		Workspaces: id.Workspaces(access.RoleViewer),
	}
	switch req.Msg.GetAccount() {
	case directoryrosterv1.AccountFilter_ACCOUNT_FILTER_LIVE:
		live := true
		query.Live = &live
	case directoryrosterv1.AccountFilter_ACCOUNT_FILTER_SUSPENDED:
		live := false
		query.Live = &live
	case directoryrosterv1.AccountFilter_ACCOUNT_FILTER_UNSPECIFIED:
	}
	// The links are read here, once, and handed to the hub with the rest
	// of the query: the column needs a login on every row, and the facet
	// has to narrow before the limit or "the first 200" would mean the
	// matches among the first 200 — which answers "nobody" while the
	// snapshot holds hundreds. It is one object read, not the GitHub
	// report: no organisation is asked anything.
	logins, known, linksErr := c.githubLogins(ctx)
	query.GitHubLogins = logins
	switch req.Msg.GetGithub() {
	case directoryrosterv1.LinkFilter_LINK_FILTER_LINKED:
		linked := true
		query.GitHubLinked = &linked
	case directoryrosterv1.LinkFilter_LINK_FILTER_NOT_LINKED:
		linked := false
		query.GitHubLinked = &linked
	case directoryrosterv1.LinkFilter_LINK_FILTER_UNSPECIFIED:
	}
	// Narrowing by something unknown is the one case that must fail
	// loudly. Unasked, an unreadable link store only empties a column,
	// and github_known says so; asked, it would silently answer "nobody
	// linked" or "everybody did".
	if query.GitHubLinked != nil && !known {
		if linksErr != nil {
			return nil, connect.NewError(connect.CodeUnavailable,
				fmt.Errorf("whether people linked a GitHub account cannot be read: %w", linksErr))
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("nobody can link a GitHub account in this deployment: there is nowhere links are kept"))
	}
	people, total, err := c.deps.Hub.People(ctx, query, limit)
	if err != nil {
		return nil, rpcError(err)
	}
	out := &directoryrosterv1.SearchPeopleResponse{
		Truncated:   total > len(people),
		Total:       int32(total), //nolint:gosec // a snapshot's account count never overflows
		People:      make([]*directoryrosterv1.PersonSummary, 0, len(people)),
		GithubKnown: known,
	}
	for i := range people {
		person := &people[i]
		out.People = append(out.People, &directoryrosterv1.PersonSummary{
			Email:       person.Email,
			GivenName:   person.GivenName,
			FamilyName:  person.FamilyName,
			WorkspaceId: person.Workspace,
			Live:        person.Live,
			GithubLogin: person.GitHubLogin,
		})
	}
	return connect.NewResponse(out), nil
}

// githubLogins indexes every address a link proves against the GitHub
// account that proves it, lowercased.
//
// Only a link that counts is in it — one GitHub still verifies an address
// for — so the login beside a person here is the same one their own page
// calls their account, and a lost or unverifiable link leaves them
// unlinked on both.
//
// The second value is whether the links are KNOWN. False is a deployment
// that keeps none, or a read that failed; the error is returned beside it
// so a caller that cannot go on can say which.
func (c *Console) githubLogins(ctx context.Context) (map[string]string, bool, error) {
	if c.deps.GitHubLinks == nil {
		return nil, false, nil
	}
	links, err := c.deps.GitHubLinks.List(ctx)
	if err != nil {
		return nil, false, err
	}
	out := make(map[string]string, len(links))
	for i := range links {
		if !links[i].Active() {
			continue
		}
		for _, email := range links[i].Emails {
			out[strings.ToLower(email)] = links[i].Login
		}
	}
	return out, true, nil
}

// GetDirectoryGroup implements the operator contract: one directory group,
// its members as the directory reports them, and the internal groups it
// feeds. The feeds are the memberships table read backwards.
func (c *Console) GetDirectoryGroup(
	ctx context.Context, req *connect.Request[directoryrosterv1.GetDirectoryGroupRequest],
) (*connect.Response[directoryrosterv1.GetDirectoryGroupResponse], error) {
	if _, err := requireRole(ctx, access.RoleViewer); err != nil {
		return nil, err
	}
	group, err := c.deps.Hub.DirectoryGroup(ctx, req.Msg.GetEmail())
	if err != nil {
		return nil, rpcError(err)
	}
	out := &directoryrosterv1.GetDirectoryGroupResponse{
		Email:         group.Email,
		Domain:        group.Domain,
		WorkspaceId:   group.Workspace,
		Found:         group.Found,
		Authoritative: group.Authoritative,
		Members:       make([]*directoryrosterv1.DirectoryGroupMember, 0, len(group.Members)),
	}
	if !group.SnapshotAt.IsZero() {
		out.SnapshotAt = timestamppb.New(group.SnapshotAt)
	}
	for i := range group.Members {
		m := &group.Members[i]
		out.Members = append(out.Members, &directoryrosterv1.DirectoryGroupMember{
			Email:      m.Email,
			GivenName:  m.GivenName,
			FamilyName: m.FamilyName,
			Known:      m.Known,
			Live:       m.Live,
		})
	}
	for _, view := range c.deps.Authorizer.Policy().Groups() {
		for _, member := range view.Members {
			if strings.EqualFold(member.Address, group.Email) {
				out.Feeds = append(out.Feeds, &directoryrosterv1.DirectoryGroupFeed{Group: view.Name})
			}
		}
	}
	return connect.NewResponse(out), nil
}
