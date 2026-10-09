package githubapp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubapp/githubfake"
)

// A catalogue App that declares a webhook is created with it active, at the
// declared URL, or for a Kargo receiver at a placeholder under its base; one
// that does not keeps the webhook off whatever events it subscribes to.
func TestAManifestActivatesTheWebhookOnlyWhenOneIsDeclared(t *testing.T) {
	t.Parallel()
	app := catalogue.App{ID: "argocd", Org: "example-org", Permissions: map[string]string{"contents": "read"}, Events: []string{"push"}}
	plain := githubapp.ManifestFor(app, "https://access.example/console", "r", "s", nil)
	if plain.HookAttributes.Active || plain.HookAttributes.URL != "https://access.example/console" {
		t.Errorf("without a webhook = %+v", plain.HookAttributes)
	}
	app.Webhook = &catalogue.Webhook{URL: "https://argocd.example/api/webhook"}
	if got := githubapp.ManifestFor(app, "h", "r", "s", nil).HookAttributes; !got.Active || got.URL != "https://argocd.example/api/webhook" {
		t.Errorf("with a URL = %+v", got)
	}
	app.Webhook = &catalogue.Webhook{Kargo: &catalogue.Kargo{Base: "https://kargo.example", Receiver: "github"}}
	if got := githubapp.ManifestFor(app, "h", "r", "s", nil).HookAttributes; !got.Active || got.URL != "https://kargo.example/github/pending" {
		t.Errorf("with a Kargo receiver = %+v", got)
	}
}

// The webhook is read and replaced as the App; GitHub masks the secret; the
// deliveries are listed and redelivered by id.
func TestTheHookConfigIsReadAndReplacedAsTheApp(t *testing.T) {
	fake := githubfake.Start(t, "example-org")
	ctx := context.Background()

	config, err := githubapp.GetHookConfig(ctx, fake.Client(), "app-jwt")
	if err != nil || config.URL != "" || config.Secret != "" {
		t.Errorf("a fresh App's webhook = %+v, %v", config, err)
	}
	patched, err := githubapp.PatchHookConfig(ctx, fake.Client(), "app-jwt", githubapp.HookUpdate{URL: "https://argocd.example/api/webhook", Secret: "s3cret"})
	if err != nil || patched.URL != "https://argocd.example/api/webhook" || patched.ContentType != "json" {
		t.Errorf("PatchHookConfig = %+v, %v", patched, err)
	}
	if fake.Hook.Secret != "s3cret" || fake.Hook.Patches != 1 {
		t.Errorf("GitHub holds %+v", fake.Hook)
	}
	config, err = githubapp.GetHookConfig(ctx, fake.Client(), "app-jwt")
	if err != nil || config.Secret != "********" || strings.Contains(config.Secret, "s3cret") {
		t.Errorf("the secret is not masked: %+v, %v", config, err)
	}
	if _, err = githubapp.PatchHookConfig(ctx, fake.Client(), "app-jwt", githubapp.HookUpdate{URL: "https://x.example"}); err == nil {
		t.Error("a webhook with no secret was sent")
	}
	var status *githubapp.StatusError
	if _, err = githubapp.GetHookConfig(ctx, fake.Client(), ""); !errors.As(err, &status) || status.Code != http.StatusUnauthorized {
		t.Errorf("a read with no token = %v", err)
	}

	fake.Deliveries = []githubapp.Delivery{{ID: 9, GUID: "g-9", Event: "push", StatusCode: 502}, {ID: 8, GUID: "g-8", Event: "ping", StatusCode: 204}}
	deliveries, err := githubapp.ListDeliveries(ctx, fake.Client(), "app-jwt", 30)
	if err != nil || len(deliveries) != 2 || deliveries[0].ID != 9 || deliveries[0].StatusCode != 502 {
		t.Errorf("ListDeliveries = %+v, %v", deliveries, err)
	}
	if err = githubapp.Redeliver(ctx, fake.Client(), "app-jwt", 9); err != nil || len(fake.Redelivered) != 1 || fake.Redelivered[0] != 9 {
		t.Errorf("Redeliver = %v, %v", err, fake.Redelivered)
	}
	if err = githubapp.Redeliver(ctx, fake.Client(), "app-jwt", 77); err == nil {
		t.Error("an unknown delivery was redelivered")
	}
}

// A ping is signed the way GitHub signs, with the event header, and only a
// 2xx answers for the target; the error never carries the target's URL,
// which for a Kargo receiver is derived from the secret.
func TestAPingIsSignedAsGitHubSignsAndOnlyA2xxAnswersForTheTarget(t *testing.T) {
	t.Parallel()
	var event, signature string
	var body []byte
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		event, signature = r.Header.Get("X-GitHub-Event"), r.Header.Get("X-Hub-Signature-256")
		body, _ = io.ReadAll(r.Body)
		if r.URL.Path != "/github/ok" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(target.Close)

	if err := githubapp.Ping(context.Background(), target.Client(), target.URL+"/github/ok", "s3cret"); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if event != "ping" || signature != githubapp.Sign("s3cret", body) || !strings.HasPrefix(signature, "sha256=") || githubapp.Sign("other", body) == signature {
		t.Errorf("event %q, signature %q", event, signature)
	}
	// Known answer: HMAC-SHA256("key", "The quick brown fox jumps over the lazy dog").
	const known = "sha256=f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	if got := githubapp.Sign("key", []byte("The quick brown fox jumps over the lazy dog")); got != known {
		t.Errorf("Sign = %s", got)
	}

	err := githubapp.Ping(context.Background(), target.Client(), target.URL+"/github/derived-from-the-secret", "s3cret")
	if err == nil || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "derived-from-the-secret") {
		t.Errorf("a 404 = %v", err)
	}
	down := httptest.NewServer(http.NotFoundHandler())
	url := down.URL + "/github/derived-from-the-secret"
	down.Close()
	if err = githubapp.Ping(context.Background(), http.DefaultClient, url, "s3cret"); err == nil || strings.Contains(err.Error(), "derived-from-the-secret") {
		t.Errorf("an unreachable target = %v", err)
	}
}
