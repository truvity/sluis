package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/truvity/sluis/audit/sdk/record"
	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/storage/logattr"
)

// clientCheckTimeout bounds the pre-flight check of the OAuth client. It is on
// the operator's request path, so it is short: an answer that has not arrived
// by now is inconclusive, and inconclusive proceeds.
const clientCheckTimeout = 5 * time.Second

// Service is the Workspace service on the module's side: the hub that holds the
// workspaces, the connectors that open them, and the trail it records what it
// did to. It acts on the caller snapshot of the console, never on a session.
type Service struct {
	hub        *hub.Hub
	connectors map[string]Connector
	audit      audit.Recorder
	log        *slog.Logger
}

// NewService is the Workspace service over a hub and its connectors. A nil
// recorder records nothing, a nil logger is the default one.
func NewService(h *hub.Hub, connectors []Connector, rec audit.Recorder, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{hub: h, connectors: map[string]Connector{}, audit: rec, log: log}
	for _, c := range connectors {
		s.connectors[c.Kind()] = c
	}
	return s
}

func (s *Service) record(ctx context.Context, r *record.Record) {
	if s.audit != nil {
		s.audit.Record(ctx, r)
	}
}

// actor is whoever the snapshot names, as the trail names them.
func actor(c consolecall.Caller) audit.Actor { return audit.Identified(c.Actor) }

// gate turns what the authorization rules said into the uniform refusal: the
// same fixed words whatever was missing, and the reason in the module's log. A
// refusal that said which role was lacking, or over which directory, would tell
// a caller what access exists.
func (s *Service) gate(ctx context.Context, method string, c consolecall.Caller, err error) error {
	var coded *modcall.Error
	if !errors.As(err, &coded) {
		return err
	}
	switch coded.Code {
	case modcall.CodeForbidden:
		s.log.WarnContext(ctx, "a console call was refused",
			logattr.SafeString("method", method), logattr.Pseudonym("actor", c.Actor), logattr.SafeString("reason", coded.Message))
		return modcall.Coded(modcall.CodeForbidden, RefusalText)
	case CodeUnauthenticated:
		return modcall.Coded(CodeUnauthenticated, "sign in first")
	}
	return err
}

// fail is a hub error as the code a person is told: the ones that are the
// request's fault by name, every other as the module's own, with no detail.
func fail(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, hub.ErrInvalidAddress), errors.Is(err, hub.ErrUnknownDomain), errors.Is(err, hub.ErrUnknownGroup):
		return modcall.Coded(CodeInvalidArgument, err.Error())
	case errors.Is(err, hub.ErrNotFound):
		return modcall.Coded(CodeNotFound, err.Error())
	case errors.Is(err, hub.ErrDeclared):
		return modcall.Coded(CodeFailedPrecondition, err.Error())
	}
	// The caller is told nothing of the cause; the module's server logs it with
	// the request id (modcall.Server.SetLogger).
	return modcall.CodedCause(modcall.CodeInternal, "", err)
}

func (s *Service) view(ctx context.Context, id string) (*directoryrosterv1.Workspace, error) {
	views, err := s.hub.WorkspaceViews(ctx)
	if err != nil {
		return nil, fail(err)
	}
	for i := range views {
		if views[i].Workspace.ID == id {
			return WorkspaceProto(&views[i]), nil
		}
	}
	return nil, modcall.Coded(CodeNotFound, fmt.Sprintf("%s: %s", hub.ErrNotFound, id))
}

// ListWorkspaces lists the workspaces the caller may see.
func (s *Service) ListWorkspaces(
	ctx context.Context, c consolecall.Caller, _ *directoryrosterv1.ListWorkspacesRequest,
) (*directoryrosterv1.ListWorkspacesResponse, error) {
	id, err := consoleauthz.RequireAnywhere(c, access.RoleViewer)
	if err != nil {
		return nil, s.gate(ctx, MethodListWorkspaces, c, err)
	}
	views, err := s.hub.WorkspaceViews(ctx)
	if err != nil {
		return nil, fail(err)
	}
	visible := id.Workspaces(access.RoleViewer)
	out := &directoryrosterv1.ListWorkspacesResponse{Workspaces: make([]*directoryrosterv1.Workspace, 0, len(views))}
	for i := range views {
		// A nil list is every workspace: an installation-wide role is not
		// a list of tenants and must not be turned into one.
		if visible != nil && !slices.Contains(visible, views[i].Workspace.ID) {
			continue
		}
		out.Workspaces = append(out.Workspaces, WorkspaceProto(&views[i]))
	}
	return out, nil
}

