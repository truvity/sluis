package kube_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/kube"
)

// Every catalogue App is one Secret, created empty at start and labelled as
// the service's; each App's keys are its own, and forgetting one leaves the
// others; a damaged record hides no other App.
func TestCatalogueAppsAreKeptByIDInOneSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := newClient()
	store := kube.NewGitHubCatalogueApps(client)
	if err := store.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if store.SecretName() != "directory-roster-github-catalogue-apps" {
		t.Errorf("name = %s", store.SecretName())
	}
	secret, err := client.API().CoreV1().Secrets(client.Namespace()).Get(ctx, store.SecretName(), metav1.GetOptions{})
	if err != nil || secret.Labels["access-roster.truvity.github.io/kind"] != "github-catalogue-apps" {
		t.Fatalf("the Secret = %+v, %v", secret, err)
	}

	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"renovate", "releases"} {
		if err = store.Put(ctx, catalogueapp.Record{ID: id, Org: "example-org", AppID: 42, AppSlug: "example-org-" + id, ConnectedAt: at}, "pem-"+id); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}
	installed := catalogueapp.Record{ID: "renovate", Org: "example-org", AppID: 42, AppSlug: "example-org-renovate", InstallationID: 7, ConnectedAt: at}
	if err = store.Put(ctx, installed, "pem-renovate"); err != nil {
		t.Fatalf("Put installed: %v", err)
	}
	record, key, ok, err := store.Get(ctx, "renovate")
	if err != nil || !ok || key != "pem-renovate" || record.InstallationID != 7 {
		t.Errorf("Get installed = %+v, %q, %t, %v", record, key, ok, err)
	}
	if _, key, ok, _ = store.Get(ctx, "releases"); !ok || key != "pem-releases" {
		t.Errorf("Get pending = %q, %t", key, ok)
	}
	if _, _, ok, _ = store.Get(ctx, "absent"); ok {
		t.Error("an App never created was found")
	}

	secret, _ = client.API().CoreV1().Secrets(client.Namespace()).Get(ctx, store.SecretName(), metav1.GetOptions{})
	secret.Data["damaged.record.json"] = []byte("{")
	if _, err = client.API().CoreV1().Secrets(client.Namespace()).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	records, err := store.List(ctx)
	if err != nil || len(records) != 2 || records[0].ID != "releases" || records[1].ID != "renovate" {
		t.Errorf("List = %+v, %v; want both, sorted, the damaged one skipped", records, err)
	}

	if err = store.Delete(ctx, "renovate"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	secret, _ = client.API().CoreV1().Secrets(client.Namespace()).Get(ctx, store.SecretName(), metav1.GetOptions{})
	for _, key := range catalogueapp.Keys("renovate") {
		if _, left := secret.Data[key]; left {
			t.Errorf("%s outlived Delete", key)
		}
	}
	if _, ok := secret.Data["releases.pending_private_key"]; !ok {
		t.Error("forgetting one App forgot another")
	}
}

// A record missing what makes it an App is never written.
func TestAnIncompleteCatalogueAppIsNeverWritten(t *testing.T) {
	t.Parallel()
	for _, record := range []catalogueapp.Record{
		{ID: "Not.An.ID", Org: "example-org", AppID: 1, AppSlug: "s"},
		{ID: "renovate", Org: "", AppID: 1, AppSlug: "s"},
		{ID: "renovate", Org: "example-org", AppSlug: "s"},
		{ID: "renovate", Org: "example-org", AppID: 1},
	} {
		if _, err := catalogueapp.Encode(record, "pem"); err == nil {
			t.Errorf("%+v was encoded", record)
		}
	}
	if _, err := catalogueapp.Encode(catalogueapp.Record{ID: "renovate", Org: "example-org", AppID: 1, AppSlug: "s"}, ""); err == nil {
		t.Error("an App with no key was encoded")
	}
}

// The webhook secret is the App's: a rewrite of the key at install leaves it,
// forgetting the App takes it, and an App never put has none to keep.
func TestACatalogueAppsWebhookSecretSurvivesInstall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := kube.NewGitHubCatalogueApps(newClient())
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if err := store.PutWebhookSecret(ctx, "renovate", "early"); err == nil {
		t.Fatal("a secret was kept for an App never put")
	}
	pending := catalogueapp.Record{ID: "renovate", Org: "example-org", AppID: 42, AppSlug: "example-org-renovate", ConnectedAt: at}
	if err := store.Put(ctx, pending, "pem"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.WebhookSecret(ctx, "renovate"); err != nil || ok {
		t.Fatalf("a secret before any was set = %v, %v", ok, err)
	}
	if err := store.PutWebhookSecret(ctx, "renovate", "hook-1"); err != nil {
		t.Fatal(err)
	}
	installed := pending
	installed.InstallationID = 7
	if err := store.Put(ctx, installed, "pem"); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := store.WebhookSecret(ctx, "renovate"); err != nil || !ok || got != "hook-1" {
		t.Fatalf("after Install = %q, %v, %v", got, ok, err)
	}
	if _, key, _, _ := store.Get(ctx, "renovate"); key != "pem" {
		t.Errorf("key = %q", key)
	}
	if err := store.Delete(ctx, "renovate"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.WebhookSecret(ctx, "renovate"); ok {
		t.Error("the secret outlived the App")
	}
}
