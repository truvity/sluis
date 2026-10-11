package config_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
)

const googleHead = "apiVersion: sluis.truvity.github.io/sluis-google/v1\ninstance: example\n"

func TestTheFullGoogleDocumentLoads(t *testing.T) {
	full := filepath.Join("testdata", "sluis-google.full.yaml")
	doc, err := config.LoadGoogle(full)
	if err != nil {
		t.Fatal(err)
	}
	other := []string{"sluis.full.yaml", "sluis-backup.full.yaml"}
	if !config.IsGoogle(full) || config.IsGoogle(filepath.Join("testdata", other[0])) || config.IsGoogle(filepath.Join("testdata", other[1])) {
		t.Error("IsGoogle does not tell the documents apart")
	}
	if config.GoogleAPIVersion != config.APIVersion("sluis-google") {
		t.Errorf("the constant %q and APIVersion %q differ", config.GoogleAPIVersion, config.APIVersion("sluis-google"))
	}
	r, f, p := doc.Google.Intervals()
	if r != 15*time.Minute || f != 30*time.Minute || p != 5*time.Minute {
		t.Errorf("intervals %s %s %s", r, f, p)
	}
	if len(doc.Google.Workspaces) != 1 || doc.Google.Workspaces[0].KeySecret != "directory/example/key" {
		t.Errorf("workspaces %+v", doc.Google.Workspaces)
	}
	if s := doc.Serve(); s.Ports == nil || s.Secrets == nil || s.Instance != "example" {
		t.Errorf("serve view %+v", s)
	}
}

func TestAGoogleDocumentNeedsNothingButItsSection(t *testing.T) {
	doc, err := config.LoadGoogle(writeBackupDoc(t, googleHead+"google: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Name() != "example" {
		t.Errorf("name %q", doc.Name())
	}
}

func TestAGoogleDocumentIsRefusedWhenItIsWrong(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"no section":         {googleHead, "google"},
		"unknown key":        {googleHead + "google: {nope: 1}\n", "nope"},
		"issuer key":         {googleHead + "issuerURL: https://id.example.com\ngoogle: {}\n", "issuerURL"},
		"window too short":   {googleHead + "google: {refreshInterval: 15m, freshnessWindow: 10m}\n", "freshnessWindow"},
		"zero interval":      {googleHead + "google: {refreshInterval: 0s}\n", "refreshInterval"},
		"client no provider": {googleHead + "google: {oauthClient: {id: x}}\n", "provider"},
		"declared twice": {googleHead + `google:
  workspaces:
    - {backend: google, admin: A@example.com, keySecret: directory/a/key}
    - {backend: google, admin: a@example.com, keySecret: directory/b/key}
`, "twice"},
		"missing key secret": {googleHead + "google: {workspaces: [{backend: google, admin: a@example.com}]}\n", "keySecret"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.LoadGoogle(writeBackupDoc(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
