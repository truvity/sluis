package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
)

// consumer is a webhook receiver, as Argo CD or Kargo is: it verifies the
// signature of each delivery against the one secret it holds, and what it
// holds is whatever the console kept the last time it reloaded.
type consumer struct {
	mu     sync.Mutex
	held   string
	live   bool // reloads from the store on every request, as a synced secret would
	pings  int
	server *httptest.Server
	secret func() string
	// path, when set, is the only path it serves (a Kargo receiver's).
	path func(secret string) string
}

func startConsumer(t *testing.T, secret func() string, path func(string) string) *consumer {
	t.Helper()
	c := &consumer{secret: secret, path: path}
	c.held = secret()
	c.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.live {
			c.held = c.secret()
		}
		if c.path != nil && r.URL.Path != c.path(c.held) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(c.held))
		mac.Write(body)
		if r.Header.Get("X-GitHub-Event") != "ping" || r.Header.Get("X-Hub-Signature-256") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c.pings++
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func auditSteps(recorded *audittest.Recorder) []string {
	var out []string
	for _, rec := range recorded.Find("roster.catalogue_app.webhook_changed") {
		step, _ := rec.GetData().AsMap()["step"].(string)
		out = append(out, fmt.Sprintf("%s:%s", step, rec.GetOutcome().GetResult().String()))
	}
	return out
}

// kargoPath is a Kargo receiver's path, spelled out here rather than taken
// from the code under test: /github/ and the hex SHA-256 of the project, the
// receiver's name and the secret, joined with nothing.
func kargoPath(project, receiver, secret string) string {
	sum := sha256.Sum256([]byte(project + receiver + secret))
	return "/github/" + hex.EncodeToString(sum[:])
}

const webhookCatalogue = `
apps:
  - id: renovate
    org: globex
    permissions: {contents: write}
    events: [push]
    export: true
    webhook:
      kargo: {base: %s, receiver: github, project: apps}
  - id: releases
    org: globex
    permissions: {contents: write}
`

