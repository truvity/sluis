package kube_test

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/kube"
)

// A slug GitHub gives an App now replaces the record's, and the App's keys and
// installation stay; another GitHub App's record and a current slug are left.
func TestAnAppsSlugIsBroughtUpToDateInItsSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := newClient()
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	cat := kube.NewGitHubCatalogueApps(client)
	rec := catalogueapp.Record{ID: "example-bot", Org: "example-org", AppID: 42, AppSlug: "example-bot-roster", InstallationID: 7, ConnectedAt: at}
	if err := cat.Put(ctx, rec, "pem-bot"); err != nil {
		t.Fatal(err)
	}
	if done, err := cat.RefreshAppSlug(ctx, "example-bot", 43, "someone-else", ""); err != nil || done {
		t.Fatalf("another App = %v, %v", done, err)
	}
	if done, err := cat.RefreshAppSlug(ctx, "example-bot", 42, "example-bot-roster", ""); err != nil || done {
		t.Fatalf("the same slug = %v, %v", done, err)
	}
	if done, err := cat.RefreshAppSlug(ctx, "example-bot", 42, "example-bot-renamed", "https://github.example/apps/example-bot-renamed"); err != nil || !done {
		t.Fatalf("a renamed App = %v, %v", done, err)
	}
	got, key, ok, err := cat.Get(ctx, "example-bot")
	if err != nil || !ok || key != "pem-bot" || got.AppSlug != "example-bot-renamed" || got.InstallationID != 7 ||
		got.HTMLURL != "https://github.example/apps/example-bot-renamed" {
		t.Fatalf("after the rename = %+v, %q, %v, %v", got, key, ok, err)
	}

	runners := kube.NewGitHubRunnerApps(client)
	rrec := runnerapp.Record{Tier: "standard", Org: "example-org", AppID: 21, AppSlug: "example-org-runners", InstallationID: 8, ConnectedAt: at}
	if err = runners.Put(ctx, rrec, "pem-runner"); err != nil {
		t.Fatal(err)
	}
	if done, err := runners.RefreshAppSlug(ctx, "standard", "example-org", 21, "example-org-runners-renamed", ""); err != nil || !done {
		t.Fatalf("a renamed runner App = %v, %v", done, err)
	}
	listed, err := runners.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].AppSlug != "example-org-runners-renamed" || listed[0].InstallationID != 8 {
		t.Fatalf("runner Apps = %+v, %v", listed, err)
	}
	if key, ok, err := runners.PrivateKey(ctx, "standard", "example-org"); err != nil || !ok || key != "pem-runner" {
		t.Fatalf("runner key = %q, %v, %v", key, ok, err)
	}
}
