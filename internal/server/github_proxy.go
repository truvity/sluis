package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
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
	return c.snapshot(id), id, nil
}

// snapshot is the caller of id, and where the console is. The connected
// directories the module chooses an owner among travel beside it, as the call's
// directory facts ([Console.githubFacts]).
func (c *Console) snapshot(id access.Identity) consolecall.Caller {
	caller := consolecall.Snapshot(id)
	caller.Origin = c.githubRoot()
	return caller
}

// githubFacts is what the console resolves of the directories for one call to
// the GitHub module, which reads none: the connected directories, and for the
// addresses the call names what the directory says of each. Only the console
// resolves them, and only what the call asks. Nil is a deployment that reads no
// directory.
func (c *Console) githubFacts(ctx context.Context, addresses []string) *consolecall.DirectoryFacts {
	if c.deps.Hub == nil && c.deps.Authorizer == nil {
		return nil
	}
	facts := &consolecall.DirectoryFacts{}
	c.directoriesInto(ctx, facts)
	if len(addresses) > 0 && c.deps.Authorizer != nil {
		facts.Users = map[string]consolecall.UserFact{}
		for _, address := range addresses {
			if len(facts.Users) >= consolecall.MaxAddressFacts {
				break
			}
			if _, done := facts.Users[address]; done {
				continue
			}
			// The authorizer's reading of the directory, as the console itself uses
			// it. The module has no use for which directory holds the account.
			explained, err := c.deps.Authorizer.Explain(ctx, access.Proof{Email: address})
			if err != nil {
				facts.Users[address] = consolecall.UserFact{Err: err.Error()}
				continue
			}
			facts.Users[address] = consolecall.UserFact{
				InDomain: explained.InDomain, Authoritative: explained.Authoritative, Found: explained.Found, Suspended: explained.Suspended,
			}
		}
	}
	return facts
}

