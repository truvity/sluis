package console

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
)

// renamingGitHub answers GET /app with whatever slug the App has now, or with
// an error while down is set.
type renamingGitHub struct {
	mu    sync.Mutex
	slug  string
	down  bool
	reads int
}

func (g *renamingGitHub) set(slug string, down bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.slug, g.down = slug, down
}

func (g *renamingGitHub) asked() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reads
}

func startRenamingGitHub(t *testing.T, slug string) *renamingGitHub {
	t.Helper()
	fake := &renamingGitHub{slug: slug}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, _ *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.reads++
		if fake.down {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"Server Error"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 42, "slug": fake.slug, "html_url": "https://github.example/apps/" + fake.slug,
			"owner": map[string]any{"login": "acme"}, "permissions": map[string]string{"contents": "read"},
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	// githubapp.APIBase and WebBase are package variables; no test of this
	// package runs in parallel with another.
	api, web := githubapp.APIBase, githubapp.WebBase
	githubapp.APIBase, githubapp.WebBase = server.URL, "https://github.example"
	t.Cleanup(func() { githubapp.APIBase, githubapp.WebBase = api, web })
	return fake
}

// slugApps is a store of catalogue Apps, in memory, that can bring a slug up
// to date as the stores of the ports and Kubernetes do.
type slugApps struct {
	catalogueapp.Store
	mu      sync.Mutex
	records map[string]catalogueapp.Record
	key     string
	renames int
}

func (s *slugApps) List(context.Context) ([]catalogueapp.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]catalogueapp.Record, 0, len(s.records))
	for id := range s.records {
		out = append(out, s.records[id])
	}
	return out, nil
}

func (s *slugApps) Get(_ context.Context, id string) (catalogueapp.Record, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	return r, s.key, ok, nil
}

func (s *slugApps) RefreshAppSlug(_ context.Context, id string, appID int64, slug, htmlURL string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok || r.AppID != appID || r.AppSlug == slug {
		return false, nil
	}
	r.AppSlug, r.HTMLURL = slug, htmlURL
	s.records[id] = r
	s.renames++
	return true, nil
}

func (s *slugApps) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, id)
	return nil
}

func (s *slugApps) renamed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renames
}

func (s *slugApps) slug(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records[id].AppSlug
}

func testKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// renamedService is a service with one catalogue App, created as
// example-bot-roster and not installed yet.
func renamedService(t *testing.T) (*Service, *slugApps) {
	t.Helper()
	store := &slugApps{key: testKey(t), records: map[string]catalogueapp.Record{
		"example-bot": {
			Version: catalogueapp.Version, ID: "example-bot", Org: "acme", AppID: 42, AppSlug: "example-bot-roster",
			HTMLURL: "https://github.example/apps/example-bot-roster", ConnectedAt: time.Unix(1700000000, 0).UTC(),
		},
	}}
	svc := New(Deps{
		CatalogueApps: store, HTTP: http.DefaultClient,
		Catalogue: &catalogue.Catalogue{Apps: []catalogue.App{{ID: "example-bot", Org: "acme", Permissions: map[string]string{"contents": "read"}}}},
	})
	return svc, store
}

func operatorEverywhere() context.Context {
	return WithCaller(context.Background(), consolecall.Caller{Actor: "ada@example.test", Roles: []string{"operator"}})
}

