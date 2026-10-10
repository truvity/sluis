package server

import (
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubroster/appid"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/githubroster/runnerapp"
	"github.com/truvity/sluis/internal/kube"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/portstore"
)

// appsRig is a deployment with one of each kind of App: the link App, the
// controller App of a bound organisation, that organisation's stable
// runner App, and the catalogue's two.
type appsRig struct {
	server  *ConsoleServer
	console *Console
	github  *fakeGitHub
	client  *kube.Client
}

func newAppsRig(t *testing.T) *appsRig {
	t.Helper()
	github := startFakeGitHub(t)
	server, console := connectServer(t, newMemoryConnections())
	client := kube.NewClient(fake.NewClientset(), "access-issuer", "access-issuer")
	declared, err := catalogue.Parse([]byte(testCatalogue))
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	console.deps.GitHubCatalogue = declared
	console.deps.GitHubCatalogueApps = kube.NewGitHubCatalogueApps(client)
	console.deps.GitHubRunnerApps = kube.NewGitHubRunnerApps(client)
	console.deps.GitHubRunnerTiers = []string{"preview", "stable"}
	console.deps.GitHubLinkApp, console.deps.GitHubLinks = &memoryLinkApp{}, &memoryLinks{byID: map[int64]link.Link{}}
	console.deps.Audit = audittest.New(t)
	return &appsRig{server: server, console: console, github: github, client: client}
}

// connect puts the organisation's App in place, installed, with a key the
// fake GitHub's answers are read with.
func (r *appsRig) connect(t *testing.T, key string) {
	t.Helper()
	err := r.console.deps.GitHubOrgs.Put(context.Background(),
		connection.Record{Org: "globex", AppID: 42, AppSlug: "globex-access-roster", InstallationID: 7, ConnectedBy: "ada@north.example"},
		connection.Credential{Org: "globex", AppID: 42, InstallationID: 7, PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}
}

// runner puts one tier's App in place, installed.
func (r *appsRig) runner(t *testing.T, tier, key string) {
	t.Helper()
	err := r.console.deps.GitHubRunnerApps.Put(context.Background(), runnerapp.Record{
		Tier: tier, Org: "globex", AppID: 43, AppSlug: "globex-runners-" + tier, InstallationID: 7,
		ConnectedAt: time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC), ConnectedBy: "ada@north.example",
	}, key)
	if err != nil {
		t.Fatal(err)
	}
}

