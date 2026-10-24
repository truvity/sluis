// Package console is the Outline module's side of the console's Outline pages:
// the console authenticates the person and calls these methods as the caller
// class `console` with a snapshot of who they are (internal/consolecall). They
// run in the Outline module, the only holder of the workspaces' API keys
// (docs/decisions/0077): a key is checked with Outline and kept here, and no
// answer ever carries it back.
package console

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/truvity/sluis/audit/sdk/record"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/egress"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/outlineroster/connection"
	"github.com/truvity/sluis/internal/outlineroster/outlineapi"
	"github.com/truvity/sluis/internal/outlineroster/status"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/storage/logattr"
)

// Module is the module's name on the wire.
const Module = "outline"

// The console methods, as `outline.<method>`.
const (
	MethodGetStatus           = "console.get_status"
	MethodConnectWorkspace    = "console.connect_workspace"
	MethodDisconnectWorkspace = "console.disconnect_workspace"
	MethodRequestPass         = "console.request_pass"
	MethodConfirmBreaker      = "console.confirm_breaker"
)

// Workspaces is where workspaces are connected (internal/portstore.OutlineWorkspaces).
type Workspaces interface {
	List(ctx context.Context) ([]connection.Record, error)
	Record(ctx context.Context, workspace string) (connection.Record, bool, error)
	Put(ctx context.Context, record connection.Record, credential connection.Credential) error
	Delete(ctx context.Context, workspace string) error
	PutConfirmation(ctx context.Context, confirmation connection.Confirmation) error
	RequestPass(ctx context.Context, r connection.PassRequest) (kept bool, last time.Time, err error)
}

// Reports is what the controller last reported, one document per workspace.
type Reports interface {
	Reports(ctx context.Context) (map[string]string, error)
}

// Deps are what the service is built on.
type Deps struct {
	Log *slog.Logger
	// Policy is the policy the module decides by.
	Policy     *policy.Set
	Workspaces Workspaces
	Status     Reports
	// Enabled are the workspaces the controller acts in.
	Enabled []string
	// HTTP calls Outline when a key is checked. Nil keeps private addresses
	// and refuses loopback and link-local ones.
	HTTP  *http.Client
	Audit audit.Recorder
	Now   func() time.Time
}

// Service is the module's console service.
type Service struct{ deps Deps }

// NewService returns the service.
func NewService(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.HTTP == nil {
		d.HTTP = &http.Client{Timeout: callTimeout, Transport: egress.Policy{AllowPrivate: true}.Transport()}
	}
	return &Service{deps: d}
}

// callTimeout bounds the one call to Outline that connecting makes.
const callTimeout = 15 * time.Second

func require(caller consolecall.Caller, role access.Role) (access.Identity, error) {
	id := caller.Identity()
	if !id.Can(role) {
		return id, connect.NewError(connect.CodePermissionDenied, "this needs the "+string(role)+" role")
	}
	return id, nil
}

func (c *Service) declared(workspace string) error {
	if c.deps.Policy == nil || !c.deps.Policy.OutlineWorkspaceDeclared(workspace) {
		return connect.NewError(connect.CodeInvalidArgument, "the policy declares no Outline workspace "+workspace)
	}
	return nil
}

// report is a workspace's last report and its document; ok is false before the
// first pass.
func (c *Service) report(ctx context.Context, workspace string) (status.Workspace, string, bool) {
	if c.deps.Status == nil {
		return status.Workspace{}, "", false
	}
	reports, err := c.deps.Status.Reports(ctx)
	if err != nil {
		c.deps.Log.WarnContext(ctx, "the Outline controller's reports could not be read", logattr.SafeError("error", err))
		return status.Workspace{}, "", false
	}
	raw, ok := reports[status.Key(workspace)]
	if !ok {
		return status.Workspace{}, "", false
	}
	report, err := status.Decode(raw)
	return report, raw, err == nil
}

// GetOutlineStatus is every declared workspace, its connection and its last
// report. Viewer.
func (c *Service) GetOutlineStatus(
	ctx context.Context, caller consolecall.Caller, _ *directoryrosterv1.GetOutlineStatusRequest,
) (*directoryrosterv1.GetOutlineStatusResponse, error) {
	if _, err := require(caller, access.RoleViewer); err != nil {
		return nil, err
	}
	out := &directoryrosterv1.GetOutlineStatusResponse{}
	if c.deps.Policy == nil {
		return out, nil
	}
	records := map[string]connection.Record{}
	list, err := c.deps.Workspaces.List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the Outline connections could not be read")
	}
	for i := range list {
		records[list[i].Workspace] = list[i]
	}
	enabled := map[string]bool{}
	for _, ws := range c.deps.Enabled {
		enabled[ws] = true
	}
	for _, key := range c.deps.Policy.OutlineWorkspaceKeys() {
		ws := &directoryrosterv1.OutlineWorkspaceStatus{Workspace: key, Enabled: enabled[key]}
		if r, ok := records[key]; ok {
			ws.Connected, ws.Url, ws.KeyUser, ws.ConnectedBy = true, r.URL, r.KeyUser, r.ConnectedBy
			if !r.ConnectedAt.IsZero() {
				ws.ConnectedAt = timestamppb.New(r.ConnectedAt)
			}
		}
		if _, raw, ok := c.report(ctx, key); ok {
			ws.ReportJson = raw
		}
		out.Workspaces = append(out.Workspaces, ws)
	}
	return out, nil
}

