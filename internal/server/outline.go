package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
	outlineconsole "github.com/truvity/sluis/internal/outlineroster/console"
	"github.com/truvity/sluis/internal/rpcmount"
)

// The console's Outline pages run in the Outline module (docs/decisions/0077),
// the only holder of the workspaces' API keys: the console authenticates the
// person and calls the module as the caller class `console` with a snapshot of
// who they are. The issuer never runs the module, so without `outline.remote`
// there are no Outline pages.

// outlineCallTimeout bounds one call to the module: connecting asks Outline
// once, and a cold start of the module must not hold the console.
const outlineCallTimeout = 30 * time.Second

// UseOutline connects the Outline pages to the module, called as the caller
// class `console`.
func (s *ConsoleServer) UseOutline(m modcall.Caller) { s.console.deps.Modules.Outline = m }

// errNoOutline is the answer of an installation that runs no Outline module.
var errNoOutline = errors.New("this installation runs no Outline module: the issuer's document names none under outline.remote")

// outlineProxy calls one console method of the Outline module as the person.
func outlineProxy[Req, Resp proto.Message](ctx context.Context, c *Console, method string, req Req) (Resp, error) {
	var zero Resp
	id, ok := IdentityFrom(ctx)
	if !ok {
		return zero, connect.NewError(connect.CodeUnauthenticated, "sign in first")
	}
	m := c.deps.Modules.Outline
	if m == nil {
		return zero, connect.NewError(connect.CodeFailedPrecondition, errNoOutline.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, outlineCallTimeout)
	defer cancel()
	resp, err := consolecall.Proxy[Req, Resp](ctx, m, outlineconsole.Module, method, consolecall.Snapshot(id), req)
	return resp, outlineError(err)
}

// outlineError is what the module answered as the Connect error the page shows.
func outlineError(err error) error {
	var problem *consolecall.Problem
	var coded *modcall.Error
	var already *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &already):
		return err
	case errors.As(err, &problem):
		return connect.NewError(outlineconsole.CodeOf(problem), problem.Summary)
	case errors.As(err, &coded):
		switch coded.Code {
		case modcall.CodeForbidden:
			return connect.NewError(connect.CodePermissionDenied, coded.Message)
		case consoleauthz.CodeUnauthenticated:
			return connect.NewError(connect.CodeUnauthenticated, coded.Message)
		case modcall.CodeBadRequest:
			return connect.NewError(connect.CodeInvalidArgument, coded.Message)
		case modcall.CodeInternal:
			return connect.NewError(connect.CodeInternal, "the Outline module failed; see its log")
		}
	}
	return rpcmount.NewError(connect.CodeUnavailable, fmt.Errorf("the Outline module did not answer: %w", modcallCause(err)))
}

// GetOutlineStatus is every declared Outline workspace. Viewer.
func (c *Console) GetOutlineStatus(
	ctx context.Context, req *directoryrosterv1.GetOutlineStatusRequest,
) (*directoryrosterv1.GetOutlineStatusResponse, error) {
	return outlineProxy[*directoryrosterv1.GetOutlineStatusRequest, *directoryrosterv1.GetOutlineStatusResponse](
		ctx, c, outlineconsole.MethodGetStatus, req)
}

// ConnectOutlineWorkspace keeps a workspace's URL and API key in the module.
// Operator.
func (c *Console) ConnectOutlineWorkspace(
	ctx context.Context, req *directoryrosterv1.ConnectOutlineWorkspaceRequest,
) (*directoryrosterv1.ConnectOutlineWorkspaceResponse, error) {
	return outlineProxy[*directoryrosterv1.ConnectOutlineWorkspaceRequest, *directoryrosterv1.ConnectOutlineWorkspaceResponse](
		ctx, c, outlineconsole.MethodConnectWorkspace, req)
}

// DisconnectOutlineWorkspace forgets a workspace's connection. Operator.
func (c *Console) DisconnectOutlineWorkspace(
	ctx context.Context, req *directoryrosterv1.DisconnectOutlineWorkspaceRequest,
) (*directoryrosterv1.DisconnectOutlineWorkspaceResponse, error) {
	return outlineProxy[*directoryrosterv1.DisconnectOutlineWorkspaceRequest, *directoryrosterv1.DisconnectOutlineWorkspaceResponse](
		ctx, c, outlineconsole.MethodDisconnectWorkspace, req)
}

// RequestOutlinePass asks for a pass over one workspace now. Operator.
func (c *Console) RequestOutlinePass(
	ctx context.Context, req *directoryrosterv1.RequestOutlinePassRequest,
) (*directoryrosterv1.RequestOutlinePassResponse, error) {
	return outlineProxy[*directoryrosterv1.RequestOutlinePassRequest, *directoryrosterv1.RequestOutlinePassResponse](
		ctx, c, outlineconsole.MethodRequestPass, req)
}

// ConfirmOutlineBreaker lets one held set go ahead. Operator.
func (c *Console) ConfirmOutlineBreaker(
	ctx context.Context, req *directoryrosterv1.ConfirmOutlineBreakerRequest,
) (*directoryrosterv1.ConfirmOutlineBreakerResponse, error) {
	return outlineProxy[*directoryrosterv1.ConfirmOutlineBreakerRequest, *directoryrosterv1.ConfirmOutlineBreakerResponse](
		ctx, c, outlineconsole.MethodConfirmBreaker, req)
}
