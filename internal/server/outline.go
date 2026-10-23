package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/egress"
	"github.com/truvity/sluis/internal/outlineroster/connection"
	"github.com/truvity/sluis/internal/outlineroster/outlineapi"
	"github.com/truvity/sluis/internal/outlineroster/status"
	"github.com/truvity/sluis/policy"
)

// OutlineWorkspaces is where Outline workspaces are connected, and where
// operators' confirmations and requests for a pass are kept
// (internal/portstore.OutlineWorkspaces). The console writes the key and never
// reads it back.
type OutlineWorkspaces interface {
	List(ctx context.Context) ([]connection.Record, error)
	Record(ctx context.Context, workspace string) (connection.Record, bool, error)
	Put(ctx context.Context, record connection.Record, credential connection.Credential) error
	Delete(ctx context.Context, workspace string) error
	PutConfirmation(ctx context.Context, confirmation connection.Confirmation) error
	RequestPass(ctx context.Context, r connection.PassRequest) (kept bool, last time.Time, err error)
}

// OutlineStatusReports is what the Outline controller last reported, one
// document per workspace.
type OutlineStatusReports interface {
	Reports(ctx context.Context) (map[string]string, error)
}

// outlineCallTimeout bounds the one call to Outline that connecting makes.
const outlineCallTimeout = 15 * time.Second

func (c *Console) outlineStore() (OutlineWorkspaces, error) {
	if c.deps.OutlineWorkspaces == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "this deployment keeps no Outline records: it needs a storage adapter other than legacy")
	}
	return c.deps.OutlineWorkspaces, nil
}

func (c *Console) outlineDeclared(workspace string) error {
	if c.deps.Authorizer == nil || !c.deps.Authorizer.Policy().OutlineWorkspaceDeclared(workspace) {
		return connect.NewError(connect.CodeInvalidArgument, "the policy declares no Outline workspace "+workspace)
	}
	return nil
}

// outlineReport is a workspace's last report, decoded; ok is false before the
// first pass.
func (c *Console) outlineReport(ctx context.Context, workspace string) (status.Workspace, string, bool) {
	if c.deps.OutlineStatus == nil {
		return status.Workspace{}, "", false
	}
	reports, err := c.deps.OutlineStatus.Reports(ctx)
	if err != nil {
		c.log().WarnContext(ctx, "the Outline controller's reports could not be read", "error", err)
		return status.Workspace{}, "", false
	}
	raw, ok := reports[status.Key(workspace)]
	if !ok {
		return status.Workspace{}, "", false
	}
	report, err := status.Decode(raw)
	return report, raw, err == nil
}

// GetOutlineStatus implements the operator contract.
func (c *Console) GetOutlineStatus(ctx context.Context, _ *directoryrosterv1.GetOutlineStatusRequest) (*directoryrosterv1.GetOutlineStatusResponse, error) {
	if _, err := requireRole(ctx, access.RoleViewer); err != nil {
		return nil, err
	}
	out := &directoryrosterv1.GetOutlineStatusResponse{}
	if c.deps.Authorizer == nil {
		return out, nil
	}
	records := map[string]connection.Record{}
	if c.deps.OutlineWorkspaces != nil {
		list, err := c.deps.OutlineWorkspaces.List(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, "the Outline connections could not be read").WithCause(err)
		}
		for _, r := range list {
			records[r.Workspace] = r
		}
	}
	enabled := map[string]bool{}
	for _, ws := range c.deps.OutlineEnabled {
		enabled[ws] = true
	}
	for _, key := range c.deps.Authorizer.Policy().OutlineWorkspaceKeys() {
		ws := &directoryrosterv1.OutlineWorkspaceStatus{Workspace: key, Enabled: enabled[key]}
		if r, ok := records[key]; ok {
			ws.Connected, ws.Url, ws.KeyUser, ws.ConnectedBy = true, r.URL, r.KeyUser, r.ConnectedBy
			if !r.ConnectedAt.IsZero() {
				ws.ConnectedAt = timestamppb.New(r.ConnectedAt)
			}
		}
		if _, raw, ok := c.outlineReport(ctx, key); ok {
			ws.ReportJson = raw
		}
		out.Workspaces = append(out.Workspaces, ws)
	}
	return out, nil
}