// githubImportAddresses are the addresses an import's records name, lowercased.
func githubImportAddresses(req proto.Message) []string {
	r, ok := req.(*directoryrosterv1.ImportGitHubLinksRequest)
	if !ok {
		return nil
	}
	var out []string
	for _, record := range r.GetRecords() {
		for _, a := range record.GetEmails() {
			if a = strings.ToLower(strings.TrimSpace(a)); a != "" && !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
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
	ctx = consolecall.WithFacts(ctx, c.githubFacts(ctx, githubImportAddresses(PReq(req.Msg))))
	out, err := consolecall.Proxy[PReq, PResp](ctx, c.githubModule(), consolewire.Module, method, caller, PReq(req.Msg))
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

// signBegun signs the state a module's begin names and puts it in the URL.
func (c *Console) signBegun(id access.Identity, begun consolewire.Begun) (githubBegin, error) {
	state, err := c.deps.State.IssueAs(access.Binding{Bind: begun.Bind, Actor: id.Who(), Owner: begun.Owner})
	if err != nil {
		return githubBegin{}, connect.NewError(connect.CodeInternal, err)
	}
	return githubBegin{url: githubWithState(begun.URL, state), manifest: begun.Manifest, state: state}, nil
}

// DisconnectGitHubOrganisation revokes, then forgets: in the module.
func (c *Console) DisconnectGitHubOrganisation(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubOrganisationRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubOrganisationResponse], error) {
	return githubProto[directoryrosterv1.DisconnectGitHubOrganisationRequest, directoryrosterv1.DisconnectGitHubOrganisationResponse](
		ctx, c, consolewire.DisconnectOrganisation, access.RoleOperator, req)
}

// ChangeGitHubOrganisationOwner changes the directory recorded as a connected
// organisation's owner, or removes it: in the module.
func (c *Console) ChangeGitHubOrganisationOwner(
	ctx context.Context, req *connect.Request[directoryrosterv1.ChangeGitHubOrganisationOwnerRequest],
) (*connect.Response[directoryrosterv1.ChangeGitHubOrganisationOwnerResponse], error) {
	return githubProto[directoryrosterv1.ChangeGitHubOrganisationOwnerRequest, directoryrosterv1.ChangeGitHubOrganisationOwnerResponse](
		ctx, c, consolewire.ChangeOrganisationOwner, access.RoleOperator, req)
}

// RequestGitHubPass asks the GitHub controller to pass over now: in the module,
// which records the request and ticks itself.
func (c *Console) RequestGitHubPass(
	ctx context.Context, req *connect.Request[directoryrosterv1.RequestGitHubPassRequest],
) (*connect.Response[directoryrosterv1.RequestGitHubPassResponse], error) {
	return githubProto[directoryrosterv1.RequestGitHubPassRequest, directoryrosterv1.RequestGitHubPassResponse](
		ctx, c, consolewire.RequestPass, access.RoleOperator, req)
}

// UseGitHub connects the console's GitHub operations to the module in another
// process, with the same timing as [ConsoleServer.UseCloudflare]. Without it
// the module is served in this process.
func (s *ConsoleServer) UseGitHub(c modcall.Caller) {
	s.console.deps.Modules.GitHub = c
}

// githubModule is the caller of the GitHub module: the other process the
// deployment dialled, else the module in this one, built over the console's own
// stores when first asked.
func (c *Console) githubModule() modcall.Caller {
	if c.deps.Modules.GitHub != nil {
		return c.deps.Modules.GitHub
	}
	c.githubOnce.Do(func() {
		c.github = ghconsole.New(c.githubDeps())
		srv := modcall.NewServer(consolewire.Module)
		ghconsole.Register(srv, c.github)
		c.githubLocal = modcall.Local{consolewire.Module: srv}.As(consolecall.Class)
	})
	return c.githubLocal
}

// githubDeps is what the module in this process acts on: the console's stores.
func (c *Console) githubDeps() ghconsole.Deps {
	deps := ghconsole.Deps{
		Log:        c.deps.Log,
		Audit:      c.deps.Audit,
		Trigger:    c.deps.Trigger,
		PublicURL:  c.deps.PublicURL,
		RootURL:    c.deps.RootURL,
		Orgs:       c.deps.GitHubOrgs,
		OrgAppName: c.deps.GitHubOrgAppName,
		Catalogue:  c.deps.GitHubCatalogue,
		HTTP:       c.deps.GitHubHTTP,

		Reports:           c.deps.GitHub,
		Confirmations:     c.deps.GitHubConfirmations,
		LinkApp:           c.deps.GitHubLinkApp,
		Links:             c.deps.GitHubLinks,
		LinkAppName:       c.deps.GitHubLinkAppName,
		RunnerApps:        c.deps.GitHubRunnerApps,
		RunnerTiers:       c.deps.GitHubRunnerTiers,
		CatalogueApps:     c.deps.GitHubCatalogueApps,
		Mints:             c.deps.GitHubMints,
		WebhookHTTP:       c.deps.WebhookHTTP,
		WebhookPingWindow: c.deps.WebhookPingWindow,
		WebhookPingEvery:  c.deps.WebhookPingEvery,
	}
	if c.deps.Authorizer != nil {
		deps.Policy = c.deps.Authorizer.Policy
	}
	return deps
}

// The Apps view, an App's webhook and the GitHub page are the module's too: it
// holds the keys, the webhook secrets and the connection records they read.

// ListGitHubApps lists every App in one shape.
func (c *Console) ListGitHubApps(
	ctx context.Context, req *connect.Request[directoryrosterv1.ListGitHubAppsRequest],
) (*connect.Response[directoryrosterv1.ListGitHubAppsResponse], error) {
	return githubProto[directoryrosterv1.ListGitHubAppsRequest, directoryrosterv1.ListGitHubAppsResponse](ctx, c, consolewire.ListApps, access.RoleViewer, req)
}

// GetGitHubApp reads one App.
func (c *Console) GetGitHubApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.GetGitHubAppRequest],
) (*connect.Response[directoryrosterv1.GetGitHubAppResponse], error) {
	return githubProto[directoryrosterv1.GetGitHubAppRequest, directoryrosterv1.GetGitHubAppResponse](ctx, c, consolewire.GetApp, access.RoleViewer, req)
}

