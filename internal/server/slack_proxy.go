package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/modcall"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
)

// What the console keeps of the Slack pages is the browser's: it authenticates
// the person, signs the state of an install and pins the browser to it, and
// renders the page a callback lands on. The rest runs in the Slack module
// (internal/slackapp/console), which the console calls as the caller class
// `console` with a snapshot of who the person is.

// The stores of the Slack module, as the console's deployment hands them over.
type (
	// SlackWorkspaces is where connected Slack workspaces are kept.
	SlackWorkspaces = slackconsole.Workspaces
	// SlackCatalogueApps is where catalogue Slack Apps are kept.
	SlackCatalogueApps = slackconsole.CatalogueApps
	// SlackChannelRecords is where console channels' records are kept.
	SlackChannelRecords = slackconsole.ChannelRecords
	// SlackSharedRecords is where Slack Connect channel definitions are kept.
	SlackSharedRecords = slackconsole.SharedRecords
	// SlackStatusReports is what the Slack controller last reported.
	SlackStatusReports = slackconsole.StatusReports
)

const (
	// slackCallTimeout bounds one call to the module: a page is one call, and a
	// cold start of the module must not hold the console.
	slackCallTimeout = 30 * time.Second
	// slackCompleteTimeout bounds a callback's call. It is never repeated, since
	// Slack's code is single use, so it is long enough for the module's own
	// exchange with Slack.
	slackCompleteTimeout = 25 * time.Second
)

// UseSlack connects the Slack pages to the module in another process, called as
// the caller class `console`. Without it they call the module in this one.
func (s *ConsoleServer) UseSlack(m modcall.Caller) { s.console.deps.Modules.Slack = m }

// slackDeps is what the module in this process is built on: the console's own
// stores, read afresh at each call.
func (c *Console) slackDeps() slackconsole.Deps {
	d := slackconsole.Deps{
		Log: c.deps.Log, SlackCatalogue: c.deps.SlackCatalogue, SlackCatalogueApps: c.deps.SlackCatalogueApps,
		SlackShared: c.deps.SlackShared, SlackChannels: c.deps.SlackChannels, SlackStatus: c.deps.SlackStatus,
		SlackWorkspaces: c.deps.SlackWorkspaces, SlackAPI: c.deps.SlackAPI, Audit: c.deps.Audit, Trigger: c.deps.Trigger,
	}
	if c.deps.Authorizer != nil {
		d.Authorizer = c.deps.Authorizer
	}
	if c.deps.Hub != nil {
		d.Directory = hubDirectory{c.deps.Hub}
	}
	return d
}

// slackSvc is the module's console service over this process's stores, read
// afresh at each call.
func (c *Console) slackSvc() *slackconsole.Service {
	c.slackOnce.Do(func() {
		c.slack = slackconsole.NewService(c.slackDeps)
		c.slackLocal = modcall.Local{slackconsole.Module: slackconsole.NewServer(c.slack)}.As(consolecall.Class)
	})
	return c.slack
}

// slackModule is how the console calls the module: the one in another process
// when [ConsoleServer.UseSlack] named it, else the one in this.
func (c *Console) slackModule() modcall.Caller {
	if m := c.deps.Modules.Slack; m != nil {
		return m
	}
	c.slackSvc()
	return c.slackLocal
}

// hubDirectory is the connected directories as the Slack module asks of them,
// answered from the hub.
type hubDirectory struct{ hub *hub.Hub }

var _ slackconsole.Directory = hubDirectory{}

func (d hubDirectory) Directories(ctx context.Context) ([]consoleauthz.Directory, error) {
	dirs, err := (&Console{deps: ConsoleDeps{Hub: d.hub}}).directories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]consoleauthz.Directory, 0, len(dirs))
	for i := range dirs {
		out = append(out, consoleauthz.Directory{ID: dirs[i].id, Primary: dirs[i].primary, Served: dirs[i].served, Authoritative: dirs[i].authoritative})
	}
	return out, nil
}

func (d hubDirectory) Group(ctx context.Context, address string) (slackconsole.Group, error) {
	g, err := d.hub.DirectoryGroup(ctx, address)
	return slackconsole.Group{Found: g.Found, Workspace: g.Workspace}, err
}