// A webhook App is created with a secret this service generated (never the
// conversion's), GitHub is told it with the derived URL, an install keeps
// it, and a rotation is only as good as the consumer: it moves GitHub to the
// new secret and URL after the consumer proved it holds the new one, and
// puts the old secret back when it does not.
func TestAWebhookAppIsCreatedWithAGeneratedSecretAndRotatedWithoutLosingDeliveries(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, client, recorded := catalogueServer(t)
	ctx := context.Background()
	console.deps.WebhookPingWindow, console.deps.WebhookPingEvery = 80*time.Millisecond, 10*time.Millisecond

	secretOf := func() string {
		secret, _, err := console.deps.GitHubCatalogueApps.WebhookSecret(ctx, "renovate")
		if err != nil {
			t.Fatalf("WebhookSecret: %v", err)
		}
		return secret
	}
	kargo := startConsumer(t, secretOf, func(secret string) string { return kargoPath("apps", "github", secret) })
	console.deps.WebhookHTTP = kargo.server.Client()
	declared, err := catalogue.Parse([]byte(fmt.Sprintf(webhookCatalogue, kargo.server.URL)))
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	console.deps.GitHubCatalogue = declared

	// The manifest asks for an active webhook, at a placeholder under the base.
	begun, err := console.BeginGitHubCatalogueAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "renovate"}))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	var manifest githubapp.Manifest
	if err = json.Unmarshal([]byte(begun.Msg.GetManifest()), &manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.HookAttributes.Active || manifest.HookAttributes.URL != kargo.server.URL+"/github/pending" ||
		!slices.Equal(manifest.DefaultEvents, []string{"push"}) {
		t.Errorf("hook attributes = %+v, events %v", manifest.HookAttributes, manifest.DefaultEvents)
	}
	cookie, state := cookieFrom(t, begun.Header()), mustQuery(t, begun.Msg.GetUrl(), "state")
	created := redirect(server.githubCatalogueCallback, githubCatalogueCallbackPath, url.Values{"code": {"created"}, "state": {state}}, cookie)
	if created.Code != 302 {
		t.Fatalf("after Create = %d:\n%s", created.Code, created.Body)
	}

	// The secret is ours, kept beside the pending key, and GitHub has it with
	// the URL derived from it.
	data := catalogueSecret(t, client)
	first := string(data["renovate.webhook_secret"])
	if first == "" || first == "from-the-conversion" || len(first) < 40 {
		t.Fatalf("the webhook secret = %q", first)
	}
	wantURL := kargo.server.URL + kargoPath("apps", "github", first)
	if github.hookSecret != first || github.hookURL != wantURL || github.hookContentType != "json" || github.hookPatches != 1 {
		t.Errorf("GitHub holds url %q (want %q), secret match %t, content %q, patches %d",
			github.hookURL, wantURL, github.hookSecret == first, github.hookContentType, github.hookPatches)
	}
	var record catalogueapp.Record
	if err = json.Unmarshal(data["renovate.record.json"], &record); err != nil || record.WebhookURL != wantURL || record.HookRotatedAt.IsZero() {
		t.Errorf("record = %+v, %v", record, err)
	}
	if strings.Contains(string(data["renovate.record.json"]), first) {
		t.Error("the secret is in the record")
	}

	// Install keeps the secret beside the three keys.
	github.app, github.events = map[string]string{"contents": "write", "metadata": "read"}, []string{"push"}
	github.installed = map[string]string{"contents": "write", "metadata": "read"}
	installed := redirect(server.githubCatalogueSetup, githubCatalogueSetupPath,
		url.Values{"installation_id": {"999"}, "state": {mustQuery(t, created.Header().Get("Location"), "state")}}, cookieFrom(t, created.Header()))
	if installed.Code != 302 {
		t.Fatalf("after Install = %d:\n%s", installed.Code, installed.Body)
	}
	data = catalogueSecret(t, client)
	if string(data["renovate.webhook_secret"]) != first || string(data["renovate.github_app_private_key"]) != github.pem {
		t.Errorf("after Install the keys are %v", slices.Sorted(maps.Keys(data)))
	}
	if app := catalogueApp(t, console, "renovate"); app.GetState() != catalogueInstalled || len(app.GetDrift()) != 0 {
		t.Errorf("installed = %s, drift %v, reason %q", app.GetState(), app.GetDrift(), app.GetReason())
	}

	rotate := func(id string) (*directoryrosterv1.GitHubApp, error) {
		out, err := console.RotateGitHubAppWebhook(operator(), connect.NewRequest(&directoryrosterv1.RotateGitHubAppWebhookRequest{Id: id}))
		if err != nil {
			return nil, err
		}
		return out.Msg.GetApp(), nil
	}

	// A consumer that has not reloaded: the new secret does not verify, so
	// GitHub is never told and the old secret is back.
	if _, err = rotate("renovate"); connect.CodeOf(err) != connect.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "did not accept the new secret") || !strings.Contains(err.Error(), "previous secret is back") {
		t.Fatalf("a rotation the consumer does not follow = %v", err)
	}
	if secretOf() != first || github.hookSecret != first || github.hookURL != wantURL || github.hookPatches != 1 {
		t.Errorf("a refused rotation moved something: stored %t, GitHub %t, patches %d", secretOf() == first, github.hookSecret == first, github.hookPatches)
	}
	wantSteps := []string{"configured:RESULT_SUCCESS", "staged:RESULT_SUCCESS", "verified:RESULT_FAILURE", "restored:RESULT_SUCCESS"}
	if got, want := auditSteps(recorded), wantSteps; !slices.Equal(got, want) {
		t.Errorf("audit steps = %v, want %v", got, want)
	}

	// A consumer that follows the console's secret: the ping verifies, and
	// GitHub gets the new secret and the new URL in one PATCH.
	kargo.mu.Lock()
	kargo.live = true
	kargo.mu.Unlock()
	app, err := rotate("renovate")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	second := secretOf()
	secondURL := kargo.server.URL + kargoPath("apps", "github", second)
	if second == first || github.hookSecret != second || github.hookURL != secondURL || github.hookPatches != 2 {
		t.Errorf("after rotation GitHub holds url %q (want %q), secret is the new one %t, patches %d",
			github.hookURL, secondURL, github.hookSecret == second, github.hookPatches)
	}
	if app.GetWebhookUrl() != secondURL || app.GetWebhookRotatedAt() == nil || len(app.GetDrift()) != 0 {
		t.Errorf("view after rotation = url %q, rotated %v, drift %v", app.GetWebhookUrl(), app.GetWebhookRotatedAt(), app.GetDrift())
	}
	if got, want := auditSteps(recorded)[4:], []string{"staged:RESULT_SUCCESS", "verified:RESULT_SUCCESS", "rotated:RESULT_SUCCESS"}; !slices.Equal(got, want) {
		t.Errorf("audit steps = %v, want %v", got, want)
	}
	for _, rec := range recorded.Records() {
		if strings.Contains(rec.String(), first) || strings.Contains(rec.String(), second) {
			t.Fatalf("a secret is in the audit trail: %s", rec)
		}
	}

	// GitHub refusing the PATCH after the consumer is ready: the old secret
	// is put back, and the URL GitHub has stays the one it had.
	github.hookPatchRefused = true
	if _, err = rotate("renovate"); connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "GitHub would not take the new webhook") {
		t.Fatalf("a PATCH GitHub refuses = %v", err)
	}
	if secretOf() != second || github.hookSecret != second || github.hookURL != secondURL {
		t.Error("a refused PATCH left the console and GitHub with different secrets")
	}

	// Somebody edits the webhook on GitHub: drift, with what to do.
	github.hookURL, github.hookContentType = "https://elsewhere.example/hook", "form"
	checked, err := console.CheckGitHubApp(operator(), connect.NewRequest(&directoryrosterv1.CheckGitHubAppRequest{Id: "renovate"}))
	if err != nil {
		t.Fatal(err)
	}
	drift := strings.Join(checked.Msg.GetApp().GetDrift(), "\n")
	if checked.Msg.GetApp().GetState() != appDrifted || !strings.Contains(drift, "other than the one this service set") ||
		!strings.Contains(drift, `delivers as "form"`) {
		t.Errorf("a hand-edited webhook = %s:\n%s", checked.Msg.GetApp().GetState(), drift)
	}
	if _, err = rotate("renovate"); err != nil {
		t.Fatalf("a rotation repairs it: %v", err)
	}
	checked, _ = console.CheckGitHubApp(operator(), connect.NewRequest(&directoryrosterv1.CheckGitHubAppRequest{Id: "renovate"}))
	if len(checked.Msg.GetApp().GetDrift()) != 0 {
		t.Errorf("after the repair: %v", checked.Msg.GetApp().GetDrift())
	}

	// An App with no webhook has nothing to rotate.
	if _, err = rotate("releases"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("rotating an App with no webhook = %v", err)
	}
	if _, err = rotate("nothing"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("rotating an unknown App = %v", err)
	}
}

