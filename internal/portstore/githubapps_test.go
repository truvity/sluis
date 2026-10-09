package portstore_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/githubroster/appid"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/memory"
)

var appsAt = time.Unix(1700000000, 0).UTC()

// under lists the names directly under a path of a store.
func under(t *testing.T, root state.Store, path ...string) []string {
	t.Helper()
	for _, seg := range path {
		root = root.Child(seg)
	}
	names, err := root.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

func putAllThreeKinds(t *testing.T, b *portstore.Base) {
	t.Helper()
	orgs := portstore.NewGitHubOrgs(b)
	linkApp := link.App{Owner: "acme", AppID: 5, AppSlug: "acme-link", ClientID: "Iv1.x", ConnectedAt: appsAt, ConnectedBy: "ada@acme.example"}
	if err := orgs.PutLinkApp(ctx, linkApp, link.AppCredential{AppID: 5, ClientID: "Iv1.x", ClientSecret: "LINK-SECRET"}); err != nil {
		t.Fatal(err)
	}
	crec := catalogueapp.Record{
		ID: "renovate", Org: "acme", AppID: 8, AppSlug: "acme-renovate", InstallationID: 11, ConnectedAt: appsAt,
		ConnectedBy: "ada@acme.example", Labels: map[string]string{"team": "platform"},
	}
	if err := portstore.NewGitHubCatalogueApps(b).Put(ctx, crec, "CAT-KEY"); err != nil {
		t.Fatal(err)
	}
	rrec := runnerapp.Record{
		Tier: "stable", Org: "acme", AppID: 3, AppSlug: "acme-stable", InstallationID: 4, ConnectedAt: appsAt, ConnectedBy: "ada@acme.example",
	}
	if err := portstore.NewGitHubRunnerApps(b).Put(ctx, rrec, "RUNNER-KEY"); err != nil {
		t.Fatal(err)
	}
}

// The three typed stores are three views of one kind: one list, one id space,
// a purpose on each, and a key read by id.
func TestGitHubAppsAreOneKind(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		b := e.base(t)
		putAllThreeKinds(t, b)
		apps := portstore.NewGitHubApps(b)

		list, err := apps.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, a := range list {
			got = append(got, a.ID+":"+string(a.Purpose))
		}
		want := []string{"link:link", "renovate:catalogue", "runner-stable-acme:runner"}
		if !slices.Equal(got, want) {
			t.Fatalf("List = %v, want %v", got, want)
		}
		if list[1].Labels["team"] != "platform" || list[1].Org != "acme" || !list[1].Installed() {
			t.Errorf("the catalogue App = %+v", list[1])
		}
		if list[2].Tier != "stable" || list[2].InstallationID != 4 {
			t.Errorf("the runner App = %+v", list[2])
		}
		for id, key := range map[string]string{"renovate": "CAT-KEY", "runner-stable-acme": "RUNNER-KEY"} {
			if got, ok, err := apps.PrivateKey(ctx, id); err != nil || !ok || got != key {
				t.Errorf("PrivateKey(%s) = %q, %v, %v", id, got, ok, err)
			}
		}
		if _, ok, err := apps.PrivateKey(ctx, "link"); err != nil || ok {
			t.Errorf("the link App has a private key: %v, %v", ok, err)
		}
		if _, ok, _ := apps.Get(ctx, "nobody"); ok {
			t.Error("an App nobody made")
		}
	})
}

// A record written before records had a purpose reads back with the one its
// key implies, and the key is where the layout 5 mapping puts an App.
func TestARecordWithoutAPurposeReadsByItsKey(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		set := e.open(t)
		b := portstore.New(set)
		for key, raw := range map[string]string{
			"app.gh.runner.stable.acme": `{"v":1,"record":{"version":1,"tier":"stable","org":"acme","app_id":3,"app_slug":"acme-stable","installation_id":4}}`,
			"app.gh.cat.renovate":       `{"v":1,"record":{"version":1,"id":"renovate","org":"acme","app_id":8,"app_slug":"acme-renovate"}}`,
			"app.gh.link":               `{"v":1,"record":{"version":1,"owner":"acme","app_id":5,"app_slug":"acme-link","client_id":"Iv1.x"}}`,
		} {
			if _, err := set.State.Create(ctx, key, []byte(raw), 0); err != nil {
				t.Fatal(err)
			}
			addr, err := port.Locate5(key)
			if err != nil || addr.Module != port.ModuleGitHub || addr.Kind != "app" {
				t.Fatalf("Locate5(%q) = %+v, %v", key, addr, err)
			}
		}
		list, err := portstore.NewGitHubApps(b).List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, a := range list {
			got = append(got, a.ID+":"+string(a.Purpose))
		}
		want := []string{"link:link", "renovate:catalogue", "runner-stable-acme:runner"}
		if !slices.Equal(got, want) {
			t.Fatalf("List = %v, want %v", got, want)
		}
	})
}