// ConnectOutlineWorkspace checks an admin's API key with Outline and keeps it
// beside the workspace's record. Operator.
func (c *Service) ConnectOutlineWorkspace(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.ConnectOutlineWorkspaceRequest,
) (*directoryrosterv1.ConnectOutlineWorkspaceResponse, error) {
	id, err := require(caller, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetWorkspace())
	if err = c.declared(key); err != nil {
		return nil, err
	}
	url, err := connection.NormaliseURL(req.GetUrl())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	apiKey := strings.TrimSpace(req.GetApiKey())
	if apiKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "an API key is required")
	}
	actor := audit.Identified(id.Who())
	ws := audit.OutlineWorkspace{Key: key, URL: url}
	refuse := func(code connect.Code, why string) error {
		c.rec(ctx, audit.OutlineWorkspaceConnected(actor, ws, audit.Denied(why)))
		return connect.NewError(code, why)
	}
	call, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	info, err := outlineapi.New(url, apiKey, outlineapi.WithHTTPClient(c.deps.HTTP)).AuthInfo(call)
	if err != nil {
		var refused *outlineapi.Error
		if errors.As(err, &refused) {
			return nil, refuse(connect.CodeFailedPrecondition, "Outline refused the key: "+refused.Error())
		}
		return nil, refuse(connect.CodeUnavailable, "Outline could not be reached at "+url)
	}
	ws.KeyUser, ws.Team = info.User.Email, info.Team.ID
	if info.User.Role != "admin" {
		return nil, refuse(connect.CodeFailedPrecondition, "the API key's user "+info.User.Email+" is not an Outline admin; the controller needs an admin's key")
	}
	previous, found, err := c.deps.Workspaces.Record(ctx, key)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the workspace's connection could not be read")
	}
	if found && previous.Team != "" && previous.Team != info.Team.ID {
		return nil, refuse(connect.CodeFailedPrecondition, "the key belongs to another Outline team than the one "+key+
			" was connected to; disconnect the workspace first to connect another Outline")
	}
	keyUser := strings.ToLower(strings.TrimSpace(info.User.Email))
	err = c.deps.Workspaces.Put(ctx, connection.Record{
		Workspace: key, URL: url, KeyUser: keyUser, KeyUserID: info.User.ID, Team: info.Team.ID,
		ConnectedBy: id.Who(), ConnectedAt: c.deps.Now().UTC(),
	}, connection.Credential{Workspace: key, APIKey: apiKey})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the connection could not be kept")
	}
	c.rec(ctx, audit.OutlineWorkspaceConnected(actor, ws, audit.Succeeded()))
	ignored := c.deps.Policy.Declared().Outline.Workspaces[key].Ignored(keyUser)
	return &directoryrosterv1.ConnectOutlineWorkspaceResponse{KeyUser: keyUser, KeyUserIgnored: ignored}, nil
}

// DisconnectOutlineWorkspace forgets a workspace's record, key, confirmations
// and pass request. Nothing in Outline changes. Operator.
func (c *Service) DisconnectOutlineWorkspace(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.DisconnectOutlineWorkspaceRequest,
) (*directoryrosterv1.DisconnectOutlineWorkspaceResponse, error) {
	id, err := require(caller, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetWorkspace())
	previous, found, err := c.deps.Workspaces.Record(ctx, key)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the workspace's connection could not be read")
	}
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, "the Outline workspace "+key+" is not connected")
	}
	if err = c.deps.Workspaces.Delete(ctx, key); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the connection could not be removed")
	}
	c.rec(ctx, audit.OutlineWorkspaceDisconnected(audit.Identified(id.Who()),
		audit.OutlineWorkspace{Key: key, URL: previous.URL, KeyUser: previous.KeyUser, Team: previous.Team}))
	return &directoryrosterv1.DisconnectOutlineWorkspaceResponse{}, nil
}

// RequestOutlinePass asks for a pass over one workspace now, at most once a
// minute. Operator.
func (c *Service) RequestOutlinePass(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.RequestOutlinePassRequest,
) (*directoryrosterv1.RequestOutlinePassResponse, error) {
	id, err := require(caller, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetWorkspace())
	if err = c.declared(key); err != nil {
		return nil, err
	}
	kept, last, err := c.deps.Workspaces.RequestPass(ctx, connection.PassRequest{Workspace: key, By: id.Who(), At: c.deps.Now().UTC()})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the request could not be kept")
	}
	out := &directoryrosterv1.RequestOutlinePassResponse{Kept: kept}
	if !last.IsZero() {
		out.Last = timestamppb.New(last)
	}
	return out, nil
}