func (d hubDirectory) User(ctx context.Context, address string) (slackconsole.User, error) {
	u, err := d.hub.ResolveUser(ctx, address, nil)
	return slackconsole.User{Found: u.Found, Suspended: u.Suspended, Workspace: u.Workspace}, err
}

func (d hubDirectory) Groups(ctx context.Context) ([]slackconsole.GroupEntry, []slackconsole.Domain, error) {
	groups, served, err := d.hub.ListGroups(ctx, "", nil)
	if err != nil {
		return nil, nil, err
	}
	g := make([]slackconsole.GroupEntry, 0, len(groups))
	for _, group := range groups {
		g = append(g, slackconsole.GroupEntry{Email: group.Email, Domain: group.Domain, Members: len(group.Members)})
	}
	dom := make([]slackconsole.Domain, 0, len(served))
	for _, s := range served {
		dom = append(dom, slackconsole.Domain{Name: s.Name, Workspace: s.Workspace, Authoritative: s.Authoritative})
	}
	return g, dom, nil
}

// slackCaller is who the person is to the module: the snapshot of the identity
// the console authorized, and where the console is.
func (c *Console) slackCaller(id access.Identity) consolecall.Caller {
	caller := consolecall.Snapshot(id)
	caller.Origin = c.githubRoot()
	return caller
}

// slackIdentity is the signed-in person, or the refusal. The roles are the
// module's to ask, of its own records.
func slackIdentity(ctx context.Context) (access.Identity, error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return access.Identity{}, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
	}
	return id, nil
}

// slackProxy calls one console method of the Slack module as the person.
func slackProxy[Req, Resp proto.Message](ctx context.Context, c *Console, id access.Identity, method string, req Req) (Resp, error) {
	ctx, cancel := context.WithTimeout(ctx, slackCallTimeout)
	defer cancel()
	resp, err := consolecall.Proxy[Req, Resp](ctx, c.slackModule(), slackconsole.Module, method, c.slackCaller(id), req)
	return resp, slackError(err)
}

// slackError is what the module answered as the Connect error the page shows.
func slackError(err error) error {
	var problem *consolecall.Problem
	var coded *modcall.Error
	var already *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &already):
		return err
	case errors.As(err, &problem):
		return connect.NewError(slackconsole.CodeOf(problem), errors.New(problem.Summary))
	case errors.As(err, &coded):
		switch coded.Code {
		case modcall.CodeForbidden:
			return connect.NewError(connect.CodePermissionDenied, errors.New(coded.Message))
		case consoleauthz.CodeUnauthenticated:
			return connect.NewError(connect.CodeUnauthenticated, errors.New(coded.Message))
		case modcall.CodeBadRequest:
			return connect.NewError(connect.CodeInvalidArgument, errors.New(coded.Message))
		case modcall.CodeInternal:
			return connect.NewError(connect.CodeInternal, errors.New("the Slack module failed; see its log"))
		}
	}
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("the Slack module did not answer: %w", modcallCause(err)))
}

// modcallCause is the transport's own words for an error, which carry a code and
// no detail from the module.
func modcallCause(err error) error {
	var coded *modcall.Error
	if errors.As(err, &coded) {
		return errors.New(coded.Code)
	}
	return err
}

// withState adds the signed state to the authorize URL the module returned.
func withState(authorize, state string) (string, error) {
	u, err := url.Parse(authorize)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ---------------------------------------------------------------- SlackService

// GetSlackStatus is every Slack workspace the caller may view.
func (c *Console) GetSlackStatus(
	ctx context.Context, req *connect.Request[directoryrosterv1.GetSlackStatusRequest],
) (*connect.Response[directoryrosterv1.GetSlackStatusResponse], error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.GetSlackStatusRequest, *directoryrosterv1.GetSlackStatusResponse](
		ctx, c, id, slackconsole.MethodGetStatus, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// BeginSlackWorkspaceConnect creates the roster's App in a workspace, or
// prepares the reinstall of one created earlier, and returns Slack's authorize
// URL carrying signed state; the header pins the flow to this browser.
func (c *Console) BeginSlackWorkspaceConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginSlackWorkspaceConnectRequest],
) (*connect.Response[directoryrosterv1.BeginSlackWorkspaceConnectResponse], error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.BeginSlackWorkspaceConnectRequest, *directoryrosterv1.BeginSlackWorkspaceConnectResponse](
		ctx, c, id, slackconsole.MethodBeginWorkspaceConnect, req.Msg)
	if err != nil {
		return nil, err
	}
	state, err := c.deps.State.IssueAs(access.Binding{Bind: slackconsole.WorkspaceBind + strings.TrimSpace(req.Msg.GetWorkspace()), Actor: id.Who()})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	target, err := withState(resp.GetUrl(), state)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := connect.NewResponse(&directoryrosterv1.BeginSlackWorkspaceConnectResponse{Url: target})
	c.pinFlow(out.Header(), state)
	return out, nil
}

