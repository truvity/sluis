package portstore_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state/memory"
)

// connectedWithItsOwnApp is an organisation connected earlier with an App of its
// own, as layout v5 keeps it, and the two bases over the same stores: one that
// declares `acme` uses the catalogue App `acme-declared`, and one that declares
// nothing, which reads the record as it is stored.
func connectedWithItsOwnApp(t *testing.T, set port.Set) (declared, stored *portstore.GitHubOrgs, apps *portstore.GitHubCatalogueApps) {
	t.Helper()
	v5 := secretstore.FromStoreV5(memory.New(), "")
	plain := portstore.New(set).WithV5(v5)
	stored, apps = portstore.NewGitHubOrgs(plain), portstore.NewGitHubCatalogueApps(plain)
	rec := connection.Record{
		Org: "acme", AppID: 8, AppSlug: "acme-access-roster", InstallationID: 11, ConnectedAt: appsAt, ConnectedBy: "ada@acme.example", Owner: "ws1",
	}
	if err := stored.Put(ctx, rec, connection.Credential{Org: "acme", AppID: 8, InstallationID: 11, PrivateKey: "OWN-KEY"}); err != nil {
		t.Fatal(err)
	}
	b := portstore.New(set).WithV5(v5).DeclareGitHubApps(portstore.DeclaredGitHubApps{AppRef: func(org string) string {
		if org == "acme" {
			return "acme-declared"
		}
		return ""
	}})
	return portstore.NewGitHubOrgs(b), stored, apps
}

func declaredApp(org string, installation int64) catalogueapp.Record {
	return catalogueapp.Record{
		ID: "acme-declared", Org: org, AppID: 9, AppSlug: "acme-declared", InstallationID: installation,
		HTMLURL: "https://github.example/apps/acme-declared", ConnectedAt: appsAt,
	}
}

// An organisation connected earlier with its own App keeps it until the App
// the configuration declares for it is installed in it, and then switches, on
// read and on the controller's next pass, with its owner and connection kept.
func TestAnAlreadyConnectedOrganisationSwitchesToTheDeclaredAppOnceItIsInstalled(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		orgs, stored, apps := connectedWithItsOwnApp(t, e.open(t))

		// Created and not installed: the declared App cannot sign for the
		// organisation yet.
		if err := apps.Put(ctx, declaredApp("acme", 0), "DECL-KEY"); err != nil {
			t.Fatal(err)
		}
		list, err := orgs.List(ctx)
		if err != nil || len(list) != 1 || list[0].AppRef != "acme-access-roster" || list[0].AppID != 8 {
			t.Fatalf("List before the install = %+v, %v", list, err)
		}
		if got, ok, err := orgs.Credential(ctx, "acme"); err != nil || !ok || got.PrivateKey != "OWN-KEY" || got.AppID != 8 || got.InstallationID != 11 {
			t.Fatalf("Credential before the install = %+v, %v, %v", got, ok, err)
		}
		note, err := orgs.DeclaredAppNote(ctx, "acme")
		if err != nil || !strings.Contains(note, "acme-declared") || !strings.Contains(note, "not installed") {
			t.Errorf("note = %q, %v, want that the declared App is not installed", note, err)
		}
		if _, _, switched, err := orgs.AdoptDeclaredApp(ctx, "acme"); err != nil || switched {
			t.Fatalf("AdoptDeclaredApp before the install = %v, %v", switched, err)
		}

		// Installed: the next read uses it.
		if err = apps.Put(ctx, declaredApp("acme", 12), "DECL-KEY"); err != nil {
			t.Fatal(err)
		}
		list, err = orgs.List(ctx)
		if err != nil || len(list) != 1 || list[0].AppRef != "acme-declared" || list[0].AppID != 9 || list[0].AppSlug != "acme-declared" ||
			list[0].InstallationID != 12 || list[0].Owner != "ws1" || list[0].ConnectedBy != "ada@acme.example" {
			t.Fatalf("List after the install = %+v, %v", list, err)
		}
		if got, ok, err := orgs.Credential(ctx, "acme"); err != nil || !ok || got.PrivateKey != "DECL-KEY" || got.AppID != 9 || got.InstallationID != 12 {
			t.Fatalf("Credential after the install = %+v, %v, %v", got, ok, err)
		}
		if note, err = orgs.DeclaredAppNote(ctx, "acme"); err != nil || note != "" {
			t.Errorf("note after the install = %q, %v", note, err)
		}
		if again, _ := stored.List(ctx); len(again) != 1 || again[0].AppRef != "acme-access-roster" {
			t.Fatalf("the stored record changed before the controller's pass: %+v", again)
		}

		// The pass writes the switch, once.
		from, to, switched, err := orgs.AdoptDeclaredApp(ctx, "acme")
		if err != nil || !switched || from != "acme-access-roster" || to != "acme-declared" {
			t.Fatalf("AdoptDeclaredApp = %q, %q, %v, %v", from, to, switched, err)
		}
		if _, _, switched, err = orgs.AdoptDeclaredApp(ctx, "acme"); err != nil || switched {
			t.Errorf("the second AdoptDeclaredApp switched again: %v, %v", switched, err)
		}
		written, err := stored.List(ctx)
		if err != nil || len(written) != 1 || written[0].AppRef != "acme-declared" || written[0].AppID != 9 ||
			written[0].InstallationID != 12 || written[0].Owner != "ws1" {
			t.Fatalf("the stored record after the switch = %+v, %v", written, err)
		}

		// The old App is kept: nothing removes it, and nothing names it.
		if _, key, ok, err := apps.Get(ctx, "acme-access-roster"); err != nil || !ok || key != "OWN-KEY" {
			t.Errorf("the old App = %q, %v, %v, want it kept", key, ok, err)
		}
		if err = orgs.Delete(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		if _, _, ok, err := apps.Get(ctx, "acme-declared"); err != nil || !ok {
			t.Errorf("Disconnect removed the declared App: %v, %v", ok, err)
		}
		if _, _, ok, err := apps.Get(ctx, "acme-access-roster"); err != nil || !ok {
			t.Errorf("Disconnect removed an App the organisation no longer used: %v, %v", ok, err)
		}
	})
}