// ConfirmOutlineBreaker lets one held set go ahead: only the latest report's
// fingerprint for the scope is accepted. Operator.
func (c *Service) ConfirmOutlineBreaker(
	ctx context.Context, caller consolecall.Caller, req *directoryrosterv1.ConfirmOutlineBreakerRequest,
) (*directoryrosterv1.ConfirmOutlineBreakerResponse, error) {
	id, err := require(caller, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetWorkspace())
	report, _, ok := c.report(ctx, key)
	var breaker *status.Breaker
	for i := range report.Breakers {
		if report.Breakers[i].Scope == req.GetScope() && report.Breakers[i].Fingerprint == req.GetFingerprint() {
			breaker = &report.Breakers[i]
		}
	}
	if !ok || breaker == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the latest report holds no such set; reload and confirm what it shows")
	}
	err = c.deps.Workspaces.PutConfirmation(ctx, connection.Confirmation{
		Workspace: key, Scope: breaker.Scope, Fingerprint: breaker.Fingerprint, By: id.Who(), At: c.deps.Now().UTC(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the confirmation could not be kept")
	}
	c.rec(ctx, audit.OutlineBreakerConfirmed(audit.Identified(id.Who()), audit.OutlineBreaker{
		Workspace: key, Scope: breaker.Scope, Fingerprint: breaker.Fingerprint, Affected: breaker.Affected, Total: breaker.Total,
	}))
	return &directoryrosterv1.ConfirmOutlineBreakerResponse{}, nil
}

// Register puts the console methods on the module's server, over a service
// that open assembles on the first call of a container. A service that cannot
// be opened is a call the transport answers unavailable.
func Register(s *modcall.Server, log *slog.Logger, open func(context.Context) (*Service, error)) {
	if log == nil {
		log = slog.Default()
	}
	handle(s, log, open, MethodGetStatus, (*Service).GetOutlineStatus, consolecall.List())
	handle(s, log, open, MethodConnectWorkspace, (*Service).ConnectOutlineWorkspace)
	handle(s, log, open, MethodDisconnectWorkspace, (*Service).DisconnectOutlineWorkspace)
	handle(s, log, open, MethodRequestPass, (*Service).RequestOutlinePass)
	handle(s, log, open, MethodConfirmBreaker, (*Service).ConfirmOutlineBreaker)
}

// NewServer is the module's server with the console methods on it.
func NewServer(log *slog.Logger, open func(context.Context) (*Service, error)) *modcall.Server {
	s := modcall.NewServer(Module)
	Register(s, log, open)
	return s
}

func handle[Req, Resp proto.Message](
	s *modcall.Server, log *slog.Logger, open func(context.Context) (*Service, error), method string,
	fn func(*Service, context.Context, consolecall.Caller, Req) (Resp, error), opts ...modcall.Option,
) {
	consolecall.Handle(s, method, func(ctx context.Context, caller consolecall.Caller, req Req) (Resp, error) {
		var zero Resp
		svc, err := open(ctx)
		if err != nil {
			log.ErrorContext(ctx, "the Outline console service could not be opened", slog.String("method", method), logattr.SafeError("error", err))
			return zero, modcall.Coded(modcall.CodeUnavailable, "")
		}
		resp, err := fn(svc, ctx, caller, req)
		return resp, told(ctx, log, method, err)
	}, opts...)
}

// told turns what a handler returned into what crosses the wire: a refusal the
// person reads is a Problem with its code; the module's own failure is logged
// here and told as a code with no detail.
func told(ctx context.Context, log *slog.Logger, method string, err error) error {
	var cerr *connect.Error
	var coded *modcall.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &coded):
		return err
	case errors.As(err, &cerr):
		switch cerr.Code() {
		case connect.CodePermissionDenied:
			return modcall.Coded(modcall.CodeForbidden, cerr.Message())
		case connect.CodeUnauthenticated:
			return modcall.Coded(consoleauthz.CodeUnauthenticated, cerr.Message())
		case connect.CodeInternal, connect.CodeUnknown:
		default:
			return &consolecall.Problem{Code: cerr.Code().String(), Summary: cerr.Message()}
		}
	}
	log.ErrorContext(ctx, "an Outline console method failed", slog.String("method", method), logattr.SafeError("error", err))
	return modcall.Coded(modcall.CodeInternal, "")
}

// CodeOf is the Connect code a Problem names, a failed precondition when it
// names none.
func CodeOf(p *consolecall.Problem) connect.Code {
	var code connect.Code
	if p == nil || p.Code == "" || code.UnmarshalText([]byte(p.Code)) != nil {
		return connect.CodeFailedPrecondition
	}
	return code
}

// rec writes down something done through the console.
func (c *Service) rec(ctx context.Context, r *record.Record) {
	if c.deps.Audit != nil {
		c.deps.Audit.Record(ctx, r)
	}
}
