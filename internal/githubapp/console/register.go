package console

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/modcall"
)

// FlowDeadline bounds a call that finishes a provider's flow. The code the
// provider returned is single use, so the call is never retried and is given
// longer than an ordinary one: a lost answer after the exchange loses the key.
const FlowDeadline = 25 * time.Second

// Register makes the module answer the console's methods on s, to the console
// class alone ([consolecall.Handle] allows no other).
func Register(s *modcall.Server, svc *Service) {
	consolecall.HandleJSON(s, consolewire.BeginConnect, adapt(svc, func(ctx context.Context, r consolewire.BeginConnectRequest) (consolewire.Begun, error) {
		return svc.BeginGitHubConnect(ctx, r.Org, r.OwnerDirectory)
	}))
	consolecall.HandleJSON(s, consolewire.CompleteCreate, adapt(svc, svc.CompleteGitHubCreate))
	consolecall.HandleJSON(s, consolewire.CompleteInstall, adapt(svc, svc.CompleteGitHubInstall))
	handleProto(svc, s, consolewire.DisconnectOrganisation, svc.DisconnectGitHubOrganisation)
	handleProto(svc, s, consolewire.ChangeOrganisationOwner, svc.ChangeGitHubOrganisationOwner)
	handleProto(svc, s, consolewire.RequestPass, svc.RequestGitHubPass)
	handleProto(svc, s, consolewire.ListApps, svc.ListGitHubApps, consolecall.List())
	handleProto(svc, s, consolewire.GetApp, svc.GetGitHubApp)
	handleProto(svc, s, consolewire.ListAppTokens, svc.ListGitHubAppTokens, consolecall.List())
	handleProto(svc, s, consolewire.CheckApp, svc.CheckGitHubApp)
	handleProto(svc, s, consolewire.RotateAppWebhook, svc.RotateGitHubAppWebhook)
	handleProto(svc, s, consolewire.GetStatus, svc.GetGitHubStatus, consolecall.List())
	handleProto(svc, s, consolewire.CheckCatalogueApp, svc.CheckGitHubCatalogueApp)
	consolecall.HandleJSON(s, consolewire.BeginCatalogueConnect, adapt(svc, func(ctx context.Context, r consolewire.BeginCatalogueConnectRequest) (consolewire.Begun, error) {
		return svc.BeginGitHubCatalogueAppConnect(ctx, r.ID)
	}))
	consolecall.HandleJSON(s, consolewire.CompleteCatalogueCreate, adapt(svc, svc.CompleteGitHubCatalogueCreate))
	consolecall.HandleJSON(s, consolewire.CompleteCatalogueSetup, adapt(svc, svc.CompleteGitHubCatalogueSetup))
	handleProto(svc, s, consolewire.DisconnectCatalogueApp, svc.DisconnectGitHubCatalogueApp)
	consolecall.HandleJSON(s, consolewire.BeginRunnerConnect, adapt(svc, func(ctx context.Context, r consolewire.BeginRunnerConnectRequest) (consolewire.Begun, error) {
		return svc.BeginGitHubRunnerAppConnect(ctx, r.Org, r.Tier)
	}))
	consolecall.HandleJSON(s, consolewire.CompleteRunnerCreate, adapt(svc, svc.CompleteGitHubRunnerCreate))
	consolecall.HandleJSON(s, consolewire.CompleteRunnerSetup, adapt(svc, svc.CompleteGitHubRunnerSetup))
	handleProto(svc, s, consolewire.DisconnectRunnerApp, svc.DisconnectGitHubRunnerApp)
}

// begin adapts a handler of plain values: the snapshot goes in the context, and
// an error comes out as the console's reply.
func adapt[Req, Resp any](
	c *Service, fn func(context.Context, Req) (Resp, error),
) func(context.Context, consolecall.Caller, Req) (Resp, error) {
	return func(ctx context.Context, caller consolecall.Caller, req Req) (Resp, error) {
		resp, err := fn(WithCaller(ctx, caller), req)
		return resp, c.told(ctx, err)
	}
}

func handleProto[Req, Resp any, PReq interface {
	*Req
	proto.Message
}, PResp interface {
	*Resp
	proto.Message
}](
	c *Service, s *modcall.Server, method string,
	fn func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error), opts ...modcall.Option,
) {
	consolecall.Handle(s, method, func(ctx context.Context, caller consolecall.Caller, req PReq) (PResp, error) {
		resp, err := fn(WithCaller(ctx, caller), connect.NewRequest((*Req)(req)))
		if err != nil {
			return nil, c.told(ctx, err)
		}
		return PResp(resp.Msg), nil
	}, opts...)
}

// told is err as the console is told it. A refusal a person should read (a
// Connect error of a client's making, or a flow's Problem) crosses as a
// Problem; anything else is the module's own failure, which crosses with no
// detail and stays in the log.
func (c *Service) told(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var p *consolecall.Problem
	if errors.As(err, &p) {
		return p
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		ce = nil
	}
	if ce != nil {
		switch ce.Code() {
		case connect.CodeInvalidArgument, connect.CodeNotFound, connect.CodeFailedPrecondition, connect.CodePermissionDenied,
			connect.CodeResourceExhausted, connect.CodeAlreadyExists, connect.CodeUnauthenticated, connect.CodeAborted,
			connect.CodeOutOfRange,
			// A failure of the records or of GitHub keeps its code and its
			// words, as the handlers have always told it: it is the operator's
			// to act on, and nothing in it is a secret.
			connect.CodeUnavailable, connect.CodeDeadlineExceeded:
			return &consolecall.Problem{Summary: ce.Message(), Code: ce.Code().String()}
		}
	}
	c.log().ErrorContext(ctx, "a console operation failed in the GitHub module", "error", err)
	return modcall.Coded(modcall.CodeInternal, "")
}
