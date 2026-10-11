package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/store"
)

// Minter is the GitHub module's installation-token mint and nothing else: the
// App keys of the catalogue, read from the secrets store, and the `github`
// module's `mint_installation` method over them (docs/decisions/0071). It runs
// no controller pass and holds no console client, no leases and no audit trail:
// the issuer decides the grant and audits the request, and this only signs.
type Minter struct {
	rpc    *modcall.Server
	stores *store.Stores
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
func (m *Minter) Close() { m.stores.Close() }
