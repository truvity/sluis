package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/port/porttest"
)

// The conformance suite runs against the store of every module.
func TestModuleStoresConformance(t *testing.T) {
	for _, mod := range port.Modules() {
		t.Run(string(mod), func(t *testing.T) {
			porttest.Run(t, func(*testing.T) porttest.Env {
				s := memory.NewModules().Store(mod)
				s.Allow("workload-token", "system:serviceaccount:ns:sa", "sluis")
				return porttest.Env{
					Set:          s.Set(),
					Advance:      s.Advance,
					BlobPrefixes: []string{"reports/", "snapshots/"},
					Proof: func() porttest.Proof {
						return porttest.Proof{Token: "workload-token", Subject: "system:serviceaccount:ns:sa", Audience: "sluis"}
					},
				}
			})
		})
	}
}

func TestOwnershipRefusal(t *testing.T) {
	ctx := context.Background()
	m := memory.NewModules()
	gh := m.Set(port.ModuleGitHub)
	if gh.Module != port.ModuleGitHub {
		t.Fatalf("Module = %q", gh.Module)
	}

	// the module's own key and a key of no module are written
	if _, err := gh.State.Put(ctx, "gh.org.acme", []byte("x"), time.Hour); err != nil {
		t.Fatalf("own key: %v", err)
	}
	if _, err := gh.State.Put(ctx, "lease.github-tick:acme", []byte("x"), time.Hour); err != nil {
		t.Fatalf("lease of the module: %v", err)
	}

	// another module's key is refused by every write, and nothing lands
	other := "ws.slack.t1"
	rev, err := m.Set(port.ModuleSlack).State.Put(ctx, other, []byte("x"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"Put":              func() error { _, err := gh.State.Put(ctx, other, []byte("y"), time.Hour); return err },
		"Create":           func() error { _, err := gh.State.Create(ctx, "ws.slack.t2", []byte("y"), time.Hour); return err },
		"Update":           func() error { _, err := gh.State.Update(ctx, other, []byte("y"), time.Hour, rev); return err },
		"Delete":           func() error { return gh.State.Delete(ctx, other) },
		"DeleteIfRevision": func() error { return gh.State.DeleteIfRevision(ctx, other, rev) },
	} {
		if err := call(); !errors.Is(err, port.ErrNotOwner) {
			t.Errorf("%s: err = %v, want ErrNotOwner", name, err)
		}
	}
	if _, err := m.Store(port.ModuleGitHub).Get(ctx, other); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("a refused write reached the github store: %v", err)
	}
	if r, err := m.Store(port.ModuleSlack).Get(ctx, other); err != nil || string(r.Value) != "x" {
		t.Errorf("the slack record changed: %v %q", err, r.Value)
	}

	// a key of a backend with no module is refused, not written somewhere
	if _, err := gh.State.Put(ctx, "ws.dir.entra.x", []byte("y"), time.Hour); !errors.Is(err, port.ErrUnsupported) {
		t.Errorf("no-module backend: err = %v, want ErrUnsupported", err)
	}
}

func TestPeerViews(t *testing.T) {
	ctx := context.Background()
	m := memory.NewModules()
	if _, err := m.Set(port.ModuleGoogle).State.Put(ctx, "ws.dir.google.w1", []byte("g"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Set(port.ModuleSlack).State.Put(ctx, "ws.slack.t1", []byte("s"), 0); err != nil {
		t.Fatal(err)
	}

	issuer := m.Set(port.ModuleOIDC, port.ModuleGoogle, port.ModuleOIDC)
	g, ok := issuer.Peer(port.ModuleGoogle)
	if !ok {
		t.Fatal("no google peer")
	}
	if r, err := g.Get(ctx, "ws.dir.google.w1"); err != nil || string(r.Value) != "g" {
		t.Fatalf("peer Get: %v %q", err, r.Value)
	}
	if page, err := g.List(ctx, "ws.dir.", "", 0); err != nil || len(page.Records) != 1 {
		t.Fatalf("peer List: %v %d", err, len(page.Records))
	}

	// a peer that was not named is absent, and the module is not its own peer
	if _, ok := issuer.Peer(port.ModuleSlack); ok {
		t.Error("an unnamed peer is readable")
	}
	if _, ok := issuer.Peer(port.ModuleOIDC); ok {
		t.Error("a module is its own peer")
	}

	// the view reads the peer's store only
	if _, err := g.Get(ctx, "ws.slack.t1"); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("google peer saw a slack key: %v", err)
	}

	// the view carries no write: the compile-time half is the StateReader
	// type, and the run-time half is that it does not unwrap to a State.
	if _, ok := g.(port.State); ok {
		t.Error("a peer view is a State")
	}
	var _ port.StateReader = g
}

func TestOwnedKeepsOptionalCapabilities(t *testing.T) {
	s := memory.NewModules().Set(port.ModuleOIDC)
	if _, ok := s.State.(port.RevisionPeeker); !ok {
		t.Error("RevisionPeeker lost")
	}
	if _, ok := s.State.(port.StateExporter); !ok {
		t.Error("StateExporter lost")
	}
}