// githubLoginsRemote is [Console.githubLogins] for a GitHub module in another
// process: the links are its records, so it indexes them.
func (c *Console) githubLoginsRemote(ctx context.Context) (map[string]string, bool, error) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return nil, false, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in first"))
	}
	ctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	ctx = consolecall.WithFacts(ctx, c.githubFacts(ctx, nil))
	out, err := consolecall.ProxyJSON[struct{}, consolewire.LinkLogins](
		ctx, c.githubModule(), consolewire.Module, consolewire.ListLinkLogins, c.snapshot(id), struct{}{})
	if err != nil {
		return nil, false, githubError(err)
	}
	if !out.Known {
		return nil, false, nil
	}
	return out.Logins, true, nil
}

// ListGitHubAppTokens lists the last installation tokens asked of an App.
//
// The ring of them is written where the decision to mint is made, which is the
// issuer, and read here. A GitHub module in another process mints but keeps no
// ring (the issuer asks it for each token and records the request itself), so
// there is nothing to forward the question to: the module is asked only which
// App this is and whether the person may operate it, and the ring answers.
func (c *Console) ListGitHubAppTokens(
	ctx context.Context, req *connect.Request[directoryrosterv1.ListGitHubAppTokensRequest],
) (*connect.Response[directoryrosterv1.ListGitHubAppTokensResponse], error) {
	if c.deps.Modules.GitHub != nil {
		return c.listGitHubAppTokensRemote(ctx, req.Msg.GetId())
	}
	return githubProto[directoryrosterv1.ListGitHubAppTokensRequest, directoryrosterv1.ListGitHubAppTokensResponse](
		ctx, c, consolewire.ListAppTokens, access.RoleOperator, req)
}

func (c *Console) listGitHubAppTokensRemote(
	ctx context.Context, id string,
) (*connect.Response[directoryrosterv1.ListGitHubAppTokensResponse], error) {
	// Operator, not viewer: a request names who asked for it.
	if _, err := requireAnywhere(ctx, access.RoleOperator); err != nil {
		return nil, err
	}
	got, err := githubProto[directoryrosterv1.GetGitHubAppRequest, directoryrosterv1.GetGitHubAppResponse](
		ctx, c, consolewire.GetApp, access.RoleViewer, connect.NewRequest(&directoryrosterv1.GetGitHubAppRequest{Id: id}))
	if err != nil {
		return nil, err
	}
	app := got.Msg.GetApp()
	switch {
	case app.GetPurpose() != directoryrosterv1.AppPurpose_APP_PURPOSE_TOKENS:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%s mints no installation tokens: only an App the catalogue declares does", app.GetId()))
	case !app.GetCanOperate():
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("this needs the operator role over the App's organisation"))
	case c.deps.GitHubMints == nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(
			"this deployment mints no installation tokens here, so it keeps none to show"))
	}
	return connect.NewResponse(ghconsole.TokensOf(c.deps.GitHubMints, app.GetId())), nil
}

// CheckGitHubApp asks GitHub again about one App, whichever kind it is.
func (c *Console) CheckGitHubApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.CheckGitHubAppRequest],
) (*connect.Response[directoryrosterv1.CheckGitHubAppResponse], error) {
	return githubProto[directoryrosterv1.CheckGitHubAppRequest, directoryrosterv1.CheckGitHubAppResponse](ctx, c, consolewire.CheckApp, access.RoleOperator, req)
}

// RotateGitHubAppWebhook gives an App's webhook a new secret.
func (c *Console) RotateGitHubAppWebhook(
	ctx context.Context, req *connect.Request[directoryrosterv1.RotateGitHubAppWebhookRequest],
) (*connect.Response[directoryrosterv1.RotateGitHubAppWebhookResponse], error) {
	return githubProto[directoryrosterv1.RotateGitHubAppWebhookRequest, directoryrosterv1.RotateGitHubAppWebhookResponse](
		ctx, c, consolewire.RotateAppWebhook, access.RoleOperator, req)
}

