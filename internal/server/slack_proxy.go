package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/rpcmount"
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

// slackFacts is what the console resolves of the connected directories for one
// Slack call, from the hub it holds: the module reads no directory of its own.
// Only what the method asks is resolved, and each part is bounded. Nil is a
// deployment that reads no directory.
func (c *Console) slackFacts(ctx context.Context, id access.Identity, method string, req proto.Message) *consolecall.DirectoryFacts {
	if c.deps.Hub == nil {
		return nil
	}
	facts := &consolecall.DirectoryFacts{}
	switch method {
	case slackconsole.MethodBeginWorkspaceConnect, slackconsole.MethodGetStatus, slackconsole.MethodChangeOwner:
		c.directoriesInto(ctx, facts)
	case slackconsole.MethodListChannels, slackconsole.MethodListSharedChannels:
		// The picker is for whoever may define a channel.
		if id.CanAnywhere(access.RoleOperator) {
			c.slackSourcesInto(ctx, facts)
		}
	case slackconsole.MethodCreateChannel, slackconsole.MethodUpdateChannel,
		slackconsole.MethodCreateSharedChannel, slackconsole.MethodUpdateSharedChannel:
		c.directoriesInto(ctx, facts)
		c.slackAddressesInto(ctx, facts, slackAddresses(req))
	default:
		return nil
	}
	return facts
}

// userFact is what the module may know of a person's address.
func userFact(user hub.UserResult) consolecall.UserFact {
	return consolecall.UserFact{
		InDomain: user.InDomain, Authoritative: user.Authoritative, Found: user.Found, Suspended: user.Suspended, Workspace: user.Workspace,
	}
}

// directoriesInto adds the connected directories, as an owner is chosen among them.
func (c *Console) directoriesInto(ctx context.Context, facts *consolecall.DirectoryFacts) {
	dirs, err := c.directories(ctx)
	if err != nil {
		return
	}
	for i := range dirs {
		if len(facts.Directories) == consolecall.MaxDirectories {
			break
		}
		facts.Directories = append(facts.Directories, consolecall.Directory{
			ID: dirs[i].id, Primary: dirs[i].primary, Served: dirs[i].served, Authoritative: dirs[i].authoritative,
		})
	}
}

// slackSourcesInto adds the directories and their groups as a picker lists them.
func (c *Console) slackSourcesInto(ctx context.Context, facts *consolecall.DirectoryFacts) {
	c.directoriesInto(ctx, facts)
	groups, served, err := c.deps.Hub.ListGroups(ctx, "", nil)
	if err != nil {
		facts.SourcesError = "the directories' groups could not be read just now: reload to try again"
		return
	}
	byDomain := map[string]string{}
	domains := map[string][]string{}
	for _, s := range served {
		byDomain[s.Name] = s.Workspace
		if s.Authoritative {
			domains[s.Workspace] = append(domains[s.Workspace], s.Name)
		}
	}
	known := map[string]bool{}
	for i := range facts.Directories {
		known[facts.Directories[i].ID] = true
	}
	by := map[string]*consolecall.SourceDirectory{}
	total := 0
	for _, g := range groups {
		ws := byDomain[g.Domain]
		if ws == "" || !known[ws] {
			continue
		}
		if total == consolecall.MaxSourceGroups {
			facts.SourcesError = "there are more groups than a picker lists: type the address of the one you want"
			break
		}
		dir := by[ws]
		if dir == nil {
			facts.Sources = append(facts.Sources, consolecall.SourceDirectory{Workspace: ws, Domains: slices.Sorted(slices.Values(domains[ws]))})
			dir = &facts.Sources[len(facts.Sources)-1]
			by[ws] = dir
		}
		dir.Groups = append(dir.Groups, consolecall.SourceGroup{Email: g.Email, Members: len(g.Members)})
		total++
	}
}