func (c *Console) outlineHTTP() *http.Client {
	if c.deps.OutlineHTTP != nil {
		return c.deps.OutlineHTTP
	}
	// The URL is an operator's, and may well be a Service in the cluster; it
	// is never the metadata service or this host.
	return &http.Client{Timeout: outlineCallTimeout, Transport: egress.Policy{AllowPrivate: true}.Transport()}
}

// ConnectOutlineWorkspace implements the operator contract.
func (c *Console) ConnectOutlineWorkspace(
	ctx context.Context, req *directoryrosterv1.ConnectOutlineWorkspaceRequest,
) (*directoryrosterv1.ConnectOutlineWorkspaceResponse, error) {
	id, err := requireRole(ctx, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	store, err := c.outlineStore()
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetWorkspace())
	if err = c.outlineDeclared(key); err != nil {
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
	record := audit.OutlineWorkspace{Key: key, URL: url}
	refuse := func(code connect.Code, why string) error {
		c.record(ctx, audit.OutlineWorkspaceConnected(actorOf(ctx), record, audit.Denied(why)))
		return connect.NewError(code, why)
	}
	call, cancel := context.WithTimeout(ctx, outlineCallTimeout)
	defer cancel()
	info, err := outlineapi.New(url, apiKey, outlineapi.WithHTTPClient(c.outlineHTTP())).AuthInfo(call)
	if err != nil {
		var refused *outlineapi.Error
		if errors.As(err, &refused) {
			return nil, refuse(connect.CodeFailedPrecondition, "Outline refused the key: "+refused.Error())
		}
		return nil, refuse(connect.CodeUnavailable, "Outline could not be reached at "+url)
	}
	record.KeyUser, record.Team = info.User.Email, info.Team.ID
	if info.User.Role != "admin" {
		return nil, refuse(connect.CodeFailedPrecondition, "the API key's user "+info.User.Email+" is not an Outline admin; the controller needs an admin's key")
	}
	previous, found, err := store.Record(ctx, key)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the workspace's connection could not be read").WithCause(err)
	}
	if found && previous.Team != "" && previous.Team != info.Team.ID {
		return nil, refuse(connect.CodeFailedPrecondition, "the key belongs to another Outline team than the one "+key+
			" was connected to; disconnect the workspace first to connect another Outline")
	}
	keyUser := strings.ToLower(strings.TrimSpace(info.User.Email))
	err = store.Put(ctx, connection.Record{
		Workspace: key, URL: url, KeyUser: keyUser, KeyUserID: info.User.ID, Team: info.Team.ID,
		ConnectedBy: id.Who(), ConnectedAt: time.Now().UTC(),
	}, connection.Credential{Workspace: key, APIKey: apiKey})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the connection could not be kept").WithCause(err)
	}
	c.record(ctx, audit.OutlineWorkspaceConnected(actorOf(ctx), record, audit.Succeeded()))
	ignored := c.deps.Authorizer.Policy().Declared().Outline.Workspaces[key].Ignored(keyUser)
	return &directoryrosterv1.ConnectOutlineWorkspaceResponse{KeyUser: keyUser, KeyUserIgnored: ignored}, nil
}

// DisconnectOutlineWorkspace implements the operator contract.
func (c *Console) DisconnectOutlineWorkspace(
	ctx context.Context, req *directoryrosterv1.DisconnectOutlineWorkspaceRequest,
) (*directoryrosterv1.DisconnectOutlineWorkspaceResponse, error) {
	if _, err := requireRole(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	store, err := c.outlineStore()
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetWorkspace())
	previous, found, err := store.Record(ctx, key)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the workspace's connection could not be read").WithCause(err)
	}
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, "the Outline workspace "+key+" is not connected")
	}
	if err = store.Delete(ctx, key); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the connection could not be removed").WithCause(err)
	}
	c.record(ctx, audit.OutlineWorkspaceDisconnected(actorOf(ctx),
		audit.OutlineWorkspace{Key: key, URL: previous.URL, KeyUser: previous.KeyUser, Team: previous.Team}))
	return &directoryrosterv1.DisconnectOutlineWorkspaceResponse{}, nil
}

