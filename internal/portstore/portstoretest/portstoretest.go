// Package portstoretest is the harness every test of a domain store on the
// ports runs over: State and Secrets in memory, with a way to open another
// "process" onto the same State and Secrets.
package portstoretest

import (
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
)

// Env is one composition under test: a State and a Secrets, and how to open
// another "process" onto the same ones (two replicas of the service, or the
// host's runner and the guest's).
type Env struct {
	Name string
	// Open is a new "process" onto the same State and Secrets.
	Open func(t *testing.T) port.Set
	// Advance moves every process's clock forward, so a lifetime can be
	// crossed without sleeping.
	Advance func(time.Duration)
}

// Envs are the compositions every domain store runs over: the in-memory State
// and Secrets, where every "process" is the same store.
func Envs(t *testing.T) []Env {
	t.Helper()
	m := memory.New()
	set := m.Set()
	return []Env{{Name: "memory", Open: func(*testing.T) port.Set { return set }, Advance: m.Advance}}
}

// Each runs a test over every composition.
func Each(t *testing.T, test func(t *testing.T, e Env)) {
	t.Helper()
	for _, e := range Envs(t) {
		t.Run(e.Name, func(t *testing.T) { test(t, e) })
	}
}
