package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/truvity/sluis/internal/audit"
	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/store"
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
	ghconsole.Register(m.rpc, ghconsole.New(ghconsole.Deps{
		Log:        log,
		Audit:      m.trail,
		Trigger:    m.stores.Ports.Trigger,
		Policy:     func() *policy.Set { return set },
		PublicURL:  cfg.console,
		Orgs:       portstore.NewGitHubOrgs(m.base),
		OrgAppName: cfg.policy.OrganisationAppName,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
	}))
	return nil
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
