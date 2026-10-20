package console

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect/v2"

	backendpkg "github.com/truvity/sluis/backend"
	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/modcall"
)

// CompleteDeadline bounds the consent's completion: the exchange with Google
// and the first read of the directory happen in one call, and the browser is
// waiting on it.
const CompleteDeadline = 25 * time.Second

// Client is the console's view of the module: one method per console method,
// each sent as the caller class `console`.
type Client struct {
	c modcall.Caller
}

// NewClient is the console's calls to the Google module through c, which sends
// as the class `console`.
func NewClient(c modcall.Caller) *Client { return &Client{c: c} }

// ListWorkspaces implements the console's call of the same name.
func (c *Client) ListWorkspaces(ctx context.Context, caller consolecall.Caller) (*directoryrosterv1.ListWorkspacesResponse, error) {
	return consolecall.Proxy[*directoryrosterv1.ListWorkspacesRequest, *directoryrosterv1.ListWorkspacesResponse](
		ctx, c.c, Module, MethodListWorkspaces, caller, &directoryrosterv1.ListWorkspacesRequest{})
}

// BeginConnect asks where the browser goes to consent to a new connection.
func (c *Client) BeginConnect(ctx context.Context, caller consolecall.Caller, kind, state string) (string, error) {
	out, err := callJSON[BeginRequest, BeginResponse](ctx, c.c, MethodBeginConnect, caller, BeginRequest{Backend: kind, State: state})
	return out.URL, err
}

// Reconnect asks the same for an existing workspace.
func (c *Client) Reconnect(ctx context.Context, caller consolecall.Caller, workspace, state string) (string, error) {
	out, err := callJSON[BeginRequest, BeginResponse](ctx, c.c, MethodReconnect, caller, BeginRequest{Bind: workspace, State: state})
	return out.URL, err
}

// CompleteConsent forwards the callback's code. It is never retried: the code
// is single-use, so a second send after a lost answer would find it spent and
// report a failure for a connection that was made. It is bounded by
// [CompleteDeadline].
func (c *Client) CompleteConsent(ctx context.Context, caller consolecall.Caller, req CompleteRequest) (CompleteResponse, error) {
	ctx, cancel := context.WithTimeout(modcall.WithoutRetry(ctx), CompleteDeadline)
	defer cancel()
	return callJSON[CompleteRequest, CompleteResponse](ctx, c.c, MethodCompleteConsent, caller, req)
}

// UploadKey forwards a service-account key. The key is in memory for the length
// of the call and is not written anywhere here; more than [MaxKeyBytes] is
// refused before it is sent.
func (c *Client) UploadKey(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.UploadKeyRequest,
) (*directoryrosterv1.UploadKeyResponse, error) {
	if len(req.GetKey()) > MaxKeyBytes {
		return nil, modcall.Coded(CodeInvalidArgument, fmt.Sprintf("a key is at most %d KB", MaxKeyBytes>>10))
	}
	return consolecall.Proxy[*directoryrosterv1.UploadKeyRequest, *directoryrosterv1.UploadKeyResponse](
		ctx, c.c, Module, MethodUploadKey, caller, req)
}

// SetServedDomains implements the console's call of the same name.
func (c *Client) SetServedDomains(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.SetServedDomainsRequest,
) (*directoryrosterv1.SetServedDomainsResponse, error) {
	return consolecall.Proxy[*directoryrosterv1.SetServedDomainsRequest, *directoryrosterv1.SetServedDomainsResponse](
		ctx, c.c, Module, MethodSetServedDomains, caller, req)
}

// SetSyncedGroups implements the console's call of the same name.
func (c *Client) SetSyncedGroups(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.SetSyncedGroupsRequest,
) (*directoryrosterv1.SetSyncedGroupsResponse, error) {
	return consolecall.Proxy[*directoryrosterv1.SetSyncedGroupsRequest, *directoryrosterv1.SetSyncedGroupsResponse](
		ctx, c.c, Module, MethodSetSyncedGroups, caller, req)
}

// Probe implements the console's call of the same name.
func (c *Client) Probe(ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.ProbeRequest) (*directoryrosterv1.ProbeResponse, error) {
	return consolecall.Proxy[*directoryrosterv1.ProbeRequest, *directoryrosterv1.ProbeResponse](
		ctx, c.c, Module, MethodProbe, caller, req)
}

// Refresh implements the console's call of the same name.
func (c *Client) Refresh(ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.RefreshRequest) (*directoryrosterv1.RefreshResponse, error) {
	return consolecall.Proxy[*directoryrosterv1.RefreshRequest, *directoryrosterv1.RefreshResponse](
		ctx, c.c, Module, MethodRefresh, caller, req)
}

