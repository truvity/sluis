package server

import (
	"strings"
	"testing"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/rpcmount"
)

// A catalogue App an owner renamed on GitHub after creating it: the page
// links the settings and the install under the new slug, and the App's record
// in its Secret is brought up to date.
func TestACatalogueAppRenamedOnGitHubIsLinkedAndRecordedUnderItsNewSlug(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, client, _ := catalogueServer(t)
	createdCatalogueApp(t, server, console)
	github.mu.Lock()
	github.app = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}
	github.slug = "globex-renovate-renamed"
	github.mu.Unlock()

	app := listed(t, console, "renovate")
	if app.GetAppSlug() != "globex-renovate-renamed" || app.GetName() != "globex-renovate-renamed" ||
		app.GetSettingsUrl() != "https://github.example/organizations/globex/settings/apps/globex-renovate-renamed" ||
		app.GetHtmlUrl() != "https://github.com/apps/globex-renovate-renamed" {
		t.Errorf("the renamed App = slug %q, name %q, settings %q, page %q",
			app.GetAppSlug(), app.GetName(), app.GetSettingsUrl(), app.GetHtmlUrl())
	}
	record, err := catalogueapp.DecodeRecord(catalogueSecret(t, client)[catalogueapp.RecordKey("renovate")])
	if err != nil || record.AppSlug != "globex-renovate-renamed" || record.HTMLURL != "https://github.com/apps/globex-renovate-renamed" {
		t.Errorf("the record = %+v, %v", record, err)
	}

	callCtx, _ := rpcmount.ServerContext(operator())
	begun, err := console.BeginGitHubCatalogueAppConnect(callCtx, &directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "renovate"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !strings.HasPrefix(begun.GetUrl(), "https://github.example/apps/globex-renovate-renamed/installations/new?") {
		t.Errorf("install = %q, want the new slug", begun.GetUrl())
	}
}
