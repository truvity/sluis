package portstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/portstore"
)

func TestBaseCarriesTheModuleAndItsPeers(t *testing.T) {
	set := memory.NewModules().Set(port.ModuleOIDC, port.ModuleGoogle)
	b := portstore.New(set)
	if b.Module != port.ModuleOIDC {
		t.Errorf("Module = %q", b.Module)
	}
	if _, ok := b.Peer(port.ModuleGoogle); !ok {
		t.Error("no google peer")
	}
	if _, ok := b.Peer(port.ModuleGitHub); ok {
		t.Error("an unnamed peer")
	}
}

// On layout 5 the issuer's own table holds none of the google and github
// records: its reads go to the peer views, and a module it holds no grant for
// is refused rather than answered "not found".
func TestReadsOfPeerRecordsGoToThePeer(t *testing.T) {
	ctx := context.Background()
	mods := memory.NewModules()
	secrets := memory.NewSecrets()
	with := func(own port.Module, peers ...port.Module) *portstore.Base {
		set := mods.Set(own, peers...)
		set.Secrets = secrets
		return portstore.New(set)
	}
	w1 := hub.Workspace{ID: "w1", Backend: "google", Domains: []string{"example.test"}}
	if err := portstore.NewWorkspaces(with(port.ModuleGoogle)).Put(ctx, w1); err != nil {
		t.Fatal(err)
	}
	crec := catalogueapp.Record{
		ID: "renovate", Org: "example", AppID: 8, AppSlug: "example-renovate", ConnectedAt: time.Now().UTC(), ConnectedBy: "ada@example.test",
	}
	if err := portstore.NewGitHubCatalogueApps(with(port.ModuleGitHub)).Put(ctx, crec, "CAT-KEY"); err != nil {
		t.Fatal(err)
	}

	issuer := with(port.ModuleOIDC, port.ModuleGoogle, port.ModuleGitHub)
	list, err := portstore.NewWorkspaces(issuer).List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "w1" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if got, err := portstore.NewWorkspaces(issuer).Get(ctx, "w1"); err != nil || got.ID != "w1" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	rec, key, ok, err := portstore.NewGitHubCatalogueApps(issuer).Get(ctx, "renovate")
	if err != nil || !ok || rec.ID != "renovate" || key != "CAT-KEY" {
		t.Fatalf("App Get = %+v, %q, %v, %v", rec, key, ok, err)
	}

	// No grant, no read.
	blind := with(port.ModuleOIDC)
	if _, err = portstore.NewWorkspaces(blind).List(ctx); !errors.Is(err, portstore.ErrNoPeer) {
		t.Errorf("List without the google peer = %v, want ErrNoPeer", err)
	}
	if _, _, _, err = portstore.NewGitHubCatalogueApps(blind).Get(ctx, "renovate"); !errors.Is(err, portstore.ErrNoPeer) {
		t.Errorf("App Get without the github peer = %v, want ErrNoPeer", err)
	}
	// A peer cannot be written through: the issuer's State refuses the key.
	if err = portstore.NewWorkspaces(issuer).Put(ctx, hub.Workspace{ID: "w2", Backend: "google"}); !errors.Is(err, port.ErrNotOwner) {
		t.Errorf("Put of a google record by the issuer = %v, want ErrNotOwner", err)
	}
}