// GetGitHubStatus implements the GitHub page.
func (c *Console) GetGitHubStatus(
	ctx context.Context, req *connect.Request[directoryrosterv1.GetGitHubStatusRequest],
) (*connect.Response[directoryrosterv1.GetGitHubStatusResponse], error) {
	return githubProto[directoryrosterv1.GetGitHubStatusRequest, directoryrosterv1.GetGitHubStatusResponse](ctx, c, consolewire.GetStatus, access.RoleViewer, req)
}

// CheckGitHubCatalogueApp asks GitHub again about one catalogue App.
func (c *Console) CheckGitHubCatalogueApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.CheckGitHubCatalogueAppRequest],
) (*connect.Response[directoryrosterv1.CheckGitHubCatalogueAppResponse], error) {
	return githubProto[directoryrosterv1.CheckGitHubCatalogueAppRequest, directoryrosterv1.CheckGitHubCatalogueAppResponse](
		ctx, c, consolewire.CheckCatalogueApp, access.RoleOperator, req)
}

// beginVia asks the module to begin a flow with request and signs the state it
// names as the caller.
func beginVia[Req any](ctx context.Context, c *Console, method string, req Req) (githubBegin, error) {
	_, id, err := c.githubCaller(ctx, access.RoleOperator)
	if err != nil {
		return githubBegin{}, err
	}
	return beginAs(ctx, c, id, method, req)
}

func beginAs[Req any](ctx context.Context, c *Console, id access.Identity, method string, req Req) (githubBegin, error) {
	ctx, cancel := context.WithTimeout(ctx, githubCallTimeout)
	defer cancel()
	caller := c.snapshot(id)
	ctx = consolecall.WithFacts(ctx, c.githubFacts(ctx, nil))
	begun, err := consolecall.ProxyJSON[Req, consolewire.Begun](ctx, c.githubModule(), consolewire.Module, method, caller, req)
	if err != nil {
		return githubBegin{}, githubError(err)
	}
	return c.signBegun(id, begun)
}

// BeginGitHubCatalogueAppConnect starts creating a catalogue App, or finishing
// installing one created before.
func (c *Console) BeginGitHubCatalogueAppConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubCatalogueAppConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubCatalogueAppConnectResponse], error) {
	begun, err := c.beginCatalogueAppConnect(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubCatalogueAppConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

func (c *Console) beginCatalogueAppConnect(ctx context.Context, id string) (githubBegin, error) {
	return beginVia(ctx, c, consolewire.BeginCatalogueConnect, consolewire.BeginCatalogueConnectRequest{ID: id})
}

// DisconnectGitHubCatalogueApp uninstalls a catalogue App, then forgets it.
func (c *Console) DisconnectGitHubCatalogueApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubCatalogueAppRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubCatalogueAppResponse], error) {
	return githubProto[directoryrosterv1.DisconnectGitHubCatalogueAppRequest, directoryrosterv1.DisconnectGitHubCatalogueAppResponse](
		ctx, c, consolewire.DisconnectCatalogueApp, access.RoleOperator, req)
}

// BeginGitHubRunnerAppConnect starts creating a runner App, or finishing
// installing one created before.
func (c *Console) BeginGitHubRunnerAppConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubRunnerAppConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubRunnerAppConnectResponse], error) {
	begun, err := c.beginRunnerAppConnect(ctx, req.Msg.GetOrg(), req.Msg.GetTier())
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubRunnerAppConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

func (c *Console) beginRunnerAppConnect(ctx context.Context, org, tier string) (githubBegin, error) {
	return beginVia(ctx, c, consolewire.BeginRunnerConnect, consolewire.BeginRunnerConnectRequest{Org: org, Tier: tier})
}

// DisconnectGitHubRunnerApp uninstalls a runner App, then forgets it.
func (c *Console) DisconnectGitHubRunnerApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubRunnerAppRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubRunnerAppResponse], error) {
	return githubProto[directoryrosterv1.DisconnectGitHubRunnerAppRequest, directoryrosterv1.DisconnectGitHubRunnerAppResponse](
		ctx, c, consolewire.DisconnectRunnerApp, access.RoleOperator, req)
}

