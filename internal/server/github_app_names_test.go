package server

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/link"
)

// The manifest the organisation connect flow sends carries the configured
// name for that organisation, and the default for every other.
func TestTheConnectManifestCarriesTheConfiguredOrganisationAppName(t *testing.T) {
	rig := newAppsRig(t)
	rig.console.deps.GitHubOrgAppName = func(org string) string {
		if org == "globex" {
			return "globex-people"
		}
		return ""
	}
	name := func(org string) string {
		t.Helper()
		begun, err := rig.console.BeginGitHubConnect(operator(), connect.NewRequest(&directoryrosterv1.BeginGitHubConnectRequest{Org: org}))
		if err != nil {
			t.Fatalf("BeginGitHubConnect(%s): %v", org, err)
		}
		var manifest githubapp.Manifest
		if err = json.Unmarshal([]byte(begun.Msg.GetManifest()), &manifest); err != nil {
			t.Fatalf("manifest: %v", err)
		}
		return manifest.Name
	}
	if got := name("globex"); got != "globex-people" {
		t.Errorf("the configured organisation's App is named %q", got)
	}
	if got := name("acme"); got != "acme-access-roster" {
		t.Errorf("an organisation with no name is created as %q, want the default", got)
	}
}

// The link App's manifest and its row before it exists carry the configured
// name; once it exists the row shows its slug.
func TestTheLinkAppTakesItsConfiguredNameUntilItExists(t *testing.T) {
	rig := newAppsRig(t)
	rig.console.deps.GitHubLinkAppName = "globex-sign-in-link"

	begun, err := rig.console.BeginGitHubLinkAppConnect(operator(), connect.NewRequest(&directoryrosterv1.BeginGitHubLinkAppConnectRequest{Owner: "globex"}))
	if err != nil {
		t.Fatalf("BeginGitHubLinkAppConnect: %v", err)
	}
	var manifest githubapp.Manifest
	if err = json.Unmarshal([]byte(begun.Msg.GetManifest()), &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest.Name != "globex-sign-in-link" || !manifest.Public || len(manifest.CallbackURLs) != 1 {
		t.Errorf("the link manifest = %+v", manifest)
	}

	apps, _ := rig.list(t)
	if got := apps["link"].GetName(); got != "globex-sign-in-link" {
		t.Errorf("the link row before it exists is %q, want the configured name", got)
	}
	err = rig.console.deps.GitHubLinkApp.PutLinkApp(context.Background(),
		link.App{Owner: "globex", AppID: 9, AppSlug: "renamed-on-github", ClientID: "Iv1.test"},
		link.AppCredential{AppID: 9, ClientID: "Iv1.test", ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	apps, _ = rig.list(t)
	if got := apps["link"].GetName(); got != "renamed-on-github" {
		t.Errorf("the link row after it exists is %q, want its slug", got)
	}
}

// An App renamed on GitHub's create page is shown by the slug GitHub gave
// it, in the list and in the superseded catalogue shape; before it exists the
// row shows the configured name, or the default.
func TestAnOrganisationRowShowsTheRecordedSlugOnceThereIsOne(t *testing.T) {
	rig := newAppsRig(t)
	rig.console.deps.GitHubOrgAppName = func(org string) string {
		if org == "acme" {
			return "acme-members"
		}
		return ""
	}
	err := rig.console.deps.GitHubOrgs.Put(context.Background(),
		connection.Record{Org: "globex", AppID: 42, AppSlug: "people-of-globex", InstallationID: 7, ConnectedBy: "ada@north.example"},
		connection.Credential{Org: "globex", AppID: 42, InstallationID: 7, PrivateKey: rig.github.pem})
	if err != nil {
		t.Fatal(err)
	}
	apps, _ := rig.list(t)
	if got := apps["globex-controller"].GetName(); got != "people-of-globex" {
		t.Errorf("a created App's row is %q, want its recorded slug", got)
	}
	if got := apps["acme-controller"].GetName(); got != "acme-members" {
		t.Errorf("an App not created yet is %q, want the configured name", got)
	}
}
