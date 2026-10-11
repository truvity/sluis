package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/app"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/portstore/portstoretest"
	"github.com/truvity/sluis/internal/store"
)

var quiet = slog.New(slog.DiscardHandler)

func serveConfig(t *testing.T, change ...func(*config.Serve)) app.Config {
	t.Helper()
	cfg, err := app.FromConfig(issuerFile(t, append([]func(*config.Serve){func(f *config.Serve) { f.Demo = false }}, change...)...), nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// `legacy` keeps the ConfigMap and Secret stores, and with `store: memory`
// there are none of the domain stores a cluster would hold: unchanged.
func TestTheLegacyAdapterKeepsTheDomainStoresItAlwaysHad(t *testing.T) {
	kept, err := app.OpenStoresForTest(context.Background(), serveConfig(t), &store.Stores{Adapter: store.AdapterLegacy}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if kept.GitHubOrgs || kept.GitHubLinks || kept.SlackShared || kept.Credentials != nil {
		t.Errorf("legacy with store: memory kept %+v, want only the memory workspaces", kept)
	}
}

// Any other adapter keeps them on the ports, and every replica of the service
// sees the same stores, the same credentials and the same session key.
func TestAnyOtherAdapterKeepsTheDomainStoresOnThePorts(t *testing.T) {
	portstoretest.Each(t, func(t *testing.T, e portstoretest.Env) {
		open := func() app.KeptForTest {
			st := &store.Stores{Ports: e.Open(t), Adapter: store.AdapterDynamoDB, Shared: true, Usable: true}
			kept, err := app.OpenStoresForTest(context.Background(), serveConfig(t), st, quiet)
			if err != nil {
				t.Fatal(err)
			}
			return kept
		}
		a, b := open(), open()
		if !a.GitHubOrgs || !a.GitHubLinks || !a.SlackShared || a.Credentials == nil {
			t.Fatalf("kept %+v, want every domain store", a)
		}
		if !bytes.Equal(a.SessionKey, b.SessionKey) || len(a.SessionKey) == 0 {
			t.Error("two replicas signed sessions with different keys")
		}
		ctx := context.Background()
		if err := a.Workspaces.Put(ctx, hub.Workspace{ID: "C01", Backend: "google"}); err != nil {
			t.Fatal(err)
		}
		if err := a.Credentials.Save(ctx, "C01", backend.Credential{Type: backend.CredentialOAuth, Admin: "a@b.example", Data: []byte("refresh")}); err != nil {
			t.Fatal(err)
		}
		if list, err := b.Workspaces.List(ctx); err != nil || len(list) != 1 {
			t.Fatalf("the other replica lists %v, %v", list, err)
		}
		if cred, found, err := b.Credentials.Load(ctx, "C01"); err != nil || !found || string(cred.Data) != "refresh" {
			t.Fatalf("the other replica loads %+v, %v, %v", cred, found, err)
		}
	})
}

// An adapter set with no Secrets (the legacy one's, and DynamoDB's until a
// secrets adapter is chosen) stops the start naming the setting, instead of
// failing on the first credential an operator connects.
func TestAnAdapterWithNoSecretsStopsTheStartNamingTheSetting(t *testing.T) {
	set := portstoretest.Envs(t)[0].Open(t)
	set.Secrets = nil
	_, err := app.OpenStoresForTest(context.Background(), serveConfig(t), &store.Stores{Ports: set, Adapter: store.AdapterDynamoDB}, quiet)
	if err == nil || !strings.Contains(err.Error(), "secrets adapter") {
		t.Errorf("err = %v, want a refusal that names the secrets adapter", err)
	}
}

// A demonstration has fixed stores of its own and keeps them whatever the
// adapter.
func TestADemonstrationKeepsItsFixedStores(t *testing.T) {
	kept, err := app.OpenStoresForTest(context.Background(), serveConfig(t, func(f *config.Serve) { f.Demo = true }),
		&store.Stores{Ports: portstoretest.Envs(t)[0].Open(t), Adapter: store.AdapterMemory, Usable: true}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if kept.GitHubOrgs {
		t.Error("a demonstration had domain stores on the ports")
	}
}

// A provider module that is another function's (`<module>.remote`) has none of
// its stores opened in the issuer's process: the issuer's role holds no grant on
// them, and every operation on them is asked of the module. A hub that only
// reads the Google snapshots holds no workspace credential either. The module
// in this process keeps all of them, as the demonstration and the tests have it.
func TestARemoteModuleHasNoStoreInTheIssuersProcess(t *testing.T) {
	portstoretest.Each(t, func(t *testing.T, e portstoretest.Env) {
		open := func(change func(*app.Config)) app.KeptForTest {
			cfg := serveConfig(t)
			change(&cfg)
			st := &store.Stores{Ports: e.Open(t), Adapter: store.AdapterDynamoDB, Shared: true, Usable: true}
			kept, err := app.OpenStoresForTest(context.Background(), cfg, st, quiet)
			if err != nil {
				t.Fatal(err)
			}
			return kept
		}
		local := open(func(*app.Config) {})
		if !local.GitHubOrgs || !local.GitHubLinks || !local.SlackShared || local.Credentials == nil {
			t.Fatalf("the modules in this process kept %+v, want every domain store", local)
		}
		remote := open(func(c *app.Config) {
			c.GitHubRemote, c.SlackRemote, c.CloudflareRemote, c.Reader = true, true, true, true
		})
		if remote.GitHubOrgs || remote.GitHubLinks || remote.SlackShared || remote.Credentials != nil {
			t.Errorf("remote modules left stores in the issuer's process: %+v", remote)
		}
		if remote.Workspaces == nil || len(remote.SessionKey) == 0 {
			t.Errorf("the snapshot reader lost the workspace records or the session key: %+v", remote)
		}
	})
}

// With `google.remote` the issuer's view of the Google module's records is read
// only, even where its role would allow more; the module in this process keeps a
// writable one (v1.76 E-IAM).
func TestAReaderIssuersGoogleRecordsAreReadOnly(t *testing.T) {
	ctx := context.Background()
	open := func(reader bool) (app.KeptForTest, *store.Stores) {
		t.Helper()
		st, err := store.Open(ctx, store.Config{Adapter: store.AdapterMemory, SecretsLayout: config.SecretsLayoutV5}.
			As(port.ModuleOIDC, port.ModuleGoogle), quiet)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(st.Close)
		cfg := serveConfig(t)
		cfg.Reader = reader
		kept, err := app.OpenStoresForTest(ctx, cfg, st, quiet)
		if err != nil {
			t.Fatal(err)
		}
		return kept, st
	}
	ws := hub.Workspace{ID: "north", Backend: "google", Domains: []string{"north.example"}}

	local, _ := open(false)
	if err := local.Workspaces.Put(ctx, ws); err != nil {
		t.Fatalf("the module in this process could not write its record: %v", err)
	}

	remote, st := open(true)
	if err := remote.Workspaces.Put(ctx, ws); err == nil || !errors.Is(err, port.ErrNotOwner) {
		t.Errorf("a write through the reader's Google view: %v, want ErrNotOwner", err)
	}
	// What the module wrote is read through the view.
	google, err := st.ForModule(port.ModuleGoogle)
	if err != nil {
		t.Fatal(err)
	}
	if err = portstore.NewWorkspaces(portstore.New(google)).Put(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if err = remote.Workspaces.Delete(ctx, "north"); err == nil {
		t.Error("a delete through the reader's Google view succeeded")
	}
	if got, err := remote.Workspaces.Get(ctx, "north"); err != nil || got.ID != "north" {
		t.Errorf("the reader's view of the module's record: %v, %v", got, err)
	}
}