func TestGitHubAppIdsDoNotCollide(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		b := e.base(t)
		cat := portstore.NewGitHubCatalogueApps(b)
		for _, id := range []string{"link", "runner-stable-acme", "runner-x"} {
			rec := catalogueapp.Record{ID: id, Org: "acme", AppID: 8, AppSlug: "acme-x", ConnectedAt: appsAt}
			if err := cat.Put(ctx, rec, "KEY"); !errors.Is(err, appid.ErrReserved) {
				t.Errorf("a catalogue App %q = %v, want ErrReserved", id, err)
			}
		}
		// `a-b` in `c` and `a` in `b-c` would share the id runner-a-b-c.
		runner := portstore.NewGitHubRunnerApps(b)
		first := runnerapp.Record{Tier: "a-b", Org: "c", AppID: 1, AppSlug: "s1", ConnectedAt: appsAt}
		if err := runner.Put(ctx, first, "K1"); err != nil {
			t.Fatal(err)
		}
		if err := runner.Put(ctx, first, "K1"); err != nil {
			t.Fatalf("a rewrite of the same App = %v", err)
		}
		second := runnerapp.Record{Tier: "a", Org: "b-c", AppID: 2, AppSlug: "s2", ConnectedAt: appsAt}
		if err := runner.Put(ctx, second, "K2"); err == nil || !strings.Contains(err.Error(), "runner-a-b-c") {
			t.Errorf("a second App with the id runner-a-b-c = %v", err)
		}
	})
}

func TestAppLabelsAreChecked(t *testing.T) {
	rec := catalogueapp.Record{ID: "renovate", Org: "acme", AppID: 8, AppSlug: "s", Labels: map[string]string{"Team": "x"}}
	if _, err := catalogueapp.Encode(rec, "KEY"); err == nil {
		t.Error("a label key with a capital was accepted")
	}
	rec.Labels = map[string]string{"team": "platform", "tier": ""}
	keys, err := catalogueapp.Encode(rec, "KEY")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(keys[catalogueapp.RecordKey("renovate")], []byte(`"purpose":"catalogue"`)) {
		t.Errorf("the record has no purpose: %s", keys[catalogueapp.RecordKey("renovate")])
	}
}