// A declared App that is not there, or was created under another organisation,
// leaves the one in use, and the note says which it is.
func TestADeclaredAppThatCannotActLeavesTheOneInUse(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		orgs, _, apps := connectedWithItsOwnApp(t, e.open(t))

		note, err := orgs.DeclaredAppNote(ctx, "acme")
		if err != nil || !strings.Contains(note, "not created") {
			t.Errorf("note = %q, %v, want that the declared App is not created", note, err)
		}
		if _, _, switched, err := orgs.AdoptDeclaredApp(ctx, "acme"); err != nil || switched {
			t.Errorf("AdoptDeclaredApp = %v, %v", switched, err)
		}

		if err = apps.Put(ctx, declaredApp("beta", 12), "DECL-KEY"); err != nil {
			t.Fatal(err)
		}
		note, err = orgs.DeclaredAppNote(ctx, "acme")
		if err != nil || !strings.Contains(note, "created under beta") {
			t.Errorf("note = %q, %v, want that the declared App was created under beta", note, err)
		}
		if got, ok, err := orgs.Credential(ctx, "acme"); err != nil || !ok || got.PrivateKey != "OWN-KEY" || got.AppID != 8 {
			t.Errorf("Credential = %+v, %v, %v, want the App in use", got, ok, err)
		}
		if _, _, switched, err := orgs.AdoptDeclaredApp(ctx, "acme"); err != nil || switched {
			t.Errorf("AdoptDeclaredApp = %v, %v", switched, err)
		}
	})
}

// Writing the record of an organisation names the declared App, too, once it
// can act, whichever App the record being written names.
func TestWritingARecordNamesTheDeclaredAppOnceItCanAct(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		orgs, stored, apps := connectedWithItsOwnApp(t, e.open(t))
		if err := apps.Put(ctx, declaredApp("acme", 12), "DECL-KEY"); err != nil {
			t.Fatal(err)
		}
		// The console writes back the record it read from the stored one.
		list, err := stored.List(ctx)
		if err != nil || len(list) != 1 {
			t.Fatal(list, err)
		}
		if err = orgs.Put(ctx, list[0], connection.Credential{Org: "acme", AppID: 8, InstallationID: 11, PrivateKey: "OWN-KEY"}); err != nil {
			t.Fatal(err)
		}
		written, err := stored.List(ctx)
		if err != nil || len(written) != 1 || written[0].AppRef != "acme-declared" || written[0].AppID != 9 || written[0].InstallationID != 12 {
			t.Fatalf("the stored record = %+v, %v", written, err)
		}
		if got, ok, err := stored.Credential(ctx, "acme"); err != nil || !ok || got.PrivateKey != "DECL-KEY" || got.AppID != 9 {
			t.Errorf("Credential = %+v, %v, %v", got, ok, err)
		}
	})
}
