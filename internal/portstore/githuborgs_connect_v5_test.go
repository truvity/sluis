package portstore_test

import (
	"testing"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state/memory"
)

// An organisation connected from the console on layout v5 has no App of the
// catalogue yet: the App GitHub just created is kept as one, and the
// organisation's record names it, the shape the migration gives it.
func TestAFreshOrganisationConnectOnLayoutV5KeepsTheAppAndNamesIt(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		set := e.open(t)
		b := portstore.New(set).WithV5(secretstore.FromStoreV5(memory.New(), ""))
		orgs := portstore.NewGitHubOrgs(b)
		apps := portstore.NewGitHubCatalogueApps(b)

		// The create callback: the App exists, it is not installed yet.
		rec := connection.Record{Org: "acme", AppID: 8, AppSlug: "acme-access-roster", ConnectedAt: appsAt, ConnectedBy: "ada@acme.example", Owner: "ws1"}
		cred := connection.Credential{Org: "acme", AppID: 8, PrivateKey: "ORG-KEY"}
		if err := orgs.Put(ctx, rec, cred); err != nil {
			t.Fatalf("a fresh connect: %v", err)
		}
		list, err := orgs.List(ctx)
		if err != nil || len(list) != 1 || list[0].AppRef != "acme-access-roster" || list[0].Owner != "ws1" || list[0].Installed() {
			t.Fatalf("List = %+v, %v", list, err)
		}
		app, key, ok, err := apps.Get(ctx, "acme-access-roster")
		if err != nil || !ok || key != "ORG-KEY" || app.Org != "acme" || app.AppID != 8 || app.InstallationID != 0 {
			t.Fatalf("the App = %+v %q %v %v", app, key, ok, err)
		}
		// The controller reads the key through app_ref.
		got, ok, err := orgs.Credential(ctx, "acme")
		if err != nil || !ok || got.PrivateKey != "ORG-KEY" || got.AppID != 8 {
			t.Fatalf("Credential = %+v, %v, %v", got, ok, err)
		}

		// The setup step: the console writes the record it read, with the
		// installation, and the credential it read.
		installed := list[0]
		installed.InstallationID, got.InstallationID = 11, 11
		if err = orgs.Put(ctx, installed, got); err != nil {
			t.Fatalf("the install step: %v", err)
		}
		if app, _, _, _ = apps.Get(ctx, "acme-access-roster"); app.InstallationID != 11 {
			t.Errorf("the App stayed pending: %+v", app)
		}
		if got, ok, err = orgs.Credential(ctx, "acme"); err != nil || !ok || got.InstallationID != 11 || got.PrivateKey != "ORG-KEY" {
			t.Fatalf("Credential after install = %+v, %v, %v", got, ok, err)
		}
	})
}

// Disconnect takes the organisation's own App with it, so that connecting
// again works; an App another organisation names, or the configuration
// names, stays.
func TestDisconnectThenReconnectOnLayoutV5(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		set := e.open(t)
		b := portstore.New(set).WithV5(secretstore.FromStoreV5(memory.New(), ""))
		orgs := portstore.NewGitHubOrgs(b)
		apps := portstore.NewGitHubApps(b)
		rec := connection.Record{Org: "acme", AppID: 8, AppSlug: "acme-access-roster", InstallationID: 11, ConnectedAt: appsAt}
		if err := orgs.Put(ctx, rec, connection.Credential{Org: "acme", AppID: 8, InstallationID: 11, PrivateKey: "K1"}); err != nil {
			t.Fatal(err)
		}
		if err := orgs.Delete(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := apps.PrivateKey(ctx, "acme-access-roster"); err != nil || ok {
			t.Errorf("the App's key outlived Disconnect: %v, %v", ok, err)
		}
		if all, err := apps.List(ctx); err != nil || len(all) != 0 {
			t.Errorf("Apps after Disconnect = %+v, %v", all, err)
		}
		// GitHub cannot delete an App, so the next create gets another slug.
		again := connection.Record{Org: "acme", AppID: 9, AppSlug: "acme-access-roster-1", ConnectedAt: appsAt}
		if err := orgs.Put(ctx, again, connection.Credential{Org: "acme", AppID: 9, PrivateKey: "K2"}); err != nil {
			t.Fatalf("reconnect: %v", err)
		}
		if got, ok, err := orgs.Credential(ctx, "acme"); err != nil || !ok || got.PrivateKey != "K2" || got.AppID != 9 {
			t.Fatalf("Credential = %+v, %v, %v", got, ok, err)
		}
	})
}

// An App the migration (or an operator) shared between organisations is not
// the disconnected organisation's to delete.
func TestDisconnectKeepsASharedApp(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		set := e.open(t)
		b := portstore.New(set).WithV5(secretstore.FromStoreV5(memory.New(), ""))
		orgs := portstore.NewGitHubOrgs(b)
		for _, org := range []string{"acme", "beta"} {
			rec := connection.Record{Org: org, AppID: 8, AppSlug: "shared", InstallationID: 11, ConnectedAt: appsAt}
			if org == "beta" {
				rec.AppRef = "shared"
			}
			if err := orgs.Put(ctx, rec, connection.Credential{Org: org, AppID: 8, PrivateKey: "K"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := orgs.Delete(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := portstore.NewGitHubApps(b).PrivateKey(ctx, "shared"); err != nil || !ok {
			t.Errorf("a shared App was deleted: %v, %v", ok, err)
		}
	})
}

// The configuration's entry wins over making an App: nothing is made, and
// Disconnect leaves the App it names.
func TestTheAppRefEntryWinsOverMakingAnApp(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		set := e.open(t)
		stores := secretstore.FromStoreV5(memory.New(), "")
		plain := portstore.New(set).WithV5(stores)
		cat := portstore.NewGitHubCatalogueApps(plain)
		if err := cat.Put(ctx, catalogueapp.Record{ID: "declared", Org: "acme", AppID: 8, AppSlug: "declared", InstallationID: 11, ConnectedAt: appsAt}, "DECL-KEY"); err != nil {
			t.Fatal(err)
		}
		b := portstore.New(set).WithV5(stores).DeclareGitHubApps(portstore.DeclaredGitHubApps{AppRef: func(string) string { return "declared" }})
		orgs := portstore.NewGitHubOrgs(b)
		rec := connection.Record{Org: "acme", AppID: 8, AppSlug: "acme-access-roster", ConnectedAt: appsAt}
		if err := orgs.Put(ctx, rec, connection.Credential{Org: "acme", AppID: 8, PrivateKey: "ORG-KEY"}); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := orgs.Credential(ctx, "acme"); err != nil || !ok || got.PrivateKey != "DECL-KEY" {
			t.Fatalf("Credential = %+v, %v, %v", got, ok, err)
		}
		if all, err := cat.List(ctx); err != nil || len(all) != 1 {
			t.Fatalf("an App was made beside the declared one: %+v, %v", all, err)
		}
		if err := orgs.Delete(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := portstore.NewGitHubApps(b).PrivateKey(ctx, "declared"); err != nil || !ok {
			t.Errorf("the declared App was deleted: %v, %v", ok, err)
		}
	})
}
