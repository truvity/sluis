package memory

import (
	"fmt"

	"github.com/truvity/sluis/internal/port"
)

// Modules is one memory store per module, the in-process shape of layout 5's
// table per module (ADR 0072). Each store is separate: a key written through
// one module is invisible to every other, except through a peer view.
type Modules struct {
	stores map[port.Module]*Store
}

// NewModules returns an empty store for every module in [port.Modules].
func NewModules(opts ...Option) *Modules {
	m := &Modules{stores: map[port.Module]*Store{}}
	for _, mod := range port.Modules() {
		m.stores[mod] = New(opts...)
	}
	return m
}

// Store is the module's store, with no ownership check (a test's backdoor to
// seed or inspect it). It panics on a name that is not a module.
func (m *Modules) Store(mod port.Module) *Store {
	s, ok := m.stores[mod]
	if !ok {
		panic(fmt.Sprintf("memory: %q is not a module", mod))
	}
	return s
}

// Set is the ports of a process that runs module own: its State is the own
// module's store, refusing a write of another module's key with
// [port.ErrNotOwner], and Peers holds a read-only view of each named peer.
// The other ports are the own store's.
func (m *Modules) Set(own port.Module, peers ...port.Module) port.Set {
	set := m.Store(own).Set()
	set.Module = own
	set.State = port.Owned(own, set.State)
	if len(peers) > 0 {
		set.Peers = map[port.Module]port.StateReader{}
	}
	for _, p := range peers {
		if p == own {
			continue
		}
		set.Peers[p] = port.ReadOnly(m.Store(p))
	}
	return set
}