// RequestOutlinePass implements the operator contract.
func (c *Console) RequestOutlinePass(
	ctx context.Context, req *directoryrosterv1.RequestOutlinePassRequest,
) (*directoryrosterv1.RequestOutlinePassResponse, error) {
	id, err := requireRole(ctx, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	store, err := c.outlineStore()
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetWorkspace())
	if err = c.outlineDeclared(key); err != nil {
		return nil, err
	}
	kept, last, err := store.RequestPass(ctx, connection.PassRequest{Workspace: key, By: id.Who(), At: time.Now().UTC()})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the request could not be kept").WithCause(err)
	}
	out := &directoryrosterv1.RequestOutlinePassResponse{Kept: kept}
	if !last.IsZero() {
		out.Last = timestamppb.New(last)
	}
	return out, nil
}

// ConfirmOutlineBreaker implements the operator contract. Only the latest
// report's fingerprint for the scope is accepted: a set confirmed is exactly
// the set the operator saw.
func (c *Console) ConfirmOutlineBreaker(
	ctx context.Context, req *directoryrosterv1.ConfirmOutlineBreakerRequest,
) (*directoryrosterv1.ConfirmOutlineBreakerResponse, error) {
	id, err := requireRole(ctx, access.RoleOperator)
	if err != nil {
		return nil, err
	}
	store, err := c.outlineStore()
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetWorkspace())
	report, _, ok := c.outlineReport(ctx, key)
	var breaker *status.Breaker
	for i := range report.Breakers {
		if report.Breakers[i].Scope == req.GetScope() && report.Breakers[i].Fingerprint == req.GetFingerprint() {
			breaker = &report.Breakers[i]
		}
	}
	if !ok || breaker == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "the latest report holds no such set; reload and confirm what it shows")
	}
	err = store.PutConfirmation(ctx, connection.Confirmation{
		Workspace: key, Scope: breaker.Scope, Fingerprint: breaker.Fingerprint, By: id.Who(), At: time.Now().UTC(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the confirmation could not be kept").WithCause(err)
	}
	c.record(ctx, audit.OutlineBreakerConfirmed(actorOf(ctx), audit.OutlineBreaker{
		Workspace: key, Scope: breaker.Scope, Fingerprint: breaker.Fingerprint, Affected: breaker.Affected, Total: breaker.Total,
	}))
	return &directoryrosterv1.ConfirmOutlineBreakerResponse{}, nil
}

// ControllerAccess is the console's AccessService as a controller in this very
// process asks it: as the roster controllers' workload, which may ask Explain
// in bulk unrecorded, and read the served domains and directory groups as a
// viewer. It goes through the same handlers, policy and directory a controller
// in another process reaches over the network, without a token: the caller is
// this process's own code (docs/decisions/0077).
func (s *ConsoleServer) ControllerAccess(name string) *ControllerAccess {
	return &ControllerAccess{console: s.console, id: access.Identity{
		Subject: "sluis:controller:" + name, Source: access.SourceWorkload, Role: access.RoleViewer,
		Groups: []string{policy.GroupRosterControllers},
	}}
}

// Recorder is the console's audit trail, which a controller in this process
// records to as well.
func (s *ConsoleServer) Recorder() audit.Recorder { return s.console.deps.Audit }

// ControllerAccess is [ConsoleServer.ControllerAccess].
type ControllerAccess struct {
	console *Console
	id      access.Identity
}

// Explain implements the question a controller asks about one address.
func (a *ControllerAccess) Explain(ctx context.Context, req *directoryrosterv1.ExplainRequest) (*directoryrosterv1.ExplainResponse, error) {
	return a.console.Explain(WithIdentity(ctx, a.id), req)
}

// ListServedDomains implements the question of which domains each directory
// serves.
func (a *ControllerAccess) ListServedDomains(
	ctx context.Context, req *directoryrosterv1.ListServedDomainsRequest,
) (*directoryrosterv1.ListServedDomainsResponse, error) {
	return a.console.ListServedDomains(WithIdentity(ctx, a.id), req)
}

// ResolveDirectoryGroups implements the question of whose directory groups are.
func (a *ControllerAccess) ResolveDirectoryGroups(
	ctx context.Context, req *directoryrosterv1.ResolveDirectoryGroupsRequest,
) (*directoryrosterv1.ResolveDirectoryGroupsResponse, error) {
	return a.console.ResolveDirectoryGroups(WithIdentity(ctx, a.id), req)
}