// slackAddressesInto resolves each address of the call as a group and as a user.
func (c *Console) slackAddressesInto(ctx context.Context, facts *consolecall.DirectoryFacts, addresses []string) {
	facts.Groups, facts.Users = map[string]consolecall.GroupFact{}, map[string]consolecall.UserFact{}
	for _, address := range addresses {
		if len(facts.Groups) >= consolecall.MaxAddressFacts {
			return
		}
		group, err := c.deps.Hub.DirectoryGroup(ctx, address)
		if err != nil {
			facts.Groups[address] = consolecall.GroupFact{Err: err.Error()}
			continue
		}
		facts.Groups[address] = consolecall.GroupFact{Found: group.Found, Workspace: group.Workspace}
		user, err := c.deps.Hub.ResolveUser(ctx, address, nil)
		if err != nil {
			facts.Users[address] = consolecall.UserFact{Err: err.Error()}
			continue
		}
		facts.Users[address] = userFact(user)
	}
}

// slackAddresses are the sources and individual addresses a channel request
// names, as the module reads them: trimmed and lowercased.
func slackAddresses(req proto.Message) []string {
	var raw []string
	switch r := req.(type) {
	case *directoryrosterv1.CreateSlackChannelRequest:
		raw = slices.Concat(r.GetChannel().GetSources(), r.GetChannel().GetMembers())
	case *directoryrosterv1.UpdateSlackChannelRequest:
		raw = slices.Concat(r.GetChannel().GetSources(), r.GetChannel().GetMembers())
	case *directoryrosterv1.CreateSlackSharedChannelRequest:
		raw = slices.Concat(r.GetChannel().GetFrom(), r.GetChannel().GetMembers())
	case *directoryrosterv1.UpdateSlackSharedChannelRequest:
		raw = slices.Concat(r.GetChannel().GetFrom(), r.GetChannel().GetMembers())
	}
	var out []string
	for _, a := range raw {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
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
		return access.Identity{}, connect.NewError(connect.CodeUnauthenticated, "sign in first")
	}
	return id, nil
}

// slackProxy calls one console method of the Slack module as the person.
func slackProxy[Req, Resp proto.Message](ctx context.Context, c *Console, id access.Identity, method string, req Req) (Resp, error) {
	ctx, cancel := context.WithTimeout(consolecall.WithFacts(ctx, c.slackFacts(ctx, id, method, req)), slackCallTimeout)
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
		return connect.NewError(slackconsole.CodeOf(problem), problem.Summary)
	case errors.As(err, &coded):
		switch coded.Code {
		case modcall.CodeForbidden:
			return connect.NewError(connect.CodePermissionDenied, coded.Message)
		case consoleauthz.CodeUnauthenticated:
			return connect.NewError(connect.CodeUnauthenticated, coded.Message)
		case modcall.CodeBadRequest:
			return connect.NewError(connect.CodeInvalidArgument, coded.Message)
		case modcall.CodeInternal:
			return connect.NewError(connect.CodeInternal, "the Slack module failed; see its log")
		}
	}
	return rpcmount.NewError(connect.CodeUnavailable, fmt.Errorf("the Slack module did not answer: %w", modcallCause(err)))
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
	ctx context.Context, req *directoryrosterv1.GetSlackStatusRequest,
) (*directoryrosterv1.GetSlackStatusResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.GetSlackStatusRequest, *directoryrosterv1.GetSlackStatusResponse](
		ctx, c, id, slackconsole.MethodGetStatus, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// BeginSlackWorkspaceConnect creates the roster's App in a workspace, or
// prepares the reinstall of one created earlier, and returns Slack's authorize
// URL carrying signed state; the header pins the flow to this browser.
func (c *Console) BeginSlackWorkspaceConnect(
	ctx context.Context, req *directoryrosterv1.BeginSlackWorkspaceConnectRequest,
) (*directoryrosterv1.BeginSlackWorkspaceConnectResponse, error) {
	info := rpcmount.CallInfo(ctx)
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.BeginSlackWorkspaceConnectRequest, *directoryrosterv1.BeginSlackWorkspaceConnectResponse](
		ctx, c, id, slackconsole.MethodBeginWorkspaceConnect, req)
	if err != nil {
		return nil, err
	}
	state, err := c.deps.State.IssueAs(access.Binding{Bind: slackconsole.WorkspaceBind + strings.TrimSpace(req.GetWorkspace()), Actor: id.Who()})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	target, err := withState(resp.GetUrl(), state)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	out := &directoryrosterv1.BeginSlackWorkspaceConnectResponse{Url: target}
	c.pinFlow(info.ResponseHeader(), state)
	return out, nil
}