// RequestSlackPass asks the Slack controller to pass over a workspace now.
func (c *Console) RequestSlackPass(
	ctx context.Context, req *connect.Request[directoryrosterv1.RequestSlackPassRequest],
) (*connect.Response[directoryrosterv1.RequestSlackPassResponse], error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.RequestSlackPassRequest, *directoryrosterv1.RequestSlackPassResponse](
		ctx, c, id, slackconsole.MethodRequestPass, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// DisconnectSlackWorkspace revokes the bot token and forgets the connection.
func (c *Console) DisconnectSlackWorkspace(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectSlackWorkspaceRequest],
) (*connect.Response[directoryrosterv1.DisconnectSlackWorkspaceResponse], error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.DisconnectSlackWorkspaceRequest, *directoryrosterv1.DisconnectSlackWorkspaceResponse](
		ctx, c, id, slackconsole.MethodDisconnectWorkspace, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// ChangeSlackWorkspaceOwner changes the directory recorded as a connected
// workspace's owner, or removes it.
func (c *Console) ChangeSlackWorkspaceOwner(
	ctx context.Context, req *connect.Request[directoryrosterv1.ChangeSlackWorkspaceOwnerRequest],
) (*connect.Response[directoryrosterv1.ChangeSlackWorkspaceOwnerResponse], error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.ChangeSlackWorkspaceOwnerRequest, *directoryrosterv1.ChangeSlackWorkspaceOwnerResponse](
		ctx, c, id, slackconsole.MethodChangeOwner, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// ConfirmSlackRemovals lets exactly the removal set the operator saw go ahead.
func (c *Console) ConfirmSlackRemovals(
	ctx context.Context, req *connect.Request[directoryrosterv1.ConfirmSlackRemovalsRequest],
) (*connect.Response[directoryrosterv1.ConfirmSlackRemovalsResponse], error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.ConfirmSlackRemovalsRequest, *directoryrosterv1.ConfirmSlackRemovalsResponse](
		ctx, c, id, slackconsole.MethodConfirmRemovals, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// ---------------------------------------------------------- SlackAppService

// ListSlackApps is every declared App, and every App created from an entry no
// longer declared, with where each stands.
func (c *Console) ListSlackApps(
	ctx context.Context, req *connect.Request[directoryrosterv1.ListSlackAppsRequest],
) (*connect.Response[directoryrosterv1.ListSlackAppsResponse], error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.ListSlackAppsRequest, *directoryrosterv1.ListSlackAppsResponse](
		ctx, c, id, slackconsole.MethodListApps, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// CreateSlackApp creates a catalogue App in Slack.
func (c *Console) CreateSlackApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.CreateSlackAppRequest],
) (*connect.Response[directoryrosterv1.CreateSlackAppResponse], error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.CreateSlackAppRequest, *directoryrosterv1.CreateSlackAppResponse](
		ctx, c, id, slackconsole.MethodCreateApp, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// InstallSlackApp starts installing, or reinstalling, a created App: the
// response is Slack's authorize URL carrying signed state, and the header pins
// the flow to this browser.
func (c *Console) InstallSlackApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.InstallSlackAppRequest],
) (*connect.Response[directoryrosterv1.InstallSlackAppResponse], error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.InstallSlackAppRequest, *directoryrosterv1.InstallSlackAppResponse](
		ctx, c, id, slackconsole.MethodInstallApp, req.Msg)
	if err != nil {
		return nil, err
	}
	state, err := c.deps.State.IssueAs(access.Binding{Bind: slackconsole.CatalogueBind + strings.TrimSpace(req.Msg.GetId()), Actor: id.Who()})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	target, err := withState(resp.GetUrl(), state)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := connect.NewResponse(&directoryrosterv1.InstallSlackAppResponse{Url: target})
	c.pinFlow(out.Header(), state)
	return out, nil
}
