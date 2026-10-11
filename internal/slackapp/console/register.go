package console

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
)

// The console methods of the Slack module, as `slack.<method>`. Each answers the
// caller class `console` alone ([consolecall.Handle]).
const (
	MethodBeginWorkspaceConnect = "console.begin_workspace_connect"
	MethodCompleteWorkspace     = "console.complete_workspace"
	MethodDisconnectWorkspace   = "console.disconnect_workspace"
	MethodChangeOwner           = "console.change_owner"
	MethodRequestPass           = "console.request_pass"
	MethodGetStatus             = "console.get_status"
	MethodConfirmRemovals       = "console.confirm_removals"

	MethodListApps          = "console.list_apps"
	MethodCreateApp         = "console.create_app"
	MethodInstallApp        = "console.install_app"
	MethodCompleteCatalogue = "console.complete_catalogue"

	MethodListChannels        = "console.list_channels"
	MethodCreateChannel       = "console.create_channel"
	MethodUpdateChannel       = "console.update_channel"
	MethodDeleteChannel       = "console.delete_channel"
	MethodListSharedChannels  = "console.list_shared_channels"
	MethodCreateSharedChannel = "console.create_shared_channel"
	MethodUpdateSharedChannel = "console.update_shared_channel"
	MethodDeleteSharedChannel = "console.delete_shared_channel"
)

// Register puts the console methods on the module's server.
func Register(s *modcall.Server, c *Service) {
	handle(c, s, MethodBeginWorkspaceConnect, c.BeginSlackWorkspaceConnect)
	handleJSON(c, s, MethodCompleteWorkspace, c.CompleteWorkspace)
	handle(c, s, MethodDisconnectWorkspace, c.DisconnectSlackWorkspace)
	handle(c, s, MethodChangeOwner, c.ChangeSlackWorkspaceOwner)
	handle(c, s, MethodRequestPass, c.RequestSlackPass)
	handle(c, s, MethodGetStatus, c.GetSlackStatus, consolecall.List())
	handle(c, s, MethodConfirmRemovals, c.ConfirmSlackRemovals)

	handle(c, s, MethodListApps, c.ListSlackApps, consolecall.List())
	handle(c, s, MethodCreateApp, c.CreateSlackApp)
	handle(c, s, MethodInstallApp, c.InstallSlackApp)
	handleJSON(c, s, MethodCompleteCatalogue, c.CompleteCatalogue)

	handle(c, s, MethodListChannels, c.ListSlackChannels, consolecall.List())
	handle(c, s, MethodCreateChannel, c.CreateSlackChannel)
	handle(c, s, MethodUpdateChannel, c.UpdateSlackChannel)
	handle(c, s, MethodDeleteChannel, c.DeleteSlackChannel)
	handle(c, s, MethodListSharedChannels, c.ListSlackSharedChannels, consolecall.List())
	handle(c, s, MethodCreateSharedChannel, c.CreateSlackSharedChannel)
	handle(c, s, MethodUpdateSharedChannel, c.UpdateSlackSharedChannel)
	handle(c, s, MethodDeleteSharedChannel, c.DeleteSlackSharedChannel)
}

// NewServer is the module's server with the console methods on it: what a
// deployment that has not split the module out calls in its own process.
func NewServer(c *Service) *modcall.Server {
	s := modcall.NewServer(Module)
	Register(s, c)
	return s
}

func handle[Req, Resp proto.Message](
	c *Service, s *modcall.Server, method string, fn func(context.Context, consolecall.Caller, Req) (Resp, error), opts ...modcall.Option,
) {
	consolecall.Handle(s, method, func(ctx context.Context, caller consolecall.Caller, req Req) (Resp, error) {
		resp, err := fn(ctx, caller, req)
		return resp, c.told(ctx, method, err)
	}, opts...)
}

func handleJSON[Req, Resp any](
	c *Service, s *modcall.Server, method string, fn func(context.Context, consolecall.Caller, Req) (Resp, error), opts ...modcall.Option,
) {
	consolecall.HandleJSON(s, method, func(ctx context.Context, caller consolecall.Caller, req Req) (Resp, error) {
		resp, err := fn(ctx, caller, req)
		return resp, c.told(ctx, method, err)
	}, opts...)
}
