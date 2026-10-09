// Package keystest gives tests a key provider that keeps nothing outside the
// process.
package keystest

import (
	"testing"

	"github.com/truvity/sluis/audit/keys"
	skeys "github.com/truvity/sluis/storage/keys"
	localkeys "github.com/truvity/sluis/storage/keys/local"
)

// New returns a provider over the local backend with a random root, holding a
// pseudonym key and a conceal key. It is closed when the test ends.
func New(t testing.TB) *keys.PortProvider {
	t.Helper()
	b, err := localkeys.New(localkeys.RandomRoot())
	if err != nil {
		t.Fatal(err)
	}
	set, err := skeys.Open(skeys.Config{Adapter: "local", Keys: map[skeys.Purpose]skeys.Entry{
		skeys.Pseudonym: {Key: "audit-pseudonym"}, skeys.Conceal: {Key: "audit-conceal"},
	}}, skeys.Options{Backend: b, Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := keys.NewPortProvider(set)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}
