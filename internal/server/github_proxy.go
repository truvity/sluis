package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/modcall"
)

// The console's GitHub operations are the GitHub module's (docs/decisions/0071,
// v1.76 D-v176-1 b). What stays here is the console's part: authenticate the
// person, check the coarse role, take the snapshot of who they are, call the
// module as the class `console` and tell the answer in the contract's own
// words. The module authorizes again against its own records.

// githubCallTimeout bounds an ordinary call to the module.
const githubCallTimeout = 30 * time.Second

// githubCaller is the snapshot of the signed-in person, once the coarse role
// has passed.
func (c *Console) githubCaller(ctx context.Context, want access.Role) (consolecall.Caller, access.Identity, error) {
	id, err := requireAnywhere(ctx, want)
	if err != nil {
		return consolecall.Caller{}, id, err
	}
	return c.snapshot(ctx, id)
}

// snapshot is the caller of id with the connected directories the console
// knows, which the module chooses an owner among and may not read itself.
func (c *Console) snapshot(ctx context.Context, id access.Identity) (consolecall.Caller, access.Identity, error) {
	caller := consolecall.Snapshot(id)
	dirs, err := c.directories(ctx)
	if err != nil {
		return consolecall.Caller{}, id, err
	}
	caller.Directories = dirs
	return caller, id, nil
}

// githubError is a failure of a call to the module as the contract tells it. A
// refusal the module told keeps its code and words; the hop's own failures are
// fixed words, with the reason in the log under the request's id (the refusal
// interceptor masks an Internal error and logs what it wrote).
func githubError(err error) error {
	var p *consolecall.Problem
	if errors.As(err, &p) {
		code := connect.CodeFailedPrecondition
		if p.Code != "" {
			if parsed := new(connect.Code); parsed.UnmarshalText([]byte(p.Code)) == nil {
				code = *parsed
			}
		}
		return connect.NewError(code, errors.New(p.Summary))
	}
	var e *modcall.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &e) && e.Code == modcall.CodeDeadlineExceeded:
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("the GitHub service did not answer in time"))
	case errors.As(err, &e) && e.Code == modcall.CodeForbidden:
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("the GitHub service refused the console: %w", err))
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("the GitHub service: %w", err))
}

// githubProto is a console method that is the module's, over the contract's own
// messages.
func githubProto[Req, Resp any, PReq interface {
	*Req
	proto.Message
}, PResp interface {
	*Resp
	proto.Message
}](
	ctx context.Context, c *Console, method string, want access.Role, req *connect.Request[Req],
) (*connect.Response[Resp], error) {
	caller, _, err := c.githubCaller(ctx, want)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	out, err := consolecall.Proxy[PReq, PResp](ctx, c.deps.Modules.GitHub, consolewire.Module, method, caller, PReq(req.Msg))
	if err != nil {
		return nil, githubError(err)
	}
	return connect.NewResponse((*Resp)(out)), nil
}

// BeginGitHubConnect starts connecting an organisation: the module decides who
// may and what is made, and this signs the state that pins the flow to this
// browser, which the module never holds the key for.
func (c *Console) BeginGitHubConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubConnectResponse], error) {
	_, id, err := c.githubCaller(ctx, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	begun, err := c.beginOrganisationConnect(ctx, id, req.Msg.GetOrg(), req.Msg.GetOwnerDirectory())
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

// beginOrganisationConnect asks the module to begin an organisation's connect
// and signs the state it names, as id.
func (c *Console) beginOrganisationConnect(ctx context.Context, id access.Identity, org, owner string) (githubBegin, error) {
	ctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	caller, _, err := c.snapshot(ctx, id)
	if err != nil {
		return githubBegin{}, err
	}
	begun, err := consolecall.ProxyJSON[consolewire.BeginConnectRequest, consolewire.Begun](ctx, c.deps.Modules.GitHub,
		consolewire.Module, consolewire.BeginConnect, caller,
		consolewire.BeginConnectRequest{Org: org, OwnerDirectory: owner})
	if err != nil {
		return githubBegin{}, githubError(err)
	}
	return c.signBegun(id, begun)
}

// signBegun signs the state a module's begin names and puts it in the URL.
func (c *Console) signBegun(id access.Identity, begun consolewire.Begun) (githubBegin, error) {
	state, err := c.deps.State.IssueAs(access.Binding{Bind: begun.Bind, Actor: id.Who(), Owner: begun.Owner})
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err)
	}
	return githubBegin{url: withState(begun.URL, state), manifest: begun.Manifest, state: state}, nil
}

// DisconnectGitHubOrganisation revokes, then forgets: in the module.
func (c *Console) DisconnectGitHubOrganisation(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubOrganisationRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubOrganisationResponse], error) {
	return githubProto[directoryrosterv1.DisconnectGitHubOrganisationRequest, directoryrosterv1.DisconnectGitHubOrganisationResponse](ctx, c, consolewire.DisconnectOrganisation, access.RoleOperator, req)
}

// disconnectOrganisation is the work of it, which [Console.DisconnectGitHubApp]
// does too.
func (c *Console) disconnectOrganisation(ctx context.Context, org string) (githubDisconnect, error) {
	resp, err := c.DisconnectGitHubOrganisation(ctx, connect.NewRequest(&directoryrosterv1.DisconnectGitHubOrganisationRequest{Org: org}))
	if err != nil {
		return githubDisconnect{}, err
	}
	return githubDisconnect{
		uninstalled: resp.Msg.GetUninstalled(), detail: resp.Msg.GetDetail(), settingsURL: resp.Msg.GetAppSettingsUrl(),
	}, nil
}

// ChangeGitHubOrganisationOwner changes the directory recorded as a connected
// organisation's owner, or removes it: in the module.
func (c *Console) ChangeGitHubOrganisationOwner(
	ctx context.Context, req *connect.Request[directoryrosterv1.ChangeGitHubOrganisationOwnerRequest],
) (*connect.Response[directoryrosterv1.ChangeGitHubOrganisationOwnerResponse], error) {
	return githubProto[directoryrosterv1.ChangeGitHubOrganisationOwnerRequest, directoryrosterv1.ChangeGitHubOrganisationOwnerResponse](ctx, c, consolewire.ChangeOrganisationOwner, access.RoleOperator, req)
}

// RequestGitHubPass asks the GitHub controller to pass over now: in the module,
// which records the request and ticks itself.
func (c *Console) RequestGitHubPass(
	ctx context.Context, req *connect.Request[directoryrosterv1.RequestGitHubPassRequest],
) (*connect.Response[directoryrosterv1.RequestGitHubPassResponse], error) {
	return githubProto[directoryrosterv1.RequestGitHubPassRequest, directoryrosterv1.RequestGitHubPassResponse](ctx, c, consolewire.RequestPass, access.RoleOperator, req)
}

// UseGitHub connects the console's GitHub operations to the module in another
// process, with the same timing as [ConsoleServer.UseCloudflare]. Without it
// the module is served in this process.
func (s *ConsoleServer) UseGitHub(c modcall.Caller) {
	s.console.deps.Modules.GitHub = c
}