// An App renamed on GitHub is shown under its new slug: the name, the settings
// page and the install link are the new slug's, and the record is brought up
// to date, once.
func TestAnAppRenamedOnGitHubIsShownAndKeptUnderItsNewSlug(t *testing.T) {
	github := startRenamingGitHub(t, "example-bot-renamed")
	svc, store := renamedService(t)

	got, err := svc.GetGitHubApp(operatorEverywhere(), &directoryrosterv1.GetGitHubAppRequest{Id: "example-bot"})
	if err != nil {
		t.Fatal(err)
	}
	app := got.GetApp()
	if app.GetAppSlug() != "example-bot-renamed" || app.GetName() != "example-bot-renamed" {
		t.Errorf("slug %q, name %q, want the new slug", app.GetAppSlug(), app.GetName())
	}
	if want := "https://github.example/organizations/acme/settings/apps/example-bot-renamed"; app.GetSettingsUrl() != want {
		t.Errorf("settings = %q, want %q", app.GetSettingsUrl(), want)
	}
	if app.GetHtmlUrl() != "https://github.example/apps/example-bot-renamed" {
		t.Errorf("page = %q", app.GetHtmlUrl())
	}
	if store.slug("example-bot") != "example-bot-renamed" || store.renamed() != 1 {
		t.Errorf("record slug %q after %d renames, want the new slug once", store.slug("example-bot"), store.renamed())
	}

	// The next page load is the cache's: GitHub is not asked again, and
	// nothing is written again.
	reads := github.asked()
	if _, err = svc.ListGitHubApps(operatorEverywhere(), &directoryrosterv1.ListGitHubAppsRequest{}); err != nil {
		t.Fatal(err)
	}
	if github.asked() != reads || store.renamed() != 1 {
		t.Errorf("a cached list asked GitHub %d times and renamed %d times", github.asked()-reads, store.renamed())
	}

	// Installing picks up under the slug GitHub gives now, even before the
	// record has caught up with a second rename.
	github.set("example-bot-again", false)
	begun, err := svc.beginCatalogueAppConnect(context.Background(), "example-bot")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(begun.url, "https://github.example/apps/example-bot-again/installations/new?") {
		t.Errorf("install = %q, want the current slug", begun.url)
	}
	if store.slug("example-bot") != "example-bot-again" {
		t.Errorf("record slug %q after the install began", store.slug("example-bot"))
	}

	// Disconnecting points at the settings page the owner deletes it on.
	github.set("example-bot-last", false)
	gone, err := svc.disconnectCatalogueApp(context.Background(), "example-bot")
	if err != nil {
		t.Fatal(err)
	}
	if gone.settingsURL != "https://github.example/organizations/acme/settings/apps/example-bot-last" {
		t.Errorf("settings after disconnect = %q", gone.settingsURL)
	}
}

// GitHub unable to answer does not fail the page: the stored slug is shown and
// linked, the reason says why, and the record is left as it is.
func TestAnAppFallsBackToItsStoredSlugWhenGitHubCannotBeAsked(t *testing.T) {
	github := startRenamingGitHub(t, "example-bot-renamed")
	github.set("example-bot-renamed", true)
	svc, store := renamedService(t)

	got, err := svc.GetGitHubApp(operatorEverywhere(), &directoryrosterv1.GetGitHubAppRequest{Id: "example-bot"})
	if err != nil {
		t.Fatalf("the page failed: %v", err)
	}
	app := got.GetApp()
	if app.GetAppSlug() != "example-bot-roster" || app.GetName() != "example-bot-roster" {
		t.Errorf("slug %q, name %q, want the stored slug", app.GetAppSlug(), app.GetName())
	}
	if want := "https://github.example/organizations/acme/settings/apps/example-bot-roster"; app.GetSettingsUrl() != want {
		t.Errorf("settings = %q, want %q", app.GetSettingsUrl(), want)
	}
	if !strings.Contains(app.GetReason(), "GitHub could not be asked") {
		t.Errorf("reason = %q", app.GetReason())
	}
	begun, err := svc.beginCatalogueAppConnect(context.Background(), "example-bot")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(begun.url, "https://github.example/apps/example-bot-roster/installations/new?") {
		t.Errorf("install = %q, want the stored slug", begun.url)
	}
	if store.slug("example-bot") != "example-bot-roster" || store.renamed() != 0 {
		t.Errorf("record slug %q after %d renames, want it untouched", store.slug("example-bot"), store.renamed())
	}
}
