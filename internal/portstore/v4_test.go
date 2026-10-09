package portstore_test

import (
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/secretstore"
	slackcatalogueapp "github.com/truvity/sluis/internal/slackapp/catalogueapp"
	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/memory"
)

func TestAppsExportOnLayoutV4(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		now := time.Unix(1700000000, 0).UTC()
		set := e.open(t)
		stores := secretstore.FromStore(memory.New(), secretstore.LayoutV4, "")
		b := portstore.New(set).WithV4(stores).ExportGitHubApps(func(id string) bool { return id == "renovate" })

		// A runner App: pending stays internal; installed is the document.
		runner := portstore.NewGitHubRunnerApps(b)
		rrec := runnerapp.Record{Tier: "stable", Org: "acme", AppID: 3, AppSlug: "acme-stable", ConnectedAt: now, ConnectedBy: "ada@acme.example"}
		if err := runner.Put(ctx, rrec, "RUNNER-KEY"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := stores.External.GitHubRunnerApp("stable", "acme").Get(ctx); !errors.Is(err, state.ErrNotFound) {
			t.Fatalf("a pending App is external: %v", err)
		}
		if key, ok, err := runner.PrivateKey(ctx, "stable", "acme"); err != nil || !ok || key != "RUNNER-KEY" {
			t.Fatalf("pending key = %q, %v, %v", key, ok, err)
		}
		rrec.InstallationID = 4
		if err := runner.Put(ctx, rrec, "RUNNER-KEY"); err != nil {
			t.Fatal(err)
		}
		doc, _, err := stores.External.GitHubRunnerApp("stable", "acme").Get(ctx)
		if err != nil || doc.AppID != "3" || doc.InstallationID != "4" || doc.PrivateKey != "RUNNER-KEY" {
			t.Fatalf("document = %+v, %v", doc, err)
		}
		if key, ok, err := runner.PrivateKey(ctx, "stable", "acme"); err != nil || !ok || key != "RUNNER-KEY" {
			t.Fatalf("installed key = %q, %v, %v", key, ok, err)
		}
		// Stored once: the internal credential is gone.
		if names, _ := set.Secrets.List(ctx, "credentials/github-runner-app/stable/acme"); len(names) != 0 {
			t.Fatalf("the key is also internal: %v", names)
		}

		// A catalogue App is external only with export: true.
		cat := portstore.NewGitHubCatalogueApps(b)
		for _, id := range []string{"renovate", "kept"} {
			rec := catalogueapp.Record{ID: id, Org: "acme", AppID: 8, AppSlug: "acme-" + id, InstallationID: 11, ConnectedAt: now, ConnectedBy: "ada@acme.example"}
			if err := cat.Put(ctx, rec, "KEY-"+id); err != nil {
				t.Fatal(err)
			}
			if _, key, ok, err := cat.Get(ctx, id); err != nil || !ok || key != "KEY-"+id {
				t.Fatalf("Get %s = %q, %v, %v", id, key, ok, err)
			}
		}
		if _, _, err := stores.External.GitHubApp("renovate").Get(ctx); err != nil {
			t.Fatalf("exported App is not external: %v", err)
		}
		if _, _, err := stores.External.GitHubApp("kept").Get(ctx); !errors.Is(err, state.ErrNotFound) {
			t.Fatalf("an App without export is external: %v", err)
		}

		// A Slack App's bot token is external; its client secret is not.
		slack := portstore.NewSlackCatalogueApps(b)
		srec := slackcatalogueapp.Record{
			ID: "notifier", Workspace: "acme", AppID: "A1", ClientID: "c1", AuthorizeURL: "https://slack.example/i",
			CreatedAt: now, CreatedBy: "ada@acme.example", TeamID: "T1", BotUserID: "U1",
		}
		if err := slack.Put(ctx, srec, slackcatalogueapp.Credentials{ClientSecret: "CLIENT-SECRET", BotToken: "xoxb-BOT"}); err != nil {
			t.Fatal(err)
		}
		if got, _, err := stores.External.SlackApp("notifier").Get(ctx); err != nil || got.BotToken != "xoxb-BOT" {
			t.Fatalf("slack document = %+v, %v", got, err)
		}
		if _, creds, ok, err := slack.Get(ctx, "notifier"); err != nil || !ok || creds.BotToken != "xoxb-BOT" || creds.ClientSecret != "CLIENT-SECRET" {
			t.Fatalf("slack Get = %+v, %v, %v", creds, ok, err)
		}

		// Deleting an App takes its document with it.
		if err := runner.Delete(ctx, "stable", "acme"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := stores.External.GitHubRunnerApp("stable", "acme").Get(ctx); !errors.Is(err, state.ErrNotFound) {
			t.Fatalf("the document outlived the App: %v", err)
		}
		if err := slack.Delete(ctx, "notifier"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := stores.External.SlackApp("notifier").Get(ctx); !errors.Is(err, state.ErrNotFound) {
			t.Fatalf("the Slack document outlived the App: %v", err)
		}
	})
}

// In transition both layouts hold the key: v3 readers still work.
func TestAppsKeepBothLayoutsInTransition(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		now := time.Unix(1700000000, 0).UTC()
		set := e.open(t)
		stores := secretstore.FromStore(memory.New(), secretstore.LayoutTransition, "")
		b := portstore.New(set).WithV4(stores)

		runner := portstore.NewGitHubRunnerApps(b)
		rrec := runnerapp.Record{Tier: "stable", Org: "acme", AppID: 3, AppSlug: "acme-stable", InstallationID: 4, ConnectedAt: now, ConnectedBy: "ada@acme.example"}
		if err := runner.Put(ctx, rrec, "RUNNER-KEY"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := stores.External.GitHubRunnerApp("stable", "acme").Get(ctx); err != nil {
			t.Fatal(err)
		}
		// A v3 reader (no v4) still finds the key.
		old := portstore.NewGitHubRunnerApps(portstore.New(set))
		if key, ok, err := old.PrivateKey(ctx, "stable", "acme"); err != nil || !ok || key != "RUNNER-KEY" {
			t.Fatalf("v3 reader = %q, %v, %v", key, ok, err)
		}
	})
}