// BeginGitHubAppConnect starts creating any App, or finishing installing one
// created before. The module knows which kind an id is, and who may operate it.
func (c *Console) BeginGitHubAppConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubAppConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubAppConnectResponse], error) {
	begun, err := beginVia(ctx, c, consolewire.BeginAppConnect, consolewire.BeginAppConnectRequest{
		ID: req.Msg.GetId(), Owner: req.Msg.GetOwner(), OwnerDirectory: req.Msg.GetOwnerDirectory(),
	})
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubAppConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

// DisconnectGitHubApp uninstalls any App, then forgets it.
func (c *Console) DisconnectGitHubApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubAppRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubAppResponse], error) {
	return githubProto[directoryrosterv1.DisconnectGitHubAppRequest, directoryrosterv1.DisconnectGitHubAppResponse](
		ctx, c, consolewire.DisconnectApp, access.RoleOperator, req)
}

// BeginGitHubLinkAppConnect starts creating the link App.
func (c *Console) BeginGitHubLinkAppConnect(
	ctx context.Context, req *connect.Request[directoryrosterv1.BeginGitHubLinkAppConnectRequest],
) (*connect.Response[directoryrosterv1.BeginGitHubLinkAppConnectResponse], error) {
	begun, err := beginVia(ctx, c, consolewire.BeginLinkAppConnect, consolewire.BeginLinkAppConnectRequest{Owner: req.Msg.GetOwner()})
	if err != nil {
		return nil, err
	}
	response := connect.NewResponse(&directoryrosterv1.BeginGitHubLinkAppConnectResponse{Url: begun.url, Manifest: begun.manifest})
	c.pinFlow(response.Header(), begun.state)
	return response, nil
}

// DisconnectGitHubLinkApp forgets the link App and makes every link made with it
// unverifiable.
func (c *Console) DisconnectGitHubLinkApp(
	ctx context.Context, req *connect.Request[directoryrosterv1.DisconnectGitHubLinkAppRequest],
) (*connect.Response[directoryrosterv1.DisconnectGitHubLinkAppResponse], error) {
	return githubProto[directoryrosterv1.DisconnectGitHubLinkAppRequest, directoryrosterv1.DisconnectGitHubLinkAppResponse](
		ctx, c, consolewire.DisconnectLinkApp, access.RoleOperator, req)
}

// ConfirmGitHubRemovals lets exactly the removal set the operator saw go ahead.
func (c *Console) ConfirmGitHubRemovals(
	ctx context.Context, req *connect.Request[directoryrosterv1.ConfirmGitHubRemovalsRequest],
) (*connect.Response[directoryrosterv1.ConfirmGitHubRemovalsResponse], error) {
	return githubProto[directoryrosterv1.ConfirmGitHubRemovalsRequest, directoryrosterv1.ConfirmGitHubRemovalsResponse](
		ctx, c, consolewire.ConfirmRemovals, access.RoleOperator, req)
}

// ImportGitHubLinks adopts approved pairings as links.
func (c *Console) ImportGitHubLinks(
	ctx context.Context, req *connect.Request[directoryrosterv1.ImportGitHubLinksRequest],
) (*connect.Response[directoryrosterv1.ImportGitHubLinksResponse], error) {
	return githubProto[directoryrosterv1.ImportGitHubLinksRequest, directoryrosterv1.ImportGitHubLinksResponse](
		ctx, c, consolewire.ImportLinks, access.RoleOperator, req)
}

// beginOrganisationConnect asks the module to begin an organisation's connect
// and signs the state it names, as id.
func (c *Console) beginOrganisationConnect(ctx context.Context, id access.Identity, org, owner string) (githubBegin, error) {
	return beginAs(ctx, c, id, consolewire.BeginConnect, consolewire.BeginConnectRequest{Org: org, OwnerDirectory: owner})
}
