package portstore_test

import (
	"testing"

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