// link puts the link App in place, with one account linked through it.
func (r *appsRig) link(t *testing.T) {
	t.Helper()
	err := r.console.deps.GitHubLinkApp.PutLinkApp(context.Background(),
		link.App{Owner: "globex", AppID: 9, AppSlug: "globex-access-roster-link", ClientID: "Iv1.test", ConnectedBy: "ada@north.example"},
		link.AppCredential{AppID: 9, ClientID: "Iv1.test", ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.console.deps.GitHubLinks.Claim(context.Background(), link.Link{
		ID: 11, Login: "ada-north", AppID: 9, Emails: []string{"ada@globex.example"}, State: link.StateLinked,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// catalogueApp puts one catalogue App in place, installed.
func (r *appsRig) catalogueApp(t *testing.T, id, key string) {
	t.Helper()
	err := r.console.deps.GitHubCatalogueApps.Put(context.Background(), catalogueapp.Record{
		ID: id, Org: "globex", AppID: 44, AppSlug: "globex-" + id, InstallationID: 7, ConnectedBy: "ada@north.example",
	}, key)
	if err != nil {
		t.Fatal(err)
	}
}

// list is the Apps list, by id.
func (r *appsRig) list(t *testing.T) (map[string]*directoryrosterv1.GitHubApp, *directoryrosterv1.ListGitHubAppsResponse) {
	t.Helper()
	listed, err := r.console.ListGitHubApps(operator(), connect.NewRequest(&directoryrosterv1.ListGitHubAppsRequest{}))
	if err != nil {
		t.Fatalf("ListGitHubApps: %v", err)
	}
	byID := map[string]*directoryrosterv1.GitHubApp{}
	for _, app := range listed.Msg.GetApps() {
		if _, twice := byID[app.GetId()]; twice {
			t.Errorf("two Apps share the id %s", app.GetId())
		}
		byID[app.GetId()] = app
	}
	return byID, listed.Msg
}

func ids(apps map[string]*directoryrosterv1.GitHubApp) []string {
	out := make([]string, 0, len(apps))
	for id := range apps {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// One list, four kinds of App: what each is for, where it stands, and the
// Secret its key is in — the same shape whether the deployment declared it
// in a catalogue or this service declares it itself.
func TestTheAppsListCarriesEveryKindOfAppInOneShape(t *testing.T) {
	rig := newAppsRig(t)
	rig.connect(t, rig.github.pem)
	rig.runner(t, "stable", rig.github.pem)
	rig.link(t)
	rig.github.app = map[string]string{"members": "write", "organization_administration": "read", "metadata": "read"}
	rig.github.installed = rig.github.app

	apps, listed := rig.list(t)
	want := []string{
		"acme-controller", "acme-runners-preview", "acme-runners-stable",
		"globex-controller", "globex-runners-preview", "globex-runners-stable",
		"link", "releases", "renovate",
	}
	if got := ids(apps); !slices.Equal(got, want) {
		t.Errorf("the list = %v, want %v", got, want)
	}
	if !listed.GetConnectingAvailable() || !listed.GetLinkingAvailable() || !listed.GetCatalogueAvailable() ||
		!slices.Equal(listed.GetRunnerTiers(), []string{"preview", "stable"}) ||
		!slices.Equal(listed.GetBoundOrganisations(), []string{"acme", "globex"}) ||
		listed.GetLinkUrl() != "https://access.example/connect/github/link" {
		t.Errorf("what comes with the list = %+v", listed)
	}

	if app := apps["link"]; app.GetPurpose() != appLink || app.GetOrigin() != appPreset || app.GetOrg() != "globex" ||
		app.GetState() != appInstalled || app.GetAttention() != appDone || app.GetLinkedAccounts() != 1 ||
		app.GetName() != "globex-access-roster-link" {
		t.Errorf("the link App = %+v", apps["link"])
	}
	if app := apps["globex-controller"]; app.GetPurpose() != appController || app.GetState() != appInstalled ||
		app.GetAttention() != appDone || app.GetInstallationId() != 7 || app.GetConnectedBy() != "ada@north.example" ||
		app.GetSettingsUrl() != "https://github.example/organizations/globex/settings/apps/globex-access-roster" {
		t.Errorf("the controller App = %+v", apps["globex-controller"])
	}
	if app := apps["globex-runners-stable"]; app.GetPurpose() != appRunners || app.GetTier() != "stable" ||
		app.GetSecret() != "access-issuer-github-runner-apps" ||
		!slices.Contains(app.GetSecretKeys(), "stable.globex.github_app_private_key") {
		t.Errorf("the stable runner App = %+v", apps["globex-runners-stable"])
	}
	// Declared and never created: the permissions it will ask for are
	// shown before it exists, which is the point of asking an owner for
	// two clicks.
	preview := apps["globex-runners-preview"]
	if preview.GetState() != appNotCreated || preview.GetAttention() != appNeedsYou || preview.GetName() != "globex-runners-preview" ||
		len(preview.GetPermissions()) != 1 || preview.GetPermissions()[0].GetDeclared() != "write" {
		t.Errorf("a runner App nobody has created = %+v", preview)
	}
	if app := apps["renovate"]; app.GetPurpose() != appTokens || app.GetOrigin() != appCatalogue ||
		app.GetSecret() != "access-issuer-github-catalogue-apps" || len(app.GetGrants()) != 2 ||
		!app.GetGrants()[0].GetGroupDeclared() || app.GetGrants()[1].GetGroupDeclared() {
		t.Errorf("a catalogue App = %+v", apps["renovate"])
	}
	// One App, one page: the list's id fetches exactly the same App.
	got, err := rig.console.GetGitHubApp(operator(), connect.NewRequest(&directoryrosterv1.GetGitHubAppRequest{Id: "globex-runners-stable"}))
	if err != nil || got.Msg.GetApp().GetTier() != "stable" {
		t.Errorf("GetGitHubApp = %+v, %v", got.Msg.GetApp(), err)
	}
	if _, err = rig.console.GetGitHubApp(operator(),
		connect.NewRequest(&directoryrosterv1.GetGitHubAppRequest{Id: "no-such-app"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an id nothing declares = %v, want not found", err)
	}
}

// A deployment that keeps nothing in Kubernetes still says which Apps the
// policy asks for — that is what the page is for — and says that none of
// them can be created here, rather than listing nothing at all.
func TestAnAppsListWithoutStoresStillNamesWhatThePolicyAsksFor(t *testing.T) {
	startFakeGitHub(t)
	_, console := connectServer(t, nil)
	console.deps.GitHubOrgs = nil
	listed, err := console.ListGitHubApps(operator(), connect.NewRequest(&directoryrosterv1.ListGitHubAppsRequest{}))
	if err != nil {
		t.Fatalf("ListGitHubApps: %v", err)
	}
	if listed.Msg.GetConnectingAvailable() || listed.Msg.GetLinkingAvailable() || listed.Msg.GetCatalogueAvailable() ||
		len(listed.Msg.GetRunnerTiers()) != 0 || listed.Msg.GetLinkUrl() != "" {
		t.Errorf("without stores = %+v", listed.Msg)
	}
	byID := map[string]*directoryrosterv1.GitHubApp{}
	for _, app := range listed.Msg.GetApps() {
		byID[app.GetId()] = app
	}
	if !slices.Equal(ids(byID), []string{"acme-controller", "globex-controller"}) {
		t.Errorf("the list = %v, want the bound organisations' controller Apps alone", ids(byID))
	}
	// Nowhere to keep a key, so nowhere for the page to send anybody.
	if app := byID["globex-controller"]; app.GetState() != appNotCreated || app.GetSecret() != "" {
		t.Errorf("a controller App with nowhere to keep its key = %+v", app)
	}

	viewer := WithIdentity(context.Background(), access.Identity{Role: access.RoleNone})
	if _, err = console.ListGitHubApps(viewer,
		connect.NewRequest(&directoryrosterv1.ListGitHubAppsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a caller with no role = %v, want permission denied", err)
	}
}

// A catalogue id is the operator's own name for an App, so it wins the id
// and the preset that wanted it moves aside — both still reachable, and
// each still itself.
func TestACatalogueIdWinsACollisionAndThePresetMovesAside(t *testing.T) {
	rig := newAppsRig(t)
	declared, err := catalogue.Parse([]byte(`
apps:
  - id: link
    org: globex
    permissions: {contents: read}
  - id: globex-controller
    org: globex
    permissions: {contents: read}
`))
	if err != nil {
		t.Fatal(err)
	}
	rig.console.deps.GitHubCatalogue = declared

	apps, _ := rig.list(t)
	if app := apps["link"]; app.GetOrigin() != appCatalogue || app.GetPurpose() != appTokens {
		t.Errorf("the catalogue's own `link` = %+v", app)
	}
	if app := apps["link-preset"]; app.GetOrigin() != appPreset || app.GetPurpose() != appLink {
		t.Errorf("the link App beside it = %+v", app)
	}
	if app := apps["globex-controller-preset"]; app.GetOrigin() != appPreset || app.GetPurpose() != appController {
		t.Errorf("the controller App beside it = %+v", app)
	}
}

// The service's own Apps are checked against their declaration exactly as
// a catalogue App is: GitHub is asked what each holds, and what it answers
// is compared with the manifest this service would create it from.
func TestThePresetAppsAreCheckedAgainstTheirOwnDeclaration(t *testing.T) {
	rig := newAppsRig(t)
	rig.connect(t, rig.github.pem)
	rig.runner(t, "stable", rig.github.pem)
	rig.link(t)
	// One answer for every App, as an organisation whose owner left the
	// controller App alone and cut the runner App down to read.
	rig.github.app = map[string]string{"members": "write", "organization_administration": "read", "metadata": "read"}
	rig.github.installed = rig.github.app

	apps, _ := rig.list(t)
	controller := apps["globex-controller"]
	if controller.GetState() != appInstalled || len(controller.GetDrift()) != 0 || controller.GetReason() != "" ||
		controller.GetRepositorySelection() != "selected" {
		t.Errorf("a controller App holding what it declares = %+v", controller)
	}
	rows := map[string]*directoryrosterv1.GitHubAppPermission{}
	for _, row := range controller.GetPermissions() {
		rows[row.GetName()] = row
	}
	if rows["members"].GetDeclared() != "write" || rows["members"].GetApp() != "write" || rows["members"].GetInstallation() != "write" {
		t.Errorf("the controller App's permissions = %v", controller.GetPermissions())
	}

	runner := apps["globex-runners-stable"]
	drift := strings.Join(runner.GetDrift(), "\n")
	if runner.GetState() != appDrifted || runner.GetAttention() != appNeedsYou ||
		!strings.Contains(drift, "The App lacks organization_self_hosted_runners: write") ||
		!strings.Contains(drift, "which this service does not declare") {
		t.Errorf("a runner App an owner edited = %s:\n%s", runner.GetState(), drift)
	}

	// The link App is used by being authorized, never as the App, so this
	// service holds no key for it: it says so rather than reporting a
	// match nobody checked.
	linkApp := apps["link"]
	if len(linkApp.GetDrift()) != 0 || !strings.Contains(linkApp.GetReason(), "keeps no App key") || linkApp.GetState() != appInstalled {
		t.Errorf("the link App = %+v", linkApp)
	}

	// Somebody uninstalled the controller App on GitHub.
	rig.github.installed = nil
	checked, err := rig.console.CheckGitHubApp(operator(), connect.NewRequest(&directoryrosterv1.CheckGitHubAppRequest{Id: "globex-controller"}))
	if err != nil {
		t.Fatalf("CheckGitHubApp: %v", err)
	}
	if !strings.Contains(strings.Join(checked.Msg.GetApp().GetDrift(), "\n"), "installation is gone on GitHub") {
		t.Errorf("an uninstalled controller App = %v", checked.Msg.GetApp().GetDrift())
	}
}

// A preset whose key cannot be read says so, and claims no match: a
// declaration nobody checked is not a declaration that holds.
func TestAPresetWhoseKeyCannotBeReadSaysSoRatherThanMatching(t *testing.T) {
	rig := newAppsRig(t)
	rig.connect(t, "not a key")
	rig.github.app = map[string]string{"members": "write"}

	apps, _ := rig.list(t)
	controller := apps["globex-controller"]
	if controller.GetState() != appInstalled || len(controller.GetDrift()) != 0 || !strings.Contains(controller.GetReason(), "key") ||
		!strings.Contains(controller.GetStateDetail(), "key") {
		t.Errorf("a controller App with an unusable key = %+v", controller)
	}
	if rig.github.reads != 0 {
		t.Errorf("GitHub was asked with a key that does not sign: %d calls", rig.github.reads)
	}
}

// An App for an organisation the policy no longer binds, or a tier the
// deployment no longer declares, is listed so it can be disconnected — and
// nothing is asked of GitHub about a declaration that no longer exists.
func TestAnAppNothingDeclaresIsListedToBeDisconnected(t *testing.T) {
	rig := newAppsRig(t)
	rig.runner(t, "retired", rig.github.pem)
	err := rig.console.deps.GitHubOrgs.Put(context.Background(),
		connection.Record{Org: "dropped-org", AppID: 42, AppSlug: "dropped-org-access-roster", InstallationID: 7},
		connection.Credential{Org: "dropped-org", AppID: 42, InstallationID: 7, PrivateKey: rig.github.pem})
	if err != nil {
		t.Fatal(err)
	}

	apps, _ := rig.list(t)
	retired := apps["globex-runners-retired"]
	if retired.GetDeclared() || retired.GetAttention() != appNeedsYou || len(retired.GetPermissions()) != 0 ||
		!strings.Contains(retired.GetStateDetail(), "no longer declared") {
		t.Errorf("a runner App for a tier nothing declares = %+v", retired)
	}
	dropped := apps["dropped-org-controller"]
	if dropped.GetDeclared() || !strings.Contains(dropped.GetStateDetail(), "no longer bound in the policy") {
		t.Errorf("a controller App for an unbound organisation = %+v", dropped)
	}
	if rig.github.reads != 0 {
		t.Errorf("GitHub was asked about an App nothing declares: %d calls", rig.github.reads)
	}
}

// The generic Begin starts the same flow the per-kind one does, under the
// same signed state: a browser part-way through when the pod rolls
// finishes at the callback it was always going to.
func TestTheGenericBeginStartsTheSameFlowAsThePerKindOne(t *testing.T) {
	rig := newAppsRig(t)
	for _, c := range []struct{ name, id, owner, bind string }{
		{name: "the controller App", id: "globex-controller", bind: "github:globex"},
		{name: "a runner App", id: "globex-runners-stable", bind: "github-runner:stable:globex"},
		{name: "the link App", id: "link", owner: "globex", bind: "github-link-app:globex"},
		{name: "a catalogue App", id: "renovate", bind: "github-catalogue:renovate"},
	} {
		begun, err := rig.console.BeginGitHubAppConnect(operator(),
			connect.NewRequest(&directoryrosterv1.BeginGitHubAppConnectRequest{Id: c.id, Owner: c.owner}))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !strings.HasPrefix(begun.Msg.GetUrl(), "https://github.example/organizations/globex/settings/apps/new?state=") || begun.Msg.GetManifest() == "" {
			t.Errorf("%s = %+v, want globex's create page and a manifest", c.name, begun.Msg)
		}
		state := mustQuery(t, begun.Msg.GetUrl(), "state")
		if cookie := cookieFrom(t, begun.Header()); cookie != state {
			t.Errorf("%s pinned the flow to %q, not to its state", c.name, cookie)
		}
		binding, err := rig.console.deps.State.VerifyBinding(state)
		if err != nil || binding.Bind != c.bind || binding.Actor != "ada@north.example" {
			t.Errorf("%s binds %+v, %v; want %s", c.name, binding, err, c.bind)
		}
	}

	// The state the generic call issued finishes at the callback the
	// per-kind flow has always used.
	begun, err := rig.console.BeginGitHubAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubAppConnectRequest{Id: "renovate"}))
	if err != nil {
		t.Fatal(err)
	}
	created := redirect(rig.server.githubCatalogueCallback, githubCatalogueCallbackPath,
		url.Values{"code": {"created"}, "state": {mustQuery(t, begun.Msg.GetUrl(), "state")}}, cookieFrom(t, begun.Header()))
	if created.Code != 302 || !strings.Contains(created.Header().Get("Location"), "/installations/new?state=") {
		t.Fatalf("a generic connect finished at the catalogue callback = %d:\n%s", created.Code, created.Body)
	}

	// Only an operator, and only an App there is.
	viewer := WithIdentity(context.Background(), access.Identity{Role: access.RoleViewer})
	if _, err = rig.console.BeginGitHubAppConnect(viewer,
		connect.NewRequest(&directoryrosterv1.BeginGitHubAppConnectRequest{Id: "renovate"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer's Begin = %v, want permission denied", err)
	}
	if _, err = rig.console.BeginGitHubAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubAppConnectRequest{Id: "nothing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("Begin on an App there is none of = %v, want not found", err)
	}
}

// One Disconnect for all four kinds: each forgets what its own store
// keeps, uninstalls where there is an installation, and says where the
// App itself is deleted.
func TestTheGenericDisconnectForgetsEveryKindOfApp(t *testing.T) {
	rig := newAppsRig(t)
	rig.connect(t, rig.github.pem)
	rig.runner(t, "stable", rig.github.pem)
	rig.link(t)
	rig.catalogueApp(t, "renovate", rig.github.pem)

	for _, c := range []struct {
		name, id, settings string
		uninstalls         bool
	}{
		{name: "the controller App", id: "globex-controller", uninstalls: true,
			settings: "https://github.example/organizations/globex/settings/apps/globex-access-roster"},
		{name: "a runner App", id: "globex-runners-stable", uninstalls: true,
			settings: "https://github.example/organizations/globex/settings/apps/globex-runners-stable"},
		{name: "a catalogue App", id: "renovate", uninstalls: true,
			settings: "https://github.example/organizations/globex/settings/apps/globex-renovate"},
		// The link App is installed nowhere, so there is nothing to
		// uninstall; what it leaves behind is links nobody can check.
		{name: "the link App", id: "link", uninstalls: false,
			settings: "https://github.example/organizations/globex/settings/apps/globex-access-roster-link"},
	} {
		gone, err := rig.console.DisconnectGitHubApp(operator(), connect.NewRequest(&directoryrosterv1.DisconnectGitHubAppRequest{Id: c.id}))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if gone.Msg.GetUninstalled() != c.uninstalls || gone.Msg.GetAppSettingsUrl() != c.settings {
			t.Errorf("%s = %+v", c.name, gone.Msg)
		}
	}
	if invalidated := rig.lastLinkState(t); invalidated != link.StateUnverifiable {
		t.Errorf("the links outlived the link App as %q", invalidated)
	}
	if !slices.Equal(rig.github.uninstalled, []string{"7", "7", "7"}) {
		t.Errorf("uninstalls = %v, want one for each installed App", rig.github.uninstalled)
	}

	apps, _ := rig.list(t)
	for _, id := range []string{"globex-controller", "globex-runners-stable", "renovate", "link"} {
		if apps[id].GetState() != appNotCreated {
			t.Errorf("%s survived Disconnect as %s", id, apps[id].GetState())
		}
	}
	// Nothing left to disconnect.
	if _, err := rig.console.DisconnectGitHubApp(operator(),
		connect.NewRequest(&directoryrosterv1.DisconnectGitHubAppRequest{Id: "renovate"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("disconnecting what is gone = %v, want not found", err)
	}
}

func (r *appsRig) lastLinkState(t *testing.T) link.State {
	t.Helper()
	links, err := r.console.deps.GitHubLinks.List(context.Background())
	if err != nil || len(links) == 0 {
		t.Fatalf("links = %v, %v", links, err)
	}
	return links[0].State
}

// The calls the console used to make still answer, so a console and a
// service mid-rollout are never the same broken page.
func TestTheSupersededGitHubCallsStillAnswer(t *testing.T) {
	rig := newAppsRig(t)
	rig.connect(t, rig.github.pem)
	rig.runner(t, "stable", rig.github.pem)
	rig.link(t)
	rig.catalogueApp(t, "renovate", rig.github.pem)
	rig.github.app = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}
	rig.github.installed = rig.github.app

	status, err := rig.console.GetGitHubStatus(operator(), connect.NewRequest(&directoryrosterv1.GetGitHubStatusRequest{}))
	if err != nil {
		t.Fatalf("GetGitHubStatus: %v", err)
	}
	if status.Msg.GetLinkApp().GetAppSlug() != "globex-access-roster-link" || len(status.Msg.GetRunnerApps()) != 1 ||
		!status.Msg.GetRunnerApps()[0].GetInstalled() || len(status.Msg.GetCatalogueApps()) != 2 {
		t.Errorf("the App-shaped fields = %+v", status.Msg)
	}
	if organisation(t, status.Msg, "globex").GetConnection().GetAppSlug() != "globex-access-roster" {
		t.Errorf("the organisation's connection = %+v", organisation(t, status.Msg, "globex").GetConnection())
	}
	var renovate *directoryrosterv1.GitHubCatalogueApp
	for _, app := range status.Msg.GetCatalogueApps() {
		if app.GetId() == "renovate" {
			renovate = app
		}
	}
	if renovate.GetState() != catalogueInstalled || renovate.GetName() != "globex-renovate" || len(renovate.GetGrants()) != 2 {
		t.Errorf("a catalogue App on the status = %+v", renovate)
	}

	checked, err := rig.console.CheckGitHubCatalogueApp(operator(), connect.NewRequest(&directoryrosterv1.CheckGitHubCatalogueAppRequest{Id: "renovate"}))
	if err != nil || checked.Msg.GetApp().GetState() != catalogueInstalled {
		t.Errorf("CheckGitHubCatalogueApp = %+v, %v", checked.Msg.GetApp(), err)
	}
	for _, c := range []struct {
		name string
		call func() error
	}{
		{"the organisation's Begin", func() error {
			_, err := rig.console.BeginGitHubConnect(operator(), connect.NewRequest(&directoryrosterv1.BeginGitHubConnectRequest{Org: "acme"}))
			return err
		}},
		{"a runner App's Begin", func() error {
			_, err := rig.console.BeginGitHubRunnerAppConnect(operator(),
				connect.NewRequest(&directoryrosterv1.BeginGitHubRunnerAppConnectRequest{Org: "globex", Tier: "preview"}))
			return err
		}},
		{"a catalogue App's Begin", func() error {
			_, err := rig.console.BeginGitHubCatalogueAppConnect(operator(),
				connect.NewRequest(&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "releases"}))
			return err
		}},
		{"the runner App's Disconnect", func() error {
			_, err := rig.console.DisconnectGitHubRunnerApp(operator(),
				connect.NewRequest(&directoryrosterv1.DisconnectGitHubRunnerAppRequest{Org: "globex", Tier: "stable"}))
			return err
		}},
		{"the organisation's Disconnect", func() error {
			_, err := rig.console.DisconnectGitHubOrganisation(operator(),
				connect.NewRequest(&directoryrosterv1.DisconnectGitHubOrganisationRequest{Org: "globex"}))
			return err
		}},
		{"the link App's Disconnect", func() error {
			_, err := rig.console.DisconnectGitHubLinkApp(operator(), connect.NewRequest(&directoryrosterv1.DisconnectGitHubLinkAppRequest{}))
			return err
		}},
		{"a catalogue App's Disconnect", func() error {
			_, err := rig.console.DisconnectGitHubCatalogueApp(operator(),
				connect.NewRequest(&directoryrosterv1.DisconnectGitHubCatalogueAppRequest{Id: "renovate"}))
			return err
		}},
	} {
		if err = c.call(); err != nil {
			t.Errorf("%s = %v", c.name, err)
		}
	}
}

// A group's page says which GitHub tokens it may mint, from the
// catalogue: the reverse of an App's grants, which nothing in the policy
// carries.
func TestAGroupCarriesTheGitHubTokensItMayMint(t *testing.T) {
	rig := newAppsRig(t)
	policy, err := rig.console.GetPolicy(operator(), connect.NewRequest(&directoryrosterv1.GetPolicyRequest{}))
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	byName := map[string]*directoryrosterv1.PolicyGroup{}
	for _, group := range policy.Msg.GetGroups() {
		byName[group.GetName()] = group
	}
	grants := byName["all:platform:engineer"].GetGithubGrants()
	if len(grants) != 1 || grants[0].GetAppId() != "renovate" || grants[0].GetAppName() != "globex-renovate" ||
		grants[0].GetOrg() != "globex" || !slices.Equal(grants[0].GetRepositories(), []string{"*"}) ||
		grants[0].GetPermissions()["contents"] != "read" {
		t.Errorf("what all:platform:engineer may mint = %+v", grants)
	}
	if got := byName["all:platform:lead"].GetGithubGrants(); len(got) != 0 {
		t.Errorf("a group that mints nothing = %+v", got)
	}

	// No catalogue, no grants — and no call to GitHub to find that out.
	rig.console.deps.GitHubCatalogue = nil
	if policy, err = rig.console.GetPolicy(operator(), connect.NewRequest(&directoryrosterv1.GetPolicyRequest{})); err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	for _, group := range policy.Msg.GetGroups() {
		if len(group.GetGithubGrants()) != 0 {
			t.Errorf("%s mints %+v without a catalogue", group.GetName(), group.GetGithubGrants())
		}
	}
	if rig.github.reads != 0 {
		t.Errorf("reading the policy asked GitHub %d times", rig.github.reads)
	}
}

const testCatalogue = `
apps:
  - id: renovate
    org: globex
    description: Dependency updates
    permissions: {contents: write, pull_requests: write}
    installation: selected
    grants:
      - group: all:platform:engineer
        repositories: ["*"]
        permissions: {contents: read}
      - group: all:nobody:declares
        repositories: [docs]
        permissions: {pull_requests: read}
  - id: releases
    org: globex
    permissions: {contents: write}
`

// catalogueServer is connectServer with catalogue Apps kept in a Secret,
// as a deployment keeps them.
func catalogueServer(t *testing.T) (*ConsoleServer, *Console, *kube.Client, *audittest.Recorder) {
	t.Helper()
	server, console := connectServer(t, newMemoryConnections())
	client := kube.NewClient(fake.NewClientset(), "access-issuer", "access-issuer")
	declared, err := catalogue.Parse([]byte(testCatalogue))
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	recorded := audittest.New(t)
	console.deps.GitHubCatalogue = declared
	console.deps.GitHubCatalogueApps = kube.NewGitHubCatalogueApps(client)
	console.deps.Audit = recorded
	return server, console, client, recorded
}

func catalogueSecret(t *testing.T, client *kube.Client) map[string][]byte {
	t.Helper()
	secret, err := client.API().CoreV1().Secrets("access-issuer").Get(context.Background(), "access-issuer-github-catalogue-apps", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read the catalogue Apps Secret: %v", err)
	}
	return secret.Data
}

func catalogueApp(t *testing.T, console *Console, id string) *directoryrosterv1.GitHubCatalogueApp {
	t.Helper()
	status, err := console.GetGitHubStatus(operator(), connect.NewRequest(&directoryrosterv1.GetGitHubStatusRequest{}))
	if err != nil {
		t.Fatalf("GetGitHubStatus: %v", err)
	}
	if !status.Msg.GetCatalogueAvailable() {
		t.Fatal("the catalogue is not available")
	}
	for _, app := range status.Msg.GetCatalogueApps() {
		if app.GetId() == id {
			return app
		}
	}
	t.Fatalf("no catalogue App %s in %v", id, status.Msg.GetCatalogueApps())
	return nil
}

// Declared, created, installed, drifted, disconnected: the whole life of a
// catalogue App, as an operator walks it and as GitHub answers.
func TestACatalogueAppIsCreatedInstalledCheckedForDriftAndDisconnected(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, client, recorded := catalogueServer(t)
	ctx := context.Background()

	// Declared and not created: shown with what it would ask for, and
	// which of its grants' groups the policy knows.
	app := catalogueApp(t, console, "renovate")
	if app.GetState() != catalogueNotCreated || app.GetName() != "globex-renovate" || app.GetInstallation() != "selected" ||
		!app.GetDeclared() || len(app.GetPermissions()) != 2 || len(app.GetGrants()) != 2 ||
		!app.GetGrants()[0].GetGroupDeclared() || app.GetGrants()[1].GetGroupDeclared() {
		t.Errorf("before Create = %+v", app)
	}

	begun, err := console.BeginGitHubCatalogueAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "renovate"}))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	var manifest githubapp.Manifest
	if err = json.Unmarshal([]byte(begun.Msg.GetManifest()), &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest.Name != "globex-renovate" || manifest.Description != "Dependency updates" || manifest.DefaultPermissions["pull_requests"] != "write" ||
		manifest.RedirectURL != "https://access.example"+githubCatalogueCallbackPath || manifest.SetupURL != "https://access.example"+githubCatalogueSetupPath ||
		!strings.HasPrefix(begun.Msg.GetUrl(), "https://github.example/organizations/globex/settings/apps/new?state=") {
		t.Errorf("manifest = %+v, url %s", manifest, begun.Msg.GetUrl())
	}
	cookie, state := cookieFrom(t, begun.Header()), mustQuery(t, begun.Msg.GetUrl(), "state")

	// No other GitHub flow finishes a catalogue App's, nor the other way.
	if got := redirect(server.githubRunnerCallback, githubRunnerCallbackPath, url.Values{"code": {"created"}, "state": {state}}, cookie); got.Code != 400 {
		t.Errorf("a catalogue state at the runner callback = %d, want 400", got.Code)
	}

	created := redirect(server.githubCatalogueCallback, githubCatalogueCallbackPath, url.Values{"code": {"created"}, "state": {state}}, cookie)
	if created.Code != 302 || !strings.Contains(created.Header().Get("Location"), "/installations/new?state=") {
		t.Fatalf("after Create = %d to %q:\n%s", created.Code, created.Header().Get("Location"), created.Body)
	}
	data := catalogueSecret(t, client)
	for _, property := range []string{catalogueapp.AppIDProperty, catalogueapp.InstallationIDProperty, catalogueapp.PrivateKeyProperty} {
		if _, ok := data[catalogueapp.Key("renovate", property)]; ok {
			t.Errorf("before Install the Secret already has %s", property)
		}
	}
	if string(data["renovate.pending_private_key"]) != github.pem {
		t.Error("the App's key was not kept while it waits to be installed")
	}

	// Created, and GitHub holds what was declared: GitHub's own implied
	// metadata read is not drift.
	github.app = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}
	if app = catalogueApp(t, console, "renovate"); app.GetState() != catalogueCreated || len(app.GetDrift()) != 0 || app.GetReason() != "" {
		t.Errorf("created = state %s, drift %v, reason %q", app.GetState(), app.GetDrift(), app.GetReason())
	}
	// Begin again picks up at Install rather than creating a second App.
	again, err := console.BeginGitHubCatalogueAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "renovate"}))
	if err != nil || again.Msg.GetManifest() != "" || !strings.Contains(again.Msg.GetUrl(), "/apps/globex-access-roster/installations/new") {
		t.Errorf("Begin after Create = %+v, %v", again.Msg, err)
	}

	github.installed = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}
	install := created.Header().Get("Location")
	installed := redirect(server.githubCatalogueSetup, githubCatalogueSetupPath,
		url.Values{"installation_id": {"999"}, "state": {mustQuery(t, install, "state")}}, cookieFrom(t, created.Header()))
	if installed.Code != 302 || installed.Header().Get("Location") != "/console/#/github/apps/catalogue" {
		t.Fatalf("after Install = %d to %q:\n%s", installed.Code, installed.Header().Get("Location"), installed.Body)
	}
	data = catalogueSecret(t, client)
	if string(data["renovate.github_app_id"]) != "42" || string(data["renovate.github_app_installation_id"]) != "7" ||
		string(data["renovate.github_app_private_key"]) != github.pem {
		t.Errorf("after Install the keys are %v", slices.Sorted(maps.Keys(data)))
	}
	if _, pending := data["renovate.pending_private_key"]; pending {
		t.Error("the pending key outlived Install")
	}
	var record catalogueapp.Record
	if err = json.Unmarshal(data["renovate.record.json"], &record); err != nil || record.Version != 1 || record.ID != "renovate" ||
		record.Org != "globex" || record.AppID != 42 || record.InstallationID != 7 || record.ConnectedBy != "ada@north.example" {
		t.Errorf("record = %+v, %v", record, err)
	}
	// What a token exchange reads: the key and the installation, by id.
	if got, key, ok, err := console.deps.GitHubCatalogueApps.Get(ctx, "renovate"); err != nil || !ok || key != github.pem || got.InstallationID != 7 {
		t.Errorf("Get = %+v, key %t, %t, %v", got, key == github.pem, ok, err)
	}

	app = catalogueApp(t, console, "renovate")
	if app.GetState() != catalogueInstalled || app.GetRepositorySelection() != "selected" || app.GetConnectedBy() != "ada@north.example" {
		t.Errorf("installed = %+v", app)
	}
	if _, err = console.BeginGitHubCatalogueAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "renovate"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("creating an installed App again = %v, want failed precondition", err)
	}

	// An owner edits the App on GitHub: it now asks for more, and the
	// installation has not approved it. Re-check shows both.
	github.app = map[string]string{"contents": "write", "pull_requests": "read", "issues": "write", "metadata": "read"}
	checked, err := console.CheckGitHubCatalogueApp(operator(), connect.NewRequest(&directoryrosterv1.CheckGitHubCatalogueAppRequest{Id: "renovate"}))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	app = checked.Msg.GetApp()
	drift := strings.Join(app.GetDrift(), "\n")
	if app.GetState() != catalogueDrifted || !strings.Contains(drift, "issues: write, which the catalogue does not declare") ||
		!strings.Contains(drift, "pull_requests: read, and the catalogue declares write") || !strings.Contains(drift, "approve the permission request") {
		t.Errorf("drifted = %s:\n%s", app.GetState(), drift)
	}
	rows := map[string]*directoryrosterv1.GitHubAppPermission{}
	for _, row := range app.GetPermissions() {
		rows[row.GetName()] = row
	}
	if rows["pull_requests"].GetDeclared() != "write" || rows["pull_requests"].GetApp() != "read" || rows["pull_requests"].GetInstallation() != "write" ||
		rows["issues"].GetDeclared() != "" || rows["issues"].GetApp() != "write" {
		t.Errorf("permission rows = %v", app.GetPermissions())
	}

	// Uninstalled on GitHub by somebody else: drift, with what to do.
	github.installed = nil
	checked, _ = console.CheckGitHubCatalogueApp(operator(), connect.NewRequest(&directoryrosterv1.CheckGitHubCatalogueAppRequest{Id: "renovate"}))
	if !strings.Contains(strings.Join(checked.Msg.GetApp().GetDrift(), "\n"), "installation is gone on GitHub") {
		t.Errorf("an uninstalled App = %v", checked.Msg.GetApp().GetDrift())
	}

	gone, err := console.DisconnectGitHubCatalogueApp(operator(),
		connect.NewRequest(&directoryrosterv1.DisconnectGitHubCatalogueAppRequest{Id: "renovate"}))
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if !gone.Msg.GetUninstalled() || !slices.Equal(github.uninstalled, []string{"7"}) ||
		gone.Msg.GetAppSettingsUrl() != "https://github.example/organizations/globex/settings/apps/globex-access-roster" {
		t.Errorf("Disconnect = %+v, calls %v", gone.Msg, github.uninstalled)
	}
	for key := range catalogueSecret(t, client) {
		if strings.HasPrefix(key, "renovate.") {
			t.Errorf("%s outlived Disconnect", key)
		}
	}
	if app = catalogueApp(t, console, "renovate"); app.GetState() != catalogueNotCreated {
		t.Errorf("after Disconnect = %s", app.GetState())
	}
	if _, err = console.DisconnectGitHubCatalogueApp(operator(),
		connect.NewRequest(&directoryrosterv1.DisconnectGitHubCatalogueAppRequest{Id: "renovate"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("disconnecting what is gone = %v, want not found", err)
	}

	want := []string{"roster.catalogue_app.created", "roster.catalogue_app.installed", "roster.catalogue_app.disconnected"}
	if got := recorded.Actions(); !slices.Equal(got, want) {
		t.Errorf("audit = %v, want %v", got, want)
	}
}

// Only an operator creates one, only an App the catalogue declares, and
// only under the organisation the catalogue names.
func TestACatalogueAppIsOnlyWhatTheCatalogueDeclares(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, client, _ := catalogueServer(t)
	viewer := WithIdentity(context.Background(), access.Identity{Role: access.RoleViewer})
	for _, c := range []struct {
		name string
		ctx  context.Context
		id   string
		want connect.Code
	}{
		{"a viewer", viewer, "renovate", connect.CodePermissionDenied},
		{"an undeclared id", operator(), "something-else", connect.CodeInvalidArgument},
	} {
		_, err := console.BeginGitHubCatalogueAppConnect(c.ctx, connect.NewRequest(&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: c.id}))
		if connect.CodeOf(err) != c.want {
			t.Errorf("%s = %v, want %v", c.name, err, c.want)
		}
	}
	_, err := console.CheckGitHubCatalogueApp(viewer, connect.NewRequest(&directoryrosterv1.CheckGitHubCatalogueAppRequest{Id: "renovate"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer's re-check = %v", err)
	}

	// Created by somebody under another organisation: refused, and nothing
	// is kept.
	github.owner = "initech"
	begun, err := console.BeginGitHubCatalogueAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "releases"}))
	if err != nil {
		t.Fatal(err)
	}
	refused := redirect(server.githubCatalogueCallback, githubCatalogueCallbackPath,
		url.Values{"code": {"created"}, "state": {mustQuery(t, begun.Msg.GetUrl(), "state")}}, cookieFrom(t, begun.Header()))
	if refused.Code != 409 || !strings.Contains(refused.Body.String(), "created under initech") {
		t.Errorf("an App under another organisation = %d:\n%s", refused.Code, refused.Body)
	}
	if records, _ := console.deps.GitHubCatalogueApps.List(context.Background()); len(records) != 0 {
		t.Errorf("an App under another organisation was kept: %+v", records)
	}
	_ = client

	// A deployment keeping no state in Kubernetes keeps no catalogue Apps.
	_, none := connectServer(t, newMemoryConnections())
	if _, err = none.BeginGitHubCatalogueAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "renovate"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("no store = %v, want failed precondition", err)
	}
	status, err := none.GetGitHubStatus(operator(), connect.NewRequest(&directoryrosterv1.GetGitHubStatusRequest{}))
	if err != nil || status.Msg.GetCatalogueAvailable() || len(status.Msg.GetCatalogueApps()) != 0 {
		t.Errorf("status without a store = %v, %v", status.Msg.GetCatalogueApps(), err)
	}
}

// The page does not ask GitHub on every load: an answer is kept for a
// minute, a re-check asks again, and a failure is the App's reason rather
// than the page's error.
func TestWhatGitHubSaysOfACatalogueAppIsCachedBriefly(t *testing.T) {
	github := startFakeGitHub(t)
	_, console, _, _ := catalogueServer(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	console.githubSeen.clock = func() time.Time { return now }
	github.app = map[string]string{"contents": "write"}
	github.installed = map[string]string{"contents": "write"}
	if err := console.deps.GitHubCatalogueApps.Put(context.Background(), catalogueapp.Record{
		ID: "releases", Org: "globex", AppID: 42, AppSlug: "globex-releases", InstallationID: 7, ConnectedAt: now,
	}, github.pem); err != nil {
		t.Fatal(err)
	}
	reads := func() int {
		github.mu.Lock()
		defer github.mu.Unlock()
		return github.reads
	}

	if app := catalogueApp(t, console, "releases"); app.GetState() != catalogueInstalled || reads() != 2 {
		t.Fatalf("first read = %s after %d calls, want installed after 2", app.GetState(), reads())
	}
	github.app = map[string]string{"contents": "read"}
	now = now.Add(30 * time.Second)
	if app := catalogueApp(t, console, "releases"); app.GetState() != catalogueInstalled || reads() != 2 {
		t.Errorf("within the minute = %s after %d calls, want the cached answer", app.GetState(), reads())
	}
	if checked, err := console.CheckGitHubCatalogueApp(operator(),
		connect.NewRequest(&directoryrosterv1.CheckGitHubCatalogueAppRequest{Id: "releases"})); err != nil ||
		checked.Msg.GetApp().GetState() != catalogueDrifted || reads() != 4 {
		t.Errorf("re-check = %v after %d calls, %v", checked.Msg.GetApp(), reads(), err)
	}
	github.app = map[string]string{"contents": "write"}
	now = now.Add(2 * time.Minute)
	if app := catalogueApp(t, console, "releases"); app.GetState() != catalogueInstalled || reads() != 6 {
		t.Errorf("after the minute = %s after %d calls, want asked again", app.GetState(), reads())
	}

	// GitHub unreachable: the status still answers, the App says why.
	console.deps.GitHubCatalogueApps = brokenKeys{console.deps.GitHubCatalogueApps}
	now = now.Add(2 * time.Minute)
	app := catalogueApp(t, console, "releases")
	if app.GetState() != catalogueInstalled || !strings.Contains(app.GetReason(), "key") {
		t.Errorf("without a usable key = %s, reason %q", app.GetState(), app.GetReason())
	}
}

// brokenKeys is a store whose key has been damaged.
type brokenKeys struct{ GitHubCatalogueApps }

func (b brokenKeys) Get(ctx context.Context, id string) (catalogueapp.Record, string, bool, error) {
	record, _, ok, err := b.GitHubCatalogueApps.Get(ctx, id)
	return record, "not a key", ok, err
}

// An App whose entry was removed from the catalogue is still listed, so it
// can be disconnected, and cannot be created again.
func TestAnAppTheCatalogueNoLongerDeclaresCanStillBeDisconnected(t *testing.T) {
	github := startFakeGitHub(t)
	_, console, _, _ := catalogueServer(t)
	if err := console.deps.GitHubCatalogueApps.Put(context.Background(), catalogueapp.Record{
		ID: "retired", Org: "globex", AppID: 42, AppSlug: "globex-retired", InstallationID: 7,
	}, github.pem); err != nil {
		t.Fatal(err)
	}
	app := catalogueApp(t, console, "retired")
	if app.GetDeclared() || app.GetState() != catalogueInstalled || app.GetReason() == "" {
		t.Errorf("retired = %+v", app)
	}
	if github.reads != 0 {
		t.Errorf("GitHub was asked about an App nothing declares: %d calls", github.reads)
	}
	if _, err := console.DisconnectGitHubCatalogueApp(operator(),
		connect.NewRequest(&directoryrosterv1.DisconnectGitHubCatalogueAppRequest{Id: "retired"})); err != nil {
		t.Errorf("Disconnect = %v", err)
	}
}

// runnerServer is connectServer with runner Apps kept in a Secret, as a
// deployment keeps them, for the tiers given.
func runnerServer(t *testing.T, tiers ...string) (*ConsoleServer, *Console, *kube.Client) {
	t.Helper()
	server, console := connectServer(t, newMemoryConnections())
	client := kube.NewClient(fake.NewClientset(), "access-issuer", "access-issuer")
	console.deps.GitHubRunnerApps = kube.NewGitHubRunnerApps(client)
	console.deps.GitHubRunnerTiers = tiers
	return server, console, client
}

func runnerSecret(t *testing.T, client *kube.Client) map[string][]byte {
	t.Helper()
	secret, err := client.API().CoreV1().Secrets("access-issuer").Get(context.Background(), "access-issuer-github-runner-apps", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read the runner Apps Secret: %v", err)
	}
	return secret.Data
}

// Create, then Install, as for an organisation's App. The three keys a
// runner scale set reads exist only once the App is installed, so a copy of
// the Secret taken in between can never hand runners an App they cannot
// register with.
func TestARunnerAppIsCreateThenInstallAndItsKeysAppearOnlyOnceInstalled(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, client := runnerServer(t, "preview", "stable")

	begun, err := console.BeginGitHubRunnerAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubRunnerAppConnectRequest{Org: "globex", Tier: "stable"}))
	if err != nil {
		t.Fatalf("BeginGitHubRunnerAppConnect: %v", err)
	}
	var manifest githubapp.Manifest
	if err = json.Unmarshal([]byte(begun.Msg.GetManifest()), &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest.Name != "globex-runners-stable" || manifest.DefaultPermissions["organization_self_hosted_runners"] != "write" ||
		manifest.RedirectURL != "https://access.example"+githubRunnerCallbackPath || manifest.SetupURL != "https://access.example"+githubRunnerSetupPath {
		t.Errorf("manifest = %+v", manifest)
	}
	cookie, state := cookieFrom(t, begun.Header()), mustQuery(t, begun.Msg.GetUrl(), "state")

	// An organisation connect's callback does not finish a runner flow.
	if got := redirect(server.githubCallback, githubCallbackPath, url.Values{"code": {"created"}, "state": {state}}, cookie); got.Code != 400 {
		t.Errorf("a runner state at the organisation callback = %d, want 400", got.Code)
	}

	created := redirect(server.githubRunnerCallback, githubRunnerCallbackPath, url.Values{"code": {"created"}, "state": {state}}, cookie)
	if created.Code != 302 || !strings.Contains(created.Header().Get("Location"), "/installations/new?state=") {
		t.Fatalf("after Create = %d to %q:\n%s", created.Code, created.Header().Get("Location"), created.Body)
	}
	data := runnerSecret(t, client)
	for _, property := range []string{runnerapp.AppIDProperty, runnerapp.InstallationIDProperty, runnerapp.PrivateKeyProperty} {
		if _, ok := data[runnerapp.Key("stable", "globex", property)]; ok {
			t.Errorf("before Install the Secret already has %s", property)
		}
	}
	if key, ok, _ := console.deps.GitHubRunnerApps.PrivateKey(context.Background(), "stable", "globex"); !ok || key != github.pem {
		t.Error("the App's key was not kept while it waits to be installed")
	}

	install := created.Header().Get("Location")
	installed := redirect(server.githubRunnerSetup, githubRunnerSetupPath,
		url.Values{"installation_id": {"999"}, "state": {mustQuery(t, install, "state")}}, cookieFrom(t, created.Header()))
	if installed.Code != 302 || installed.Header().Get("Location") != "/console/#/github/apps" {
		t.Fatalf("after Install = %d to %q:\n%s", installed.Code, installed.Header().Get("Location"), installed.Body)
	}
	data = runnerSecret(t, client)
	if string(data["stable.globex.github_app_id"]) != "42" || string(data["stable.globex.github_app_installation_id"]) != "7" ||
		string(data["stable.globex.github_app_private_key"]) != github.pem {
		t.Errorf("after Install the keys are %v", slices.Sorted(maps.Keys(data)))
	}
	if _, pending := data["stable.globex.pending_private_key"]; pending {
		t.Error("the pending key outlived Install")
	}

	status, err := console.GetGitHubStatus(operator(), connect.NewRequest(&directoryrosterv1.GetGitHubStatusRequest{}))
	if err != nil {
		t.Fatalf("GetGitHubStatus: %v", err)
	}
	if !slices.Equal(status.Msg.GetRunnerTiers(), []string{"preview", "stable"}) || len(status.Msg.GetRunnerApps()) != 1 ||
		!status.Msg.GetRunnerApps()[0].GetInstalled() || status.Msg.GetRunnerApps()[0].GetConnectedBy() != "ada@north.example" {
		t.Errorf("status = tiers %v, apps %+v", status.Msg.GetRunnerTiers(), status.Msg.GetRunnerApps())
	}

	_, err = console.BeginGitHubRunnerAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubRunnerAppConnectRequest{Org: "globex", Tier: "stable"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("creating an installed tier's App again = %v, want failed precondition", err)
	}
	// Another tier is another App.
	if _, err = console.BeginGitHubRunnerAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubRunnerAppConnectRequest{Org: "globex", Tier: "preview"})); err != nil {
		t.Errorf("the preview tier beside an installed stable one = %v", err)
	}
}

