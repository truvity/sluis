package server

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/rpcmount"
)

// createdCatalogueApp creates the catalogue's renovate App through the
// console's Create and stops there: the owner then installs it on GitHub
// itself, and the setup redirect never reaches the console.
func createdCatalogueApp(t *testing.T, server *ConsoleServer, console *Console) {
	t.Helper()
	callCtx, call := rpcmount.ServerContext(operator())
	begun, err := console.BeginGitHubCatalogueAppConnect(callCtx,
		&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "renovate"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	cookie, state := cookieFrom(t, rpcmount.HTTPHeader(call.ResponseHeader())), mustQuery(t, begun.GetUrl(), "state")
	created := redirect(server.githubCatalogueCallback, githubCatalogueCallbackPath, url.Values{"code": {"created"}, "state": {state}}, cookie)
	if created.Code != 302 {
		t.Fatalf("Create = %d:\n%s", created.Code, created.Body)
	}
}

// appCreatedState is an App created and not installed.
const appCreatedState = directoryrosterv1.AppState_APP_STATE_CREATED

func recheck(t *testing.T, console *Console, id string) *directoryrosterv1.GitHubApp {
	t.Helper()
	checked, err := console.CheckGitHubApp(operator(), &directoryrosterv1.CheckGitHubAppRequest{Id: id})
	if err != nil {
		t.Fatalf("CheckGitHubApp(%s): %v", id, err)
	}
	return checked.GetApp()
}

func listed(t *testing.T, console *Console, id string) *directoryrosterv1.GitHubApp {
	t.Helper()
	got, err := console.GetGitHubApp(operator(), &directoryrosterv1.GetGitHubAppRequest{Id: id})
	if err != nil {
		t.Fatalf("GetGitHubApp(%s): %v", id, err)
	}
	return got.GetApp()
}

// The owner installed the App on GitHub directly. Re-check asks GitHub, as
// the App, where it is installed, and records that as the setup redirect
// would have.
func TestRecheckFindsACatalogueAppInstalledDirectlyOnGitHub(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, client, recorded := catalogueServer(t)
	createdCatalogueApp(t, server, console)
	github.app = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}
	github.installed = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}

	// Not installed yet: today's state, no error, nothing recorded. Opening
	// the page never looks for an installation, even where there is one.
	github.installationLogins = []string{}
	if app := recheck(t, console, "renovate"); app.GetState() != appCreatedState || app.GetInstallationId() != 0 || app.GetReason() != "" {
		t.Errorf("not installed = state %s, installation %d, reason %q", app.GetState(), app.GetInstallationId(), app.GetReason())
	}
	github.installationLogins = nil
	if app := listed(t, console, "renovate"); app.GetState() != appCreatedState {
		t.Errorf("a page load found an installation: %s", app.GetState())
	}
	if _, ok := catalogueSecret(t, client)["renovate.github_app_installation_id"]; ok {
		t.Error("the installation was recorded before a re-check asked")
	}

	// Installed on GitHub: Re-check records it and shows installed.
	app := recheck(t, console, "renovate")
	if app.GetState() != appInstalled || app.GetInstallationId() != 7 || len(app.GetDrift()) != 0 || app.GetReason() != "" {
		t.Errorf("after the re-check = state %s, installation %d, drift %v, reason %q",
			app.GetState(), app.GetInstallationId(), app.GetDrift(), app.GetReason())
	}
	data := catalogueSecret(t, client)
	if string(data["renovate.github_app_installation_id"]) != "7" || string(data["renovate.github_app_private_key"]) != github.pem {
		t.Errorf("the keys are not the installed App's: %v", data)
	}
	if _, pending := data["renovate.pending_private_key"]; pending {
		t.Error("the pending key outlived the installation")
	}
	if got := listed(t, console, "renovate"); got.GetState() != appInstalled || got.GetInstallationId() != 7 {
		t.Errorf("the page afterwards = state %s, installation %d", got.GetState(), got.GetInstallationId())
	}

	found := recorded.Find("roster.catalogue_app.installed")
	if len(found) != 1 || found[0].GetActor().GetId() != "ada@north.example" ||
		found[0].GetOutcome().GetReason() != audit.InstallationFoundByRecheck {
		t.Fatalf("audit = %v", recorded.Actions())
	}

	// Once recorded, a re-check asks nothing more about installations.
	github.installationLogins = []string{}
	if app = recheck(t, console, "renovate"); app.GetState() != appInstalled {
		t.Errorf("a second re-check = %s", app.GetState())
	}
	if n := len(recorded.Find("roster.catalogue_app.installed")); n != 1 {
		t.Errorf("%d installed records after the second re-check", n)
	}
}