// A plain URL does not move when the secret does, and GitHub is told the
// declared URL at creation.
func TestAPlainURLWebhookIsTheDeclaredURL(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, _, _ := catalogueServer(t)
	declared, err := catalogue.Parse([]byte(`
apps:
  - id: renovate
    org: globex
    permissions: {contents: write}
    events: [push]
    webhook: {url: "https://argocd.example/api/webhook"}
`))
	if err != nil {
		t.Fatal(err)
	}
	console.deps.GitHubCatalogue = declared
	begun, err := console.BeginGitHubCatalogueAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "renovate"}))
	if err != nil {
		t.Fatal(err)
	}
	var manifest githubapp.Manifest
	_ = json.Unmarshal([]byte(begun.Msg.GetManifest()), &manifest)
	if manifest.HookAttributes != (githubapp.HookAttributes{URL: "https://argocd.example/api/webhook", Active: true}) {
		t.Errorf("hook attributes = %+v", manifest.HookAttributes)
	}
	created := redirect(server.githubCatalogueCallback, githubCatalogueCallbackPath,
		url.Values{"code": {"created"}, "state": {mustQuery(t, begun.Msg.GetUrl(), "state")}}, cookieFrom(t, begun.Header()))
	if created.Code != 302 || github.hookURL != "https://argocd.example/api/webhook" || github.hookSecret == "" || github.hookSecret == "from-the-conversion" {
		t.Errorf("after Create = %d, GitHub holds %q", created.Code, github.hookURL)
	}
}

// GitHub refusing the webhook at creation leaves the App created, its key
// kept and the page saying how to finish.
func TestACreatedAppWhoseWebhookGitHubRefusesCanStillBeFinished(t *testing.T) {
	github := startFakeGitHub(t)
	server, console, client, recorded := catalogueServer(t)
	declared, _ := catalogue.Parse([]byte(`
apps:
  - id: renovate
    org: globex
    permissions: {contents: write}
    events: [push]
    webhook: {url: "https://argocd.example/api/webhook"}
`))
	console.deps.GitHubCatalogue = declared
	github.hookPatchRefused = true
	begun, _ := console.BeginGitHubCatalogueAppConnect(operator(),
		connect.NewRequest(&directoryrosterv1.BeginGitHubCatalogueAppConnectRequest{Id: "renovate"}))
	created := redirect(server.githubCatalogueCallback, githubCatalogueCallbackPath,
		url.Values{"code": {"created"}, "state": {mustQuery(t, begun.Msg.GetUrl(), "state")}}, cookieFrom(t, begun.Header()))
	if created.Code != 409 || !strings.Contains(created.Body.String(), "webhook could not be set") {
		t.Fatalf("after Create = %d:\n%s", created.Code, created.Body)
	}
	if string(catalogueSecret(t, client)["renovate.pending_private_key"]) != github.pem {
		t.Error("the App's key was lost with its webhook")
	}
	if got := auditSteps(recorded); !slices.Equal(got, []string{"configured:RESULT_FAILURE"}) {
		t.Errorf("audit steps = %v", got)
	}
}