// Only an operator, only an organisation the policy binds, only a tier the
// deployment declares.
func TestARunnerAppNeedsADeclaredTierABoundOrganisationAndAnOperator(t *testing.T) {
	startFakeGitHub(t)
	_, console, _ := runnerServer(t, "stable")
	begin := func(ctx context.Context, org, tier string) error {
		_, err := console.BeginGitHubRunnerAppConnect(ctx, connect.NewRequest(&directoryrosterv1.BeginGitHubRunnerAppConnectRequest{Org: org, Tier: tier}))
		return err
	}
	viewer := WithIdentity(context.Background(), access.Identity{Role: access.RoleViewer})
	for _, c := range []struct {
		name      string
		ctx       context.Context
		org, tier string
		want      connect.Code
	}{
		{"a viewer", viewer, "globex", "stable", connect.CodePermissionDenied},
		{"an undeclared tier", operator(), "globex", "preview", connect.CodeInvalidArgument},
		{"an unbound organisation", operator(), "not-bound", "stable", connect.CodeFailedPrecondition},
		{"a malformed login", operator(), "not a login", "stable", connect.CodeInvalidArgument},
	} {
		if err := begin(c.ctx, c.org, c.tier); connect.CodeOf(err) != c.want {
			t.Errorf("%s = %v, want %v", c.name, err, c.want)
		}
	}

	_, none, _ := runnerServer(t)
	_, err := none.BeginGitHubRunnerAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubRunnerAppConnectRequest{Org: "globex", Tier: "stable"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("no declared tiers = %v, want failed precondition", err)
	}
	status, err := none.GetGitHubStatus(operator(), connect.NewRequest(&directoryrosterv1.GetGitHubStatusRequest{}))
	if err != nil || len(status.Msg.GetRunnerTiers()) != 0 {
		t.Errorf("status without tiers = %v, %v", status.Msg.GetRunnerTiers(), err)
	}
}