// The same Re-check through the catalogue page's own call.
func TestRecheckOfTheCataloguePageFindsTheInstallationToo(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, _, recorded := catalogueServer(t)
	createdCatalogueApp(t, server, console)
	github.app = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}
	github.installed = github.app

	checked, err := console.CheckGitHubCatalogueApp(operator(), &directoryrosterv1.CheckGitHubCatalogueAppRequest{Id: "renovate"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if app := checked.GetApp(); app.GetState() != catalogueInstalled || app.GetInstallationId() != 7 {
		t.Errorf("the catalogue Re-check = state %s, installation %d", app.GetState(), app.GetInstallationId())
	}
	if len(recorded.Find("roster.catalogue_app.installed")) != 1 {
		t.Errorf("audit = %v", recorded.Actions())
	}
}

// An installation on any other account is not the declared organisation's,
// and GitHub unable to answer is shown, not recorded as anything.
func TestRecheckRecordsNoInstallationOnAnotherOrganisationOrWhenGitHubFails(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, client, recorded := catalogueServer(t)
	createdCatalogueApp(t, server, console)
	github.app = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}

	github.installationLogins = []string{"initech", "globex-labs"}
	if app := recheck(t, console, "renovate"); app.GetState() != appCreatedState || app.GetInstallationId() != 0 || app.GetReason() != "" {
		t.Errorf("an installation elsewhere = state %s, installation %d, reason %q", app.GetState(), app.GetInstallationId(), app.GetReason())
	}

	github.installationsStatus = 500
	app := recheck(t, console, "renovate")
	if app.GetState() != appCreatedState || app.GetInstallationId() != 0 || !strings.Contains(app.GetReason(), "could not say whether the App is installed") {
		t.Errorf("GitHub failing = state %s, installation %d, reason %q", app.GetState(), app.GetInstallationId(), app.GetReason())
	}

	if _, ok := catalogueSecret(t, client)["renovate.github_app_installation_id"]; ok {
		t.Error("an installation was recorded")
	}
	if got := recorded.Find("roster.catalogue_app.installed"); len(got) != 0 {
		t.Errorf("audit = %v", recorded.Actions())
	}
}

// Only an operator re-checks, and so only an operator can record anything.
func TestRecheckByAViewerRecordsNothing(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, client, _ := catalogueServer(t)
	createdCatalogueApp(t, server, console)
	github.app = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}

	viewer := WithIdentity(context.Background(), access.Identity{Email: "viewer@example.test", Role: access.RoleViewer})
	if _, err := console.CheckGitHubApp(viewer, &directoryrosterv1.CheckGitHubAppRequest{Id: "renovate"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer's Re-check = %v, want permission denied", err)
	}
	if _, ok := catalogueSecret(t, client)["renovate.github_app_installation_id"]; ok {
		t.Error("a viewer's Re-check recorded an installation")
	}
}

// A runner App installed directly on GitHub is found the same way.
func TestRecheckFindsARunnerAppInstalledDirectlyOnGitHub(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, client := runnerServer(t, "stable")
	recorded := audittest.New(t)
	console.deps.Audit = recorded

	callCtx, call := rpcmount.ServerContext(operator())
	begun, err := console.BeginGitHubRunnerAppConnect(callCtx,
		&directoryrosterv1.BeginGitHubRunnerAppConnectRequest{Org: "globex", Tier: "stable"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	cookie, state := cookieFrom(t, rpcmount.HTTPHeader(call.ResponseHeader())), mustQuery(t, begun.GetUrl(), "state")
	if created := redirect(server.githubRunnerCallback, githubRunnerCallbackPath, url.Values{"code": {"created"}, "state": {state}}, cookie); created.Code != 302 {
		t.Fatalf("Create = %d:\n%s", created.Code, created.Body)
	}
	const id = "globex-runners-stable"
	github.app = map[string]string{"organization_self_hosted_runners": "write", "metadata": "read"}
	github.installed = github.app

	github.installationLogins = []string{"initech"}
	if app := recheck(t, console, id); app.GetState() != appCreatedState || app.GetInstallationId() != 0 {
		t.Errorf("an installation elsewhere = state %s, installation %d", app.GetState(), app.GetInstallationId())
	}

	github.installationLogins = nil
	app := recheck(t, console, id)
	if app.GetState() != appInstalled || app.GetInstallationId() != 7 {
		t.Errorf("after the re-check = state %s, installation %d, reason %q", app.GetState(), app.GetInstallationId(), app.GetReason())
	}
	data := runnerSecret(t, client)
	if string(data["stable.globex.github_app_installation_id"]) != "7" || string(data["stable.globex.github_app_private_key"]) != github.pem {
		t.Errorf("the runner keys = %v", data)
	}
	found := recorded.Find("roster.runner_app.installed")
	if len(found) != 1 || found[0].GetActor().GetId() != "ada@north.example" ||
		found[0].GetOutcome().GetReason() != audit.InstallationFoundByRecheck {
		t.Errorf("audit = %v", recorded.Actions())
	}
}