// Disconnect implements the console's call of the same name.
func (c *Client) Disconnect(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.DisconnectRequest,
) (*directoryrosterv1.DisconnectResponse, error) {
	return consolecall.Proxy[*directoryrosterv1.DisconnectRequest, *directoryrosterv1.DisconnectResponse](
		ctx, c.c, Module, MethodDisconnect, caller, req)
}

// ConnectError is an error of the module as the console's Connect handler
// returns it. The module's refusals keep their fixed words; a transport failure
// says the module could not be reached and nothing else, since what went wrong
// is the log's.
func ConnectError(err error) error {
	if err == nil {
		return nil
	}
	var p *consolecall.Problem
	if errors.As(err, &p) {
		return connect.NewError(connect.CodeFailedPrecondition, p.Error()).WithCause(p)
	}
	var coded *modcall.Error
	if errors.As(err, &coded) {
		msg := errors.New(coded.Message)
		switch coded.Code {
		case modcall.CodeForbidden:
			return connect.NewError(connect.CodePermissionDenied, RefusalText)
		case CodeUnauthenticated:
			return connect.NewError(connect.CodeUnauthenticated, "sign in first")
		case CodeInvalidArgument, modcall.CodeBadRequest:
			return connect.NewError(connect.CodeInvalidArgument, msg.Error()).WithCause(msg)
		case CodeNotFound:
			return connect.NewError(connect.CodeNotFound, msg.Error()).WithCause(msg)
		case CodeFailedPrecondition:
			return connect.NewError(connect.CodeFailedPrecondition, msg.Error()).WithCause(msg)
		case CodeUnimplemented:
			return connect.NewError(connect.CodeUnimplemented, msg.Error()).WithCause(msg)
		case modcall.CodeDeadlineExceeded:
			return connect.NewError(connect.CodeDeadlineExceeded, "the Google module did not answer in time")
		}
		return connect.NewError(connect.CodeInternal, "the Google module failed")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return connect.NewError(connect.CodeDeadlineExceeded, "the Google module did not answer in time")
	}
	return connect.NewError(connect.CodeUnavailable, "the Google module could not be reached")
}

// RemoteConnector is the Google connector as the issuer holds it when the
// module is another function: it offers the kind, and the sign-in through the
// module (`google.sign_in_url`, `google.identify`). The consent and the key
// upload are the module's, reached through [Client]; this connector never
// holds a client secret or a key, and its Exchange and FromKey refuse.
type RemoteConnector struct {
	c modcall.Caller
}

var (
	_ SignInConnector = (*RemoteConnector)(nil)
	_ KeyConnector    = (*RemoteConnector)(nil)
)

// NewRemoteConnector is the connector over c, which sends as the caller class
// `issuer`.
func NewRemoteConnector(c modcall.Caller) *RemoteConnector { return &RemoteConnector{c: c} }

// ErrRemote is what a connector method that belongs to the module answers.
var ErrRemote = errors.New("google: this belongs to the Google module, which the console reaches by a call")

// Kind implements [Connector].
func (*RemoteConnector) Kind() string { return "google" }

// AuthURL implements [Connector]: the consent is begun at the module.
func (*RemoteConnector) AuthURL(string) (string, error) { return "", ErrRemote }

// Exchange implements [Connector]: the code is forwarded to the module.
func (*RemoteConnector) Exchange(context.Context, string, string) (hub.Workspace, backendpkg.Backend, error) {
	return hub.Workspace{}, nil, ErrRemote
}

// FromKey implements [KeyConnector]: a key is forwarded to the module.
func (*RemoteConnector) FromKey(context.Context, []byte, string) (hub.Workspace, backendpkg.Backend, error) {
	return hub.Workspace{}, nil, ErrRemote
}

// SignInURL implements [SignInConnector]: the module holds the client the
// address is built from.
func (r *RemoteConnector) SignInURL(state string) (string, error) {
	out, err := modcall.Do[signInURLRequest, signInURLResponse](context.Background(), r.c, Module, MethodSignInURL, signInURLRequest{State: state})
	return out.URL, err
}

// Identify implements [SignInConnector] with `google.identify`. A module that
// is cold answers late and one that is gone answers not at all, so the error is
// the caller's to show: a sign-in cannot be completed without it.
func (r *RemoteConnector) Identify(ctx context.Context, code string) (string, error) {
	out, err := modcall.Do[identifyRequest, identifyResponse](modcall.WithoutRetry(ctx), r.c, Module, MethodIdentify, identifyRequest{Code: code})
	return out.Email, err
}