// Disconnect uninstalls on GitHub and forgets every key of that one App.
func TestDisconnectingARunnerAppUninstallsThenForgetsItsKeys(t *testing.T) {
	github := startFakeGitHub(t)
	_, console, client := runnerServer(t, "preview", "stable")
	store := console.deps.GitHubRunnerApps
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for _, tier := range []string{"preview", "stable"} {
		if err := store.Put(context.Background(), runnerapp.Record{
			Tier: tier, Org: "globex", AppID: 42, AppSlug: "globex-runners-" + tier, InstallationID: 7, ConnectedAt: at,
		}, github.pem); err != nil {
			t.Fatal(err)
		}
	}

	gone, err := console.DisconnectGitHubRunnerApp(operator(),
		connect.NewRequest(&directoryrosterv1.DisconnectGitHubRunnerAppRequest{Org: "globex", Tier: "stable"}))
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if !gone.Msg.GetUninstalled() || !slices.Equal(github.uninstalled, []string{"7"}) ||
		gone.Msg.GetAppSettingsUrl() != "https://github.example/organizations/globex/settings/apps/globex-runners-stable" {
		t.Errorf("response = %+v, calls = %v", gone.Msg, github.uninstalled)
	}
	for key := range runnerSecret(t, client) {
		if strings.HasPrefix(key, "stable.") {
			t.Errorf("the stable App's %s outlived Disconnect", key)
		}
	}
	if records, _ := store.List(context.Background()); len(records) != 1 || records[0].Tier != "preview" {
		t.Errorf("after Disconnect = %+v, want the preview App alone", records)
	}

	if _, err = console.DisconnectGitHubRunnerApp(operator(),
		connect.NewRequest(&directoryrosterv1.DisconnectGitHubRunnerAppRequest{Org: "globex", Tier: "stable"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("disconnecting what is gone = %v, want not found", err)
	}
}

// The create paths of the console write through the one store, so an App
// created from the console carries the labels the configuration declares for
// it, whichever purpose it has, and the Apps list reads it back as itself.
func TestTheConsoleCreatesAnAppWithTheLabelsTheConfigurationDeclares(t *testing.T) {
	github := startFakeGitHub(t)
	server, console := connectServer(t, newMemoryConnections())
	declared, err := catalogue.Parse([]byte(testCatalogue))
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	base := portstore.New(memory.New().Set()).DeclareGitHubApps(portstore.DeclaredGitHubApps{
		Labels:       func(id string) map[string]string { return map[string]string{"owner": "app-" + id} },
		RunnerLabels: func(tier, org string) map[string]string { return map[string]string{"pool": tier + "-" + org} },
	})
	console.deps.GitHubCatalogue = declared
	console.deps.GitHubCatalogueApps = portstore.NewGitHubCatalogueApps(base)
	console.deps.GitHubRunnerApps = portstore.NewGitHubRunnerApps(base)
	console.deps.GitHubRunnerTiers = []string{"stable"}

	for _, id := range []string{"renovate", "globex-runners-stable"} {
		begun, err := console.BeginGitHubAppConnect(operator(), connect.NewRequest(&directoryrosterv1.BeginGitHubAppConnectRequest{Id: id}))
		if err != nil {
			t.Fatalf("Begin %s: %v", id, err)
		}
		callback, path := server.githubCatalogueCallback, githubCatalogueCallbackPath
		if id != "renovate" {
			callback, path = server.githubRunnerCallback, githubRunnerCallbackPath
		}
		created := redirect(callback, path, url.Values{"code": {"created"}, "state": {mustQuery(t, begun.Msg.GetUrl(), "state")}}, cookieFrom(t, begun.Header()))
		if created.Code != 302 {
			t.Fatalf("after Create %s = %d:\n%s", id, created.Code, created.Body)
		}
	}
	if github.pem == "" {
		t.Fatal("the fake GitHub handed over no key")
	}

	kept, err := portstore.NewGitHubApps(base).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	labels := map[string]map[string]string{}
	purposes := map[string]appid.Purpose{}
	for _, app := range kept {
		labels[app.ID], purposes[app.ID] = app.Labels, app.Purpose
	}
	if labels["renovate"]["owner"] != "app-renovate" || purposes["renovate"] != appid.Catalogue ||
		labels["runner-stable-globex"]["pool"] != "stable-globex" || purposes["runner-stable-globex"] != appid.Runner {
		t.Errorf("kept Apps = %v, purposes %v", labels, purposes)
	}
}