// BeginConnect returns where the browser goes to consent to a new connection.
func (s *Service) BeginConnect(ctx context.Context, c consolecall.Caller, req BeginRequest) (BeginResponse, error) {
	return s.begin(ctx, MethodBeginConnect, c, req.Backend, "", req.State)
}

// Reconnect is the same flow, bound to an existing workspace.
func (s *Service) Reconnect(ctx context.Context, c consolecall.Caller, req BeginRequest) (BeginResponse, error) {
	if _, err := consoleauthz.RequireWorkspace(c, access.RoleOperator, req.Bind); err != nil {
		return BeginResponse{}, s.gate(ctx, MethodReconnect, c, err)
	}
	views, err := s.hub.WorkspaceViews(ctx)
	if err != nil {
		return BeginResponse{}, fail(err)
	}
	kind := ""
	for i := range views {
		if views[i].Workspace.ID == req.Bind {
			kind = views[i].Workspace.Backend
		}
	}
	if kind == "" {
		return BeginResponse{}, modcall.Coded(CodeNotFound, fmt.Sprintf("%s: %s", hub.ErrNotFound, req.Bind))
	}
	return s.begin(ctx, MethodReconnect, c, kind, req.Bind, req.State)
}

func (s *Service) begin(
	ctx context.Context, method string, c consolecall.Caller, kind, _, state string,
) (BeginResponse, error) {
	if _, err := consoleauthz.RequireRole(c, access.RoleOperator); err != nil {
		return BeginResponse{}, s.gate(ctx, method, c, err)
	}
	if state == "" {
		return BeginResponse{}, modcall.Coded(CodeInvalidArgument, "the consent needs a state")
	}
	conn, ok := s.connectors[kind]
	if !ok {
		return BeginResponse{}, modcall.Coded(CodeFailedPrecondition,
			"no connector for that backend: configure the OAuth client in Settings first")
	}
	// Check the client before sending anyone to consent with it. A wrong
	// secret fails at the exchange, which is the step AFTER the consent
	// screen — so without this a real Super Admin grants a real
	// credential to an installation that cannot collect it, and has to be
	// asked to do it again. The check refuses only when the provider
	// names the client as the problem; anything inconclusive proceeds.
	if verifier, checkable := conn.(ClientVerifier); checkable {
		checking, done := context.WithTimeout(ctx, clientCheckTimeout)
		err := verifier.VerifyClient(checking)
		done()
		if err != nil {
			return BeginResponse{}, modcall.Coded(CodeFailedPrecondition, err.Error())
		}
	}
	url, err := conn.AuthURL(state)
	if err != nil {
		return BeginResponse{}, modcall.Coded(CodeFailedPrecondition, err.Error())
	}
	return BeginResponse{URL: url}, nil
}

// CompleteConsent finishes a consent: it exchanges the provider's code, checks
// it is the tenant being reconnected if one is, and adopts the workspace. The
// code is single-use, so the issuer never retries this call. What a person
// should read when it fails is a Problem, not an error.
func (s *Service) CompleteConsent(ctx context.Context, c consolecall.Caller, req CompleteRequest) (CompleteResponse, error) {
	if _, err := consoleauthz.RequireRole(c, access.RoleOperator); err != nil {
		return CompleteResponse{}, s.gate(ctx, MethodCompleteConsent, c, err)
	}
	conn, ok := s.connectors[req.Backend]
	if !ok {
		return CompleteResponse{}, &consolecall.Problem{Summary: "This hub has no connector for that directory."}
	}
	ws, b, err := conn.Exchange(ctx, req.Code, req.Bind)
	if err != nil {
		s.log.WarnContext(ctx, "consent exchange failed",
			logattr.SafeString("backend", conn.Kind()), logattr.SafeError("error", err))
		return CompleteResponse{}, &consolecall.Problem{
			Summary: ProviderName(conn.Kind()) + " granted the consent, and then refused the first read with it.",
			Detail:  err.Error(), Hints: ConsentCauses,
		}
	}
	if req.Bind != "" && ws.ID != req.Bind {
		return CompleteResponse{}, &consolecall.Problem{
			Summary: "That consent is for a different tenant than the workspace being reconnected.",
			Hints: []string{
				"The account chooser offered the account last used rather than the one this " +
					"workspace belongs to.",
			},
		}
	}
	ws.ConnectedBy = c.Actor
	if _, err = s.hub.Adopt(ctx, ws, b); err != nil {
		s.log.ErrorContext(ctx, "the workspace could not be adopted",
			logattr.SafeString("workspace", ws.ID), logattr.SafeError("error", err))
		return CompleteResponse{}, &consolecall.Problem{
			Summary: "The consent worked, but the workspace could not be saved.", Detail: err.Error(),
		}
	}
	connected := audit.WorkspaceConnected
	if req.Bind != "" {
		connected = audit.WorkspaceReconnected
	}
	s.record(ctx, connected(actor(c), ws.ID, b.Kind(), "consent"))
	s.log.InfoContext(ctx, "workspace connected",
		logattr.SafeString("workspace", ws.ID), logattr.SafeString("backend", b.Kind()), logattr.Pseudonym("by", c.Actor))
	return CompleteResponse{Workspace: ws.ID, Backend: b.Kind()}, nil
}

