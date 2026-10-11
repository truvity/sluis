package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/sluis/blobprefix"
	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/gen/directoryroster/v1/directoryrosterv1connect"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/consoleauth"
	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/telemetry"
	"github.com/truvity/sluis/policy"
)

// Minter is the GitHub module's installation-token mint and nothing else: the
// App keys of the catalogue, read from the secrets store, and the `github`
// module's `mint_installation` method over them (docs/decisions/0071). It runs
// no controller pass and holds no console client, no leases and no audit trail:
// the issuer decides the grant and audits the request, and this only signs.
type Minter struct {
	rpc    *modcall.Server
	stores *store.Stores
	base   *portstore.Base
	trail  *audit.Trail
}

// NewMinter assembles the mint from the settings [Load] built. Opening reads
// no secret: an App's key is read when a request names it.
func NewMinter(ctx context.Context, cfg Config, log *slog.Logger) (*Minter, error) {
	if log == nil {
		log = slog.Default()
	}
	stores, err := store.Open(ctx, cfg.stores, log)
	if err != nil {
		return nil, err
	}
	if stores.Adapter == store.AdapterLegacy {
		stores.Close()
		return nil, fmt.Errorf("ports.adapter %s: the mint reads the catalogue Apps from the State and Secrets ports", stores.Adapter)
	}
	base := portstore.New(stores.Ports).WithV4(stores.V4).WithV5(stores.V5)
	if err = base.RequireSecrets(); err != nil {
		stores.Close()
		return nil, fmt.Errorf("ports.adapter %s: %w", stores.Adapter, err)
	}
	log.InfoContext(ctx, "the GitHub mint is assembled")
	return &Minter{
		rpc:    MintServer(portstore.NewGitHubCatalogueApps(base), &http.Client{Timeout: 30 * time.Second}),
		stores: stores,
		base:   base,
	}, nil
}

// ServeConsole makes the module also answer the console's GitHub operations
// (docs/decisions/0071, v1.76 D-v176-1 b) on the server the mint is on: the
// console class reaches them and no other. The organisations' records, their
// keys, the audit trail and the module's own tick are this function's; who the
// caller is and which directories exist are the console's snapshot of the call.
func (m *Minter) ServeConsole(ctx context.Context, cfg Config, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	set, err := policy.NewSet(cfg.policy.Policy)
	if err != nil {
		return fmt.Errorf("the policy: %w", err)
	}
	cfg.audit.Log = log
	if cfg.audit, err = audit.FromPlan(cfg.audit, m.stores.Plan); err != nil {
		return err
	}
	if m.trail, err = audit.Open(ctx, cfg.audit); err != nil {
		return err
	}
	directory, err := consoleDirectory(ctx, cfg)
	if err != nil {
		return err
	}
	ghconsole.Register(m.rpc, ghconsole.New(ghconsole.Deps{
		Log:        log,
		Audit:      m.trail,
		Trigger:    m.stores.Ports.Trigger,
		Policy:     func() *policy.Set { return set },
		PublicURL:  cfg.console,
		Orgs:       portstore.NewGitHubOrgs(m.base),
		OrgAppName: cfg.policy.OrganisationAppName,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		Directory:  directory,
		// And what a link may be made of: the module reads the records of
		// the connections, the Apps and the links it keeps, and asks the console
		// what the directory knows.
		Reports:       rails.NewBlobReports(m.stores.Ports.Blob, blobprefix.GitHubReports),
		Confirmations: portstore.NewGitHubOrgs(m.base),
		LinkApp:       portstore.NewGitHubOrgs(m.base),
		Links:         portstore.NewGitHubLinks(m.base),
		RunnerApps:    portstore.NewGitHubRunnerApps(m.base),
		RunnerTiers:   cfg.policy.RunnerTiers(),
		CatalogueApps: portstore.NewGitHubCatalogueApps(m.base),
		Catalogue:     cfg.policy.GitHubCatalogue(),
	}))
	return nil
}

// consoleDirectory is what the directory knows of an address, asked of the
// console as this module's own identity (the roster controllers may explain): a
// module reads no directory itself.
func consoleDirectory(ctx context.Context, cfg Config) (func(context.Context, string) (ghconsole.Facts, error), error) {
	if cfg.console == "" {
		return nil, nil
	}
	var proof consoleauth.Source = consoleauth.File(cfg.tokenFile)
	if cfg.consoleAWS != "" {
		aws, err := consoleauth.NewAWS(ctx, cfg.consoleAWS)
		if err != nil {
			return nil, err
		}
		proof = aws
	}
	opts := append([]connect.ClientOption{consoleauth.Interceptor(proof)}, telemetry.ConnectClientOptions()...)
	client := directoryrosterv1connect.NewAccessServiceClient(&http.Client{Timeout: 30 * time.Second}, cfg.console, opts...)
	return func(ctx context.Context, email string) (ghconsole.Facts, error) {
		resp, err := client.Explain(ctx, connect.NewRequest(&directoryrosterv1.ExplainRequest{Email: email}))
		if err != nil {
			return ghconsole.Facts{}, err
		}
		return ghconsole.Facts{
			InDomain: resp.Msg.GetInDomain(), Authoritative: resp.Msg.GetAuthoritative(), Found: resp.Msg.GetFound(), Suspended: resp.Msg.GetSuspended(),
		}, nil
	}, nil
}

// MintServer is the `github` module's server with the mint over apps: it
// answers `mint_installation` to the issuer's class only (githubtokens.Register
// allows no other) and has no other method.
func MintServer(apps githubtokens.AppStore, web *http.Client) *modcall.Server {
	s := modcall.NewServer(githubtokens.Module)
	githubtokens.Register(s, githubtokens.InProcess{Store: apps, HTTP: web})
	return s
}

// RPC is the module's server: it answers `mint_installation` to the issuer's
// class and refuses every other caller.
func (m *Minter) RPC() *modcall.Server { return m.rpc }

// Close closes the ports.
func (m *Minter) Close() {
	if m.trail != nil {
		_ = m.trail.Close()
	}
	m.stores.Close()
}