// A catalogue App's webhook secret is the App's, not the key's: a rewrite of
// the key at install keeps it, it is in the exported document when there is
// one (and only there once v3 readers are gone), and the key still reads back
// whole beside it.
func TestACatalogueAppKeepsItsWebhookSecretAcrossWrites(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		now := time.Unix(1700000000, 0).UTC()
		set := e.open(t)
		stores := secretstore.FromStore(memory.New(), secretstore.LayoutV4, "")
		b := portstore.New(set).WithV4(stores).ExportGitHubApps(func(id string) bool { return id == "renovate" })
		cat := portstore.NewGitHubCatalogueApps(b)

		for _, id := range []string{"renovate", "kept"} {
			rec := catalogueapp.Record{ID: id, Org: "acme", AppID: 8, AppSlug: "acme-" + id, ConnectedAt: now, ConnectedBy: "ada@acme.example"}
			if err := cat.PutWebhookSecret(ctx, id, "early"); err == nil {
				t.Fatalf("%s: a secret was kept for an App never put", id)
			}
			if err := cat.Put(ctx, rec, "KEY-"+id); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := cat.WebhookSecret(ctx, id); err != nil || ok {
				t.Fatalf("%s: a secret before any was set = %v, %v", id, ok, err)
			}
			// Pending: set, and read back; the key is untouched.
			if err := cat.PutWebhookSecret(ctx, id, "hook-1"); err != nil {
				t.Fatalf("%s: PutWebhookSecret: %v", id, err)
			}
			if got, ok, err := cat.WebhookSecret(ctx, id); err != nil || !ok || got != "hook-1" {
				t.Fatalf("%s: pending secret = %q, %v, %v", id, got, ok, err)
			}
			if _, key, ok, err := cat.Get(ctx, id); err != nil || !ok || key != "KEY-"+id {
				t.Fatalf("%s: the key beside the secret = %q, %v, %v", id, key, ok, err)
			}
			if _, _, err := stores.External.GitHubApp(id).Get(ctx); !errors.Is(err, state.ErrNotFound) {
				t.Fatalf("%s: a pending App is external: %v", id, err)
			}

			// Install: the key is rewritten, the secret stays.
			rec.InstallationID = 11
			if err := cat.Put(ctx, rec, "KEY-"+id); err != nil {
				t.Fatal(err)
			}
			if got, ok, err := cat.WebhookSecret(ctx, id); err != nil || !ok || got != "hook-1" {
				t.Fatalf("%s: the secret after Install = %q, %v, %v", id, got, ok, err)
			}
			// Rotation on an installed App.
			if err := cat.PutWebhookSecret(ctx, id, "hook-2"); err != nil {
				t.Fatal(err)
			}
			if got, _, _ := cat.WebhookSecret(ctx, id); got != "hook-2" {
				t.Fatalf("%s: the rotated secret = %q", id, got)
			}
			if _, key, _, _ := cat.Get(ctx, id); key != "KEY-"+id {
				t.Fatalf("%s: the key after rotation = %q", id, key)
			}
		}

		// Exported: all four fields are in the one document, and a later
		// write keeps all four.
		doc, _, err := stores.External.GitHubApp("renovate").Get(ctx)
		if err != nil || doc.WebhookSecret != "hook-2" || doc.PrivateKey != "KEY-renovate" || doc.InstallationID != "11" || doc.AppID != "8" {
			t.Fatalf("exported document = %+v, %v", doc, err)
		}
		rec := catalogueapp.Record{
			ID: "renovate", Org: "acme", AppID: 8, AppSlug: "acme-renovate", InstallationID: 11, ConnectedAt: now,
			WebhookURL: "https://argocd.example/api/webhook",
		}
		if err = cat.Put(ctx, rec, "KEY-renovate"); err != nil {
			t.Fatal(err)
		}
		if doc, _, _ = stores.External.GitHubApp("renovate").Get(ctx); doc.WebhookSecret != "hook-2" {
			t.Fatalf("a later Put dropped the webhook secret from the document: %+v", doc)
		}
		if _, _, err = stores.External.GitHubApp("kept").Get(ctx); !errors.Is(err, state.ErrNotFound) {
			t.Fatalf("an App without export is external: %v", err)
		}

		// Forgetting the App forgets its secret.
		if err = cat.Delete(ctx, "renovate"); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := cat.WebhookSecret(ctx, "renovate"); err != nil || ok {
			t.Fatalf("the secret outlived the App: %v, %v", ok, err)
		}
	})
}
