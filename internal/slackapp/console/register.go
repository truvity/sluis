package console

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/proto"

	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/storage/logattr"
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
	RegisterLazy(s, nil, func(context.Context) (*Service, error) { return c, nil })
}

// RegisterLazy puts the console methods on the server over a service that open
// assembles on the first call of a container and returns again for the rest: a
// scheduled pass needs none of it. A service that cannot be opened is a call the
// transport answers unavailable, with no detail, and the next call tries again.
func RegisterLazy(s *modcall.Server, log *slog.Logger, open func(context.Context) (*Service, error)) {
	h := func(method string) *registrar { return &registrar{s: s, method: method, open: open, log: log} }
	handle(h(MethodBeginWorkspaceConnect), (*Service).BeginSlackWorkspaceConnect)
	handleJSON(h(MethodCompleteWorkspace), (*Service).CompleteWorkspace)
	handle(h(MethodDisconnectWorkspace), (*Service).DisconnectSlackWorkspace)
	handle(h(MethodChangeOwner), (*Service).ChangeSlackWorkspaceOwner)
	handle(h(MethodRequestPass), (*Service).RequestSlackPass)
	handle(h(MethodGetStatus), (*Service).GetSlackStatus, consolecall.List())
	handle(h(MethodConfirmRemovals), (*Service).ConfirmSlackRemovals)

	handle(h(MethodListApps), (*Service).ListSlackApps, consolecall.List())
	handle(h(MethodCreateApp), (*Service).CreateSlackApp)
	handle(h(MethodInstallApp), (*Service).InstallSlackApp)
	handleJSON(h(MethodCompleteCatalogue), (*Service).CompleteCatalogue)

	handle(h(MethodListChannels), (*Service).ListSlackChannels, consolecall.List())
	handle(h(MethodCreateChannel), (*Service).CreateSlackChannel)
	handle(h(MethodUpdateChannel), (*Service).UpdateSlackChannel)
	handle(h(MethodDeleteChannel), (*Service).DeleteSlackChannel)
	handle(h(MethodListSharedChannels), (*Service).ListSlackSharedChannels, consolecall.List())
	handle(h(MethodCreateSharedChannel), (*Service).CreateSlackSharedChannel)
	handle(h(MethodUpdateSharedChannel), (*Service).UpdateSlackSharedChannel)
	handle(h(MethodDeleteSharedChannel), (*Service).DeleteSlackSharedChannel)
}

// NewServer is the module's server with the console methods on it: what a
// deployment that has not split the module out calls in its own process.
func NewServer(c *Service) *modcall.Server {
	s := modcall.NewServer(Module)
	Register(s, c)
	return s
}

// registrar is one method's registration: where it goes and how its service is
// opened.
type registrar struct {
	s      *modcall.Server
	method string
	open   func(context.Context) (*Service, error)
	log    *slog.Logger
}

// service opens the module's service for a call, or says it is unavailable.
func (r *registrar) service(ctx context.Context) (*Service, error) {
	c, err := r.open(ctx)
	if err != nil {
		log := r.log
		if log == nil {
			log = slog.Default()
		}
		log.ErrorContext(ctx, "the console service could not be opened", slog.String("method", r.method), logattr.SafeError("error", err))
		return nil, modcall.Coded(modcall.CodeUnavailable, "")
	}
	return c, nil
}

func handle[Req, Resp proto.Message](
	r *registrar, fn func(*Service, context.Context, consolecall.Caller, Req) (Resp, error), opts ...modcall.Option,
) {
	consolecall.Handle(r.s, r.method, func(ctx context.Context, caller consolecall.Caller, req Req) (Resp, error) {
		var zero Resp
		c, err := r.service(ctx)
		if err != nil {
			return zero, err
		}
		resp, err := fn(c, ctx, caller, req)
		return resp, c.told(ctx, r.method, err)
	}, opts...)
}

func handleJSON[Req, Resp any](
	r *registrar, fn func(*Service, context.Context, consolecall.Caller, Req) (Resp, error), opts ...modcall.Option,
) {
	consolecall.HandleJSON(r.s, r.method, func(ctx context.Context, caller consolecall.Caller, req Req) (Resp, error) {
		var zero Resp
		c, err := r.service(ctx)
		if err != nil {
			return zero, err
		}
		resp, err := fn(c, ctx, caller, req)
		return resp, c.told(ctx, r.method, err)
	}, opts...)
}