// UploadKey connects a workspace with a service-account key. The key is in
// this call's memory and the credential store's, nowhere else: it is never
// logged, and a failure names the cause without it.
func (s *Service) UploadKey(
	ctx context.Context, c consolecall.Caller, req *directoryrosterv1.UploadKeyRequest,
) (*directoryrosterv1.UploadKeyResponse, error) {
	if _, err := consoleauthz.RequireRole(c, access.RoleOperator); err != nil {
		return nil, s.gate(ctx, MethodUploadKey, c, err)
	}
	conn, ok := s.connectors[BackendKind(req.GetBackend())]
	if !ok {
		return nil, modcall.Coded(CodeFailedPrecondition, "no connector for that backend")
	}
	keyed, ok := conn.(KeyConnector)
	if !ok {
		return nil, modcall.Coded(CodeUnimplemented, fmt.Sprintf("the %s backend takes no uploaded key", conn.Kind()))
	}
	admin := strings.TrimSpace(req.GetAdmin())
	if admin == "" || len(req.GetKey()) == 0 {
		return nil, modcall.Coded(CodeInvalidArgument, "both the key and the admin are required")
	}
	if len(req.GetKey()) > MaxKeyBytes {
		return nil, modcall.Coded(CodeInvalidArgument, fmt.Sprintf("a key is at most %d KB", MaxKeyBytes>>10))
	}
	ws, b, err := keyed.FromKey(ctx, req.GetKey(), admin)
	if err != nil {
		return nil, modcall.Coded(CodeInvalidArgument, err.Error())
	}
	ws.ConnectedBy = c.Actor
	if _, err = s.hub.Adopt(ctx, ws, b); err != nil {
		return nil, fail(err)
	}
	s.record(ctx, audit.WorkspaceConnected(actor(c), ws.ID, b.Kind(), "key"))
	adopted, err := s.view(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	return &directoryrosterv1.UploadKeyResponse{Workspace: adopted}, nil
}

// SetServedDomains sets which of a tenant's domains this hub answers for.
func (s *Service) SetServedDomains(
	ctx context.Context, c consolecall.Caller, req *directoryrosterv1.SetServedDomainsRequest,
) (*directoryrosterv1.SetServedDomainsResponse, error) {
	id := req.GetWorkspaceId()
	if _, err := consoleauthz.RequireWorkspace(c, access.RoleOperator, id); err != nil {
		return nil, s.gate(ctx, MethodSetServedDomains, c, err)
	}
	if _, err := s.hub.SetServed(ctx, id, req.GetDomains()); err != nil {
		return nil, fail(err)
	}
	s.record(ctx, audit.WorkspaceDomainsChanged(actor(c), id, req.GetDomains()))
	w, err := s.view(ctx, id)
	if err != nil {
		return nil, err
	}
	return &directoryrosterv1.SetServedDomainsResponse{Workspace: w}, nil
}

// SetSyncedGroups sets which of a tenant's groups this hub keeps.
func (s *Service) SetSyncedGroups(
	ctx context.Context, c consolecall.Caller, req *directoryrosterv1.SetSyncedGroupsRequest,
) (*directoryrosterv1.SetSyncedGroupsResponse, error) {
	id := req.GetWorkspaceId()
	if _, err := consoleauthz.RequireWorkspace(c, access.RoleOperator, id); err != nil {
		return nil, s.gate(ctx, MethodSetSyncedGroups, c, err)
	}
	if _, err := s.hub.SetSynced(ctx, id, req.GetGroups()); err != nil {
		return nil, fail(err)
	}
	s.record(ctx, audit.WorkspaceGroupsChanged(actor(c), id, len(req.GetGroups())))
	w, err := s.view(ctx, id)
	if err != nil {
		return nil, err
	}
	return &directoryrosterv1.SetSyncedGroupsResponse{Workspace: w}, nil
}

// Probe checks a workspace's credential now.
func (s *Service) Probe(
	ctx context.Context, c consolecall.Caller, req *directoryrosterv1.ProbeRequest,
) (*directoryrosterv1.ProbeResponse, error) {
	id := req.GetWorkspaceId()
	if _, err := consoleauthz.RequireWorkspace(c, access.RoleOperator, id); err != nil {
		return nil, s.gate(ctx, MethodProbe, c, err)
	}
	healths, err := s.hub.Probe(ctx, id)
	if err != nil {
		return nil, fail(err)
	}
	out := &directoryrosterv1.ProbeResponse{}
	if len(healths) > 0 {
		out.Health = &directoryrosterv1.Health{
			ProbedAt: Stamp(healths[0].ProbedAt), Ok: healths[0].OK, Error: healths[0].Detail,
		}
	}
	views, err := s.hub.WorkspaceViews(ctx)
	if err != nil {
		return nil, fail(err)
	}
	for i := range views {
		if views[i].Workspace.ID == id {
			out.Domains = DomainsProto(views[i].Domains)
		}
	}
	return out, nil
}

// Refresh reads a workspace's directory now, whatever the age of its snapshot.
func (s *Service) Refresh(
	ctx context.Context, c consolecall.Caller, req *directoryrosterv1.RefreshRequest,
) (*directoryrosterv1.RefreshResponse, error) {
	if _, err := consoleauthz.RequireWorkspace(c, access.RoleOperator, req.GetWorkspaceId()); err != nil {
		return nil, s.gate(ctx, MethodRefresh, c, err)
	}
	at, err := s.hub.Refresh(ctx, req.GetWorkspaceId())
	if err != nil {
		return nil, fail(err)
	}
	return &directoryrosterv1.RefreshResponse{SnapshotAt: Stamp(at)}, nil
}

// Disconnect removes a workspace.
func (s *Service) Disconnect(
	ctx context.Context, c consolecall.Caller, req *directoryrosterv1.DisconnectRequest,
) (*directoryrosterv1.DisconnectResponse, error) {
	if _, err := consoleauthz.RequireWorkspace(c, access.RoleOperator, req.GetWorkspaceId()); err != nil {
		return nil, s.gate(ctx, MethodDisconnect, c, err)
	}
	if err := s.hub.Disconnect(ctx, req.GetWorkspaceId()); err != nil {
		return nil, fail(err)
	}
	s.record(ctx, audit.WorkspaceDisconnected(actor(c), req.GetWorkspaceId()))
	return &directoryrosterv1.DisconnectResponse{}, nil
}

// signIn is the connector that signs somebody in, if there is one.
func (s *Service) signIn(kind string) (SignInConnector, bool) {
	conn, ok := s.connectors[kind]
	if !ok {
		return nil, false
	}
	signIn, ok := conn.(SignInConnector)
	return signIn, ok
}

// Identify turns the sign-in's code into the address that authenticated. It
// answers the issuer alone ([Register]); what that address may do is decided
// by the directory and the policy, not here.
func (s *Service) Identify(ctx context.Context, code string) (string, error) {
	conn, ok := s.signIn("google")
	if !ok {
		return "", modcall.Coded(CodeFailedPrecondition, "this module cannot sign anyone in")
	}
	return conn.Identify(ctx, code)
}

// SignInURL is where the browser goes to sign in.
func (s *Service) SignInURL(state string) (string, error) {
	conn, ok := s.signIn("google")
	if !ok {
		return "", modcall.Coded(CodeFailedPrecondition, "this module cannot sign anyone in")
	}
	return conn.SignInURL(state)
}

// Register makes the service answer on s: the console methods to the caller
// class `console` alone, and the two the issuer calls to the class `issuer`
// alone. A method that failed with something the person should read keeps its
// words; any other failure is the transport's, with no detail.
func Register(s *modcall.Server, svc *Service) {
	RegisterLazy(s, svc.log, func(context.Context) (*Service, error) { return svc, nil })
}

// RegisterLazy is [Register] for a function that opens the service when the
// first call arrives: a function whose other invocations (a scheduled refresh)
// do not need the hub, the connectors or the secrets this assembles. An open
// that fails is told as the module being unavailable, and tried again by the
// next call. log is where a failed open is written.
func RegisterLazy(s *modcall.Server, log *slog.Logger, open func(context.Context) (*Service, error)) {
	if log == nil {
		log = slog.Default()
	}
	get := func(ctx context.Context) (*Service, error) {
		svc, err := open(ctx)
		if err != nil {
			log.ErrorContext(ctx, "the Google module's service could not be opened", logattr.SafeError("error", err))
			return nil, modcall.Coded(modcall.CodeUnavailable, "")
		}
		return svc, nil
	}
	// proto registers a method of the contract's messages.
	consolecall.Handle(s, MethodListWorkspaces, func(
		ctx context.Context, c consolecall.Caller, r *directoryrosterv1.ListWorkspacesRequest,
	) (*directoryrosterv1.ListWorkspacesResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return nil, err
		}
		return svc.ListWorkspaces(ctx, c, r)
	}, consolecall.List())
	handleJSON(s, MethodBeginConnect, func(ctx context.Context, c consolecall.Caller, r BeginRequest) (BeginResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return BeginResponse{}, err
		}
		return svc.BeginConnect(ctx, c, r)
	})
	handleJSON(s, MethodReconnect, func(ctx context.Context, c consolecall.Caller, r BeginRequest) (BeginResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return BeginResponse{}, err
		}
		return svc.Reconnect(ctx, c, r)
	})
	// The code is spent by the exchange; the time Google and the directory
	// take is the page's wait, so the deadline is the caller's, about 25 s.
	handleJSON(s, MethodCompleteConsent, func(ctx context.Context, c consolecall.Caller, r CompleteRequest) (CompleteResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return CompleteResponse{}, err
		}
		return svc.CompleteConsent(ctx, c, r)
	})
	consolecall.Handle(s, MethodUploadKey, func(
		ctx context.Context, c consolecall.Caller, r *directoryrosterv1.UploadKeyRequest,
	) (*directoryrosterv1.UploadKeyResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return nil, err
		}
		return svc.UploadKey(ctx, c, r)
	})
	consolecall.Handle(s, MethodSetServedDomains, func(
		ctx context.Context, c consolecall.Caller, r *directoryrosterv1.SetServedDomainsRequest,
	) (*directoryrosterv1.SetServedDomainsResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return nil, err
		}
		return svc.SetServedDomains(ctx, c, r)
	})
	consolecall.Handle(s, MethodSetSyncedGroups, func(
		ctx context.Context, c consolecall.Caller, r *directoryrosterv1.SetSyncedGroupsRequest,
	) (*directoryrosterv1.SetSyncedGroupsResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return nil, err
		}
		return svc.SetSyncedGroups(ctx, c, r)
	})
	consolecall.Handle(s, MethodProbe, func(
		ctx context.Context, c consolecall.Caller, r *directoryrosterv1.ProbeRequest,
	) (*directoryrosterv1.ProbeResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return nil, err
		}
		return svc.Probe(ctx, c, r)
	})
	consolecall.Handle(s, MethodRefresh, func(
		ctx context.Context, c consolecall.Caller, r *directoryrosterv1.RefreshRequest,
	) (*directoryrosterv1.RefreshResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return nil, err
		}
		return svc.Refresh(ctx, c, r)
	})
	consolecall.Handle(s, MethodDisconnect, func(
		ctx context.Context, c consolecall.Caller, r *directoryrosterv1.DisconnectRequest,
	) (*directoryrosterv1.DisconnectResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return nil, err
		}
		return svc.Disconnect(ctx, c, r)
	})

	issuer := modcall.Allow(CallerIssuer)
	modcall.Handle(s, MethodIdentify, func(ctx context.Context, r identifyRequest) (identifyResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return identifyResponse{}, err
		}
		email, err := svc.Identify(ctx, r.Code)
		if err != nil {
			// The provider's words are the module's log's: a sign-in that failed
			// says so, and why is not the issuer's to relay to a browser.
			svc.log.WarnContext(ctx, "sign-in exchange failed", logattr.SafeError("error", err))
			return identifyResponse{}, modcall.Coded(CodeFailedPrecondition, "")
		}
		return identifyResponse{Email: email}, nil
	}, issuer)
	modcall.Handle(s, MethodSignInURL, func(ctx context.Context, r signInURLRequest) (signInURLResponse, error) {
		svc, err := get(ctx)
		if err != nil {
			return signInURLResponse{}, err
		}
		url, err := svc.SignInURL(r.State)
		if err != nil {
			return signInURLResponse{}, modcall.Coded(CodeFailedPrecondition, err.Error())
		}
		return signInURLResponse{URL: url}, nil
	}, issuer)
}

// Server is a module server with the service registered on it.
func (s *Service) Server() *modcall.Server {
	srv := modcall.NewServer(Module)
	Register(srv, s)
	return srv
}