// On layout v5 every App's credential is internal/github/apps/<id>/<ref> and
// an exported App is external/github/<id>, one id space for the three kinds.
func TestGitHubAppsOnLayoutV5(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		set := e.open(t)
		root := memory.New()
		stores := secretstore.FromStoreV5(root, "")
		b := portstore.New(set).WithV5(stores).ExportGitHubApps(func(id string) bool { return id == "renovate" })
		putAllThreeKinds(t, b)

		if got := under(t, root, "internal", "github", "apps", "link"); len(got) != 1 {
			t.Errorf("the link App's credential = %v, want one", got)
		}
		for _, id := range []string{"renovate", "runner-stable-acme"} { // exported: stored once
			if got := under(t, root, "internal", "github", "apps", id); len(got) != 0 {
				t.Errorf("the exported App %s is also internal: %v", id, got)
			}
		}
		if got, want := under(t, root, "external", "github"), []string{"renovate", "runner-stable-acme"}; !slices.Equal(got, want) {
			t.Errorf("external = %v, want %v", got, want)
		}
		if paths, _ := set.Secrets.List(ctx, ""); len(paths) != 0 {
			t.Errorf("a credential went through the Secrets port: %v", paths)
		}
		doc, _, err := stores.GitHubExternal().App("runner-stable-acme").Get(ctx)
		if err != nil || doc.AppID != "3" || doc.InstallationID != "4" || doc.PrivateKey != "RUNNER-KEY" {
			t.Fatalf("the runner document = %+v, %v", doc, err)
		}

		apps := portstore.NewGitHubApps(b)
		for id, key := range map[string]string{"renovate": "CAT-KEY", "runner-stable-acme": "RUNNER-KEY"} {
			if got, ok, err := apps.PrivateKey(ctx, id); err != nil || !ok || got != key {
				t.Errorf("PrivateKey(%s) = %q, %v, %v", id, got, ok, err)
			}
		}
		if cred, ok, err := portstore.NewGitHubOrgs(b).LinkAppCredential(ctx); err != nil || !ok || cred.ClientSecret != "LINK-SECRET" {
			t.Errorf("the link App's credential = %v, %v", ok, err)
		}

		// A pending catalogue App is internal, under a fresh ref per write.
		pending := catalogueapp.Record{ID: "kept", Org: "acme", AppID: 9, AppSlug: "acme-kept", ConnectedAt: appsAt}
		cat := portstore.NewGitHubCatalogueApps(b)
		for range 2 {
			if err = cat.Put(ctx, pending, "KEPT-KEY"); err != nil {
				t.Fatal(err)
			}
		}
		kept := under(t, root, "internal", "github", "apps", "kept")
		if len(kept) != 1 {
			t.Errorf("a rewrite left %v, want one credential", kept)
		}

		// Deleting an App takes its document and its credential.
		if err = portstore.NewGitHubRunnerApps(b).Delete(ctx, "stable", "acme"); err != nil {
			t.Fatal(err)
		}
		if _, _, err = stores.GitHubExternal().App("runner-stable-acme").Get(ctx); !errors.Is(err, state.ErrNotFound) {
			t.Errorf("the document outlived the App: %v", err)
		}
		if err = cat.Delete(ctx, "kept"); err != nil {
			t.Fatal(err)
		}
		if got := under(t, root, "internal", "github", "apps", "kept"); len(got) != 0 {
			t.Errorf("the credential outlived the App: %v", got)
		}
	})
}

// An organisation names an App: the key is kept once, under the App.
func TestAnOrganisationReferencesAnApp(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		set := e.open(t)
		stores := secretstore.FromStoreV5(memory.New(), "")
		b := portstore.New(set).WithV5(stores)
		orgs := portstore.NewGitHubOrgs(b)
		rec := connection.Record{
			Org: "acme", AppID: 8, AppSlug: "acme-renovate", InstallationID: 11, ConnectedAt: appsAt, ConnectedBy: "ada@acme.example",
			AppRef: "renovate",
		}
		cred := connection.Credential{Org: "acme"}

		if err := orgs.Put(ctx, rec, cred); err == nil {
			t.Fatal("an organisation named an App nobody has")
		}
		if err := orgs.Put(ctx, connection.Record{Org: "acme", AppID: 8, AppSlug: "s"}, connection.Credential{Org: "acme", PrivateKey: "ORG-KEY"}); err == nil {
			t.Fatal("layout v5 kept a key under an organisation")
		}
		crec := catalogueapp.Record{ID: "renovate", Org: "acme", AppID: 8, AppSlug: "acme-renovate", InstallationID: 11, ConnectedAt: appsAt}
		if err := portstore.NewGitHubCatalogueApps(b).Put(ctx, crec, "CAT-KEY"); err != nil {
			t.Fatal(err)
		}
		if err := orgs.Put(ctx, connection.Record{Org: "acme", AppID: 8, AppSlug: "s", AppRef: appid.LinkID}, cred); err == nil {
			t.Fatal("an organisation named the link App")
		}
		if err := orgs.Put(ctx, rec, cred); err != nil {
			t.Fatal(err)
		}
		got, ok, err := orgs.Credential(ctx, "acme")
		if err != nil || !ok || got.PrivateKey != "CAT-KEY" || got.AppID != 8 || got.InstallationID != 11 {
			t.Fatalf("Credential = %+v, %v, %v", got, ok, err)
		}
		if _, _, err = orgs.SetOwner(ctx, "acme", "ws1"); err != nil {
			t.Fatal(err)
		}
		if list, err := orgs.List(ctx); err != nil || len(list) != 1 || list[0].AppRef != "renovate" || list[0].Owner != "ws1" {
			t.Fatalf("List = %+v, %v", list, err)
		}
		// Forgetting the organisation leaves the App, which other organisations use.
		if err = orgs.Delete(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := portstore.NewGitHubApps(b).PrivateKey(ctx, "renovate"); !ok {
			t.Error("deleting the organisation deleted the App's key")
		}
	})
}
