package portstore_test

import (
	"testing"
	"time"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state/memory"
)

// A slug GitHub gives an App now replaces the one its record was created
// with, and nothing else of the App moves: not its key, not its installation.
// A record of another GitHub App, a slug already current and an App never
// kept are left alone.
func TestAnAppsSlugIsBroughtUpToDate(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	for _, layout := range []struct {
		name string
		base func(t *testing.T, e env) *portstore.Base
	}{
		{"v4", func(t *testing.T, e env) *portstore.Base { return e.base(t) }},
		{"v5", func(t *testing.T, e env) *portstore.Base {
			return portstore.New(e.open(t)).WithV5(secretstore.FromStoreV5(memory.New(), ""))
		}},
	} {
		t.Run(layout.name, func(t *testing.T) {
			each(t, func(t *testing.T, e env) {
				b := layout.base(t, e)
				cat := portstore.NewGitHubCatalogueApps(b)
				rec := catalogueapp.Record{
					ID: "example-bot", Org: "acme", AppID: 8, AppSlug: "example-bot-roster", InstallationID: 11,
					HTMLURL: "https://github.example/apps/example-bot-roster", ConnectedAt: now, ConnectedBy: "ada@acme.example",
				}
				if err := cat.Put(ctx, rec, "CAT-KEY"); err != nil {
					t.Fatal(err)
				}
				if done, err := cat.RefreshAppSlug(ctx, "example-bot", 9, "someone-else", ""); err != nil || done {
					t.Fatalf("another App's slug = %v, %v", done, err)
				}
				if done, err := cat.RefreshAppSlug(ctx, "example-bot", 8, "example-bot-roster", ""); err != nil || done {
					t.Fatalf("the same slug = %v, %v", done, err)
				}
				if done, err := cat.RefreshAppSlug(ctx, "nobody", 8, "x", ""); err != nil || done {
					t.Fatalf("an App never kept = %v, %v", done, err)
				}
				done, err := cat.RefreshAppSlug(ctx, "example-bot", 8, "example-bot-renamed", "https://github.example/apps/example-bot-renamed")
				if err != nil || !done {
					t.Fatalf("a renamed App = %v, %v", done, err)
				}
				got, key, ok, err := cat.Get(ctx, "example-bot")
				if err != nil || !ok || key != "CAT-KEY" || got.AppSlug != "example-bot-renamed" ||
					got.HTMLURL != "https://github.example/apps/example-bot-renamed" || got.InstallationID != 11 || got.ConnectedBy != "ada@acme.example" {
					t.Fatalf("after the rename = %+v, %q, %v, %v", got, key, ok, err)
				}

				runner := portstore.NewGitHubRunnerApps(b)
				rrec := runnerapp.Record{Tier: "stable", Org: "acme", AppID: 3, AppSlug: "acme-stable", InstallationID: 4, ConnectedAt: now}
				if err = runner.Put(ctx, rrec, "RUNNER-KEY"); err != nil {
					t.Fatal(err)
				}
				if done, err = runner.RefreshAppSlug(ctx, "stable", "acme", 3, "acme-stable-runners", ""); err != nil || !done {
					t.Fatalf("a renamed runner App = %v, %v", done, err)
				}
				listed, err := runner.List(ctx)
				if err != nil || len(listed) != 1 || listed[0].AppSlug != "acme-stable-runners" || listed[0].InstallationID != 4 {
					t.Fatalf("runner Apps = %+v, %v", listed, err)
				}
				if key, ok, err := runner.PrivateKey(ctx, "stable", "acme"); err != nil || !ok || key != "RUNNER-KEY" {
					t.Fatalf("runner key = %q, %v, %v", key, ok, err)
				}
			})
		})
	}
}