// RequestSlackPass asks the Slack controller to pass over a workspace now.
func (c *Console) RequestSlackPass(
	ctx context.Context, req *directoryrosterv1.RequestSlackPassRequest,
) (*directoryrosterv1.RequestSlackPassResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.RequestSlackPassRequest, *directoryrosterv1.RequestSlackPassResponse](
		ctx, c, id, slackconsole.MethodRequestPass, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// DisconnectSlackWorkspace revokes the bot token and forgets the connection.
func (c *Console) DisconnectSlackWorkspace(
	ctx context.Context, req *directoryrosterv1.DisconnectSlackWorkspaceRequest,
) (*directoryrosterv1.DisconnectSlackWorkspaceResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.DisconnectSlackWorkspaceRequest, *directoryrosterv1.DisconnectSlackWorkspaceResponse](
		ctx, c, id, slackconsole.MethodDisconnectWorkspace, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ChangeSlackWorkspaceOwner changes the directory recorded as a connected
// workspace's owner, or removes it.
func (c *Console) ChangeSlackWorkspaceOwner(
	ctx context.Context, req *directoryrosterv1.ChangeSlackWorkspaceOwnerRequest,
) (*directoryrosterv1.ChangeSlackWorkspaceOwnerResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.ChangeSlackWorkspaceOwnerRequest, *directoryrosterv1.ChangeSlackWorkspaceOwnerResponse](
		ctx, c, id, slackconsole.MethodChangeOwner, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ConfirmSlackRemovals lets exactly the removal set the operator saw go ahead.
func (c *Console) ConfirmSlackRemovals(
	ctx context.Context, req *directoryrosterv1.ConfirmSlackRemovalsRequest,
) (*directoryrosterv1.ConfirmSlackRemovalsResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.ConfirmSlackRemovalsRequest, *directoryrosterv1.ConfirmSlackRemovalsResponse](
		ctx, c, id, slackconsole.MethodConfirmRemovals, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ---------------------------------------------------------- SlackAppService

// ListSlackApps is every declared App, and every App created from an entry no
// longer declared, with where each stands.
func (c *Console) ListSlackApps(
	ctx context.Context, req *directoryrosterv1.ListSlackAppsRequest,
) (*directoryrosterv1.ListSlackAppsResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.ListSlackAppsRequest, *directoryrosterv1.ListSlackAppsResponse](
		ctx, c, id, slackconsole.MethodListApps, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// CreateSlackApp creates a catalogue App in Slack.
func (c *Console) CreateSlackApp(
	ctx context.Context, req *directoryrosterv1.CreateSlackAppRequest,
) (*directoryrosterv1.CreateSlackAppResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.CreateSlackAppRequest, *directoryrosterv1.CreateSlackAppResponse](
		ctx, c, id, slackconsole.MethodCreateApp, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// InstallSlackApp starts installing, or reinstalling, a created App: the
// response is Slack's authorize URL carrying signed state, and the header pins
// the flow to this browser.
func (c *Console) InstallSlackApp(
	ctx context.Context, req *directoryrosterv1.InstallSlackAppRequest,
) (*directoryrosterv1.InstallSlackAppResponse, error) {
	info := rpcmount.CallInfo(ctx)
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.InstallSlackAppRequest, *directoryrosterv1.InstallSlackAppResponse](
		ctx, c, id, slackconsole.MethodInstallApp, req)
	if err != nil {
		return nil, err
	}
	state, err := c.deps.State.IssueAs(access.Binding{Bind: slackconsole.CatalogueBind + strings.TrimSpace(req.GetId()), Actor: id.Who()})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	target, err := withState(resp.GetUrl(), state)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	out := &directoryrosterv1.InstallSlackAppResponse{Url: target}
	c.pinFlow(info.ResponseHeader(), state)
	return out, nil
}

// ------------------------------------------- SlackChannelService, SlackSharedChannelService

// ListSlackChannels is every record the caller may see, the channels the bots see that nothing manages, and the choices a form offers.
func (c *Console) ListSlackChannels(
	ctx context.Context, req *directoryrosterv1.ListSlackChannelsRequest,
) (*directoryrosterv1.ListSlackChannelsResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.ListSlackChannelsRequest, *directoryrosterv1.ListSlackChannelsResponse](
		ctx, c, id, slackconsole.MethodListChannels, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// CreateSlackChannel puts a channel under management.
func (c *Console) CreateSlackChannel(
	ctx context.Context, req *directoryrosterv1.CreateSlackChannelRequest,
) (*directoryrosterv1.CreateSlackChannelResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.CreateSlackChannelRequest, *directoryrosterv1.CreateSlackChannelResponse](
		ctx, c, id, slackconsole.MethodCreateChannel, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// UpdateSlackChannel changes a console channel's mode, ignore list and sources.
func (c *Console) UpdateSlackChannel(
	ctx context.Context, req *directoryrosterv1.UpdateSlackChannelRequest,
) (*directoryrosterv1.UpdateSlackChannelResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.UpdateSlackChannelRequest, *directoryrosterv1.UpdateSlackChannelResponse](
		ctx, c, id, slackconsole.MethodUpdateChannel, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// DeleteSlackChannel forgets a console channel's record, and archives the channel when asked.
func (c *Console) DeleteSlackChannel(
	ctx context.Context, req *directoryrosterv1.DeleteSlackChannelRequest,
) (*directoryrosterv1.DeleteSlackChannelResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.DeleteSlackChannelRequest, *directoryrosterv1.DeleteSlackChannelResponse](
		ctx, c, id, slackconsole.MethodDeleteChannel, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ListSlackSharedChannels is every Slack Connect record the caller may see, with where the controller has each.
func (c *Console) ListSlackSharedChannels(
	ctx context.Context, req *directoryrosterv1.ListSlackSharedChannelsRequest,
) (*directoryrosterv1.ListSlackSharedChannelsResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.ListSlackSharedChannelsRequest, *directoryrosterv1.ListSlackSharedChannelsResponse](
		ctx, c, id, slackconsole.MethodListSharedChannels, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// CreateSlackSharedChannel defines a Slack Connect channel.
func (c *Console) CreateSlackSharedChannel(
	ctx context.Context, req *directoryrosterv1.CreateSlackSharedChannelRequest,
) (*directoryrosterv1.CreateSlackSharedChannelResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.CreateSlackSharedChannelRequest, *directoryrosterv1.CreateSlackSharedChannelResponse](
		ctx, c, id, slackconsole.MethodCreateSharedChannel, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// UpdateSlackSharedChannel changes a Slack Connect channel's guests, sources and visibility.
func (c *Console) UpdateSlackSharedChannel(
	ctx context.Context, req *directoryrosterv1.UpdateSlackSharedChannelRequest,
) (*directoryrosterv1.UpdateSlackSharedChannelResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.UpdateSlackSharedChannelRequest, *directoryrosterv1.UpdateSlackSharedChannelResponse](
		ctx, c, id, slackconsole.MethodUpdateSharedChannel, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// DeleteSlackSharedChannel forgets a Slack Connect channel's record.
func (c *Console) DeleteSlackSharedChannel(
	ctx context.Context, req *directoryrosterv1.DeleteSlackSharedChannelRequest,
) (*directoryrosterv1.DeleteSlackSharedChannelResponse, error) {
	id, err := slackIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := slackProxy[*directoryrosterv1.DeleteSlackSharedChannelRequest, *directoryrosterv1.DeleteSlackSharedChannelResponse](
		ctx, c, id, slackconsole.MethodDeleteSharedChannel, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}
