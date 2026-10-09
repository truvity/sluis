package secretstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/memory"
)

func put(t *testing.T, v state.Value[[]byte]) {
	t.Helper()
	if _, err := v.Put(context.Background(), []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
}

// Every v5 name lands at its module-first address, with no config or
// credentials level, and nowhere else.
func TestV5PathGolden(t *testing.T) {
	root := memory.New()
	s := secretstore.FromStoreV5(root, "")
	ctx := context.Background()

	put(t, s.OIDC().StateSecret())
	put(t, s.OIDC().RecoveryPassword())
	put(t, s.OIDC().ConsoleSessionKey())
	put(t, s.OIDC().SignInClientID("google"))
	put(t, s.OIDC().SignInClientSecret("google"))
	put(t, s.OIDC().Client("rp"))
	put(t, s.GitHub().AppCredential("app-1", "r1"))
	if _, err := s.GitHub().PersonToken("alice").Put(ctx, secretstore.PersonToken{AccessToken: "a"}, ""); err != nil {
		t.Fatal(err)
	}
	put(t, s.Slack().WorkspaceCredential("T1", "r1"))
	put(t, s.Slack().AppCredential("bot", "r1"))
	put(t, s.Google().WorkspaceKey("ws"))
	put(t, s.Backup().KeyRef("seal"))
	m, err := s.Cloudflare().Minter("main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Put(ctx, secretstore.CloudflareMinterv1{Token: "t"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cloudflare().Minted("p").Put(ctx, secretstore.CloudflareMinted{}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OIDCExternal().Client("rp").Put(ctx, secretstore.OIDCv1{ClientID: "rp", ClientSecret: "s"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GitHubExternal().App("runner-small-org").Put(ctx, secretstore.GitHubv1{AppID: "1", InstallationID: "2", PrivateKey: "k", WebhookSecret: "w"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SlackExternal().App("bot").Put(ctx, secretstore.Slackv1{BotToken: "t"}, ""); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"external/github/runner-small-org",
		"external/oidc/rp",
		"external/slack/bot",
		"internal/backup/keys/seal",
		"internal/cloudflare/main/minter",
		"internal/cloudflare/minted/p",
		"internal/github/apps/app-1/r1",
		"internal/cloudflare/main/minter",
		"internal/cloudflare/minted/p",
		"internal/github/links/alice",
		"internal/google/workspaces/ws/key",
		"internal/oidc/clients/rp",
		"internal/oidc/console-session-key",
		"internal/oidc/recovery-password",
		"internal/oidc/signin/google/client-id",
		"internal/oidc/signin/google/client-secret",
		"internal/oidc/state-secret",
		"internal/slack/apps/bot/r1",
		"internal/slack/workspaces/T1/r1",
	}
	for _, addr := range want {
		if _, err := root.Get(ctx, addr); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

// external/oidc/<client> is byte-identical in v4 and v5.
func TestV5ExternalOIDCMatchesV4(t *testing.T) {
	ctx := context.Background()
	r4, r5 := memory.New(), memory.New()
	doc := secretstore.OIDCv1{ClientID: "rp", ClientSecret: "s"}
	if _, err := secretstore.FromStore(r4, "").External.OIDC("rp").Put(ctx, doc, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := secretstore.FromStoreV5(r5, "").OIDCExternal().Client("rp").Put(ctx, doc, ""); err != nil {
		t.Fatal(err)
	}
	a, err := r4.Get(ctx, "external/oidc/rp")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r5.Get(ctx, "external/oidc/rp")
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Value) != string(b.Value) {
		t.Fatalf("v4 %s\nv5 %s", a.Value, b.Value)
	}
}

// A view of module X cannot take a ref of module Y.
func TestCheckModuleRefRefusesOtherModules(t *testing.T) {
	ok := map[string]string{
		"internal/backup/r2":             "r2",
		"internal/backup/targets/a/cred": "targets/a/cred",
	}
	for ref, want := range ok {
		got, err := secretstore.CheckModuleRef(secretstore.ModuleBackup, ref)
		if err != nil || got != want {
			t.Errorf("%s: %q, %v", ref, got, err)
		}
	}
	for _, ref := range []string{
		"internal/oidc/r2", "external/backup/r2", "internal/backup/", "internal/backup",
		"internal/backup/../oidc/x", "internal/backup//x", "internal/backupx/r2", "r2",
	} {
		if _, err := secretstore.CheckModuleRef(secretstore.ModuleBackup, ref); !errors.Is(err, secretstore.ErrRef) {
			t.Errorf("%s: err %v, want ErrRef", ref, err)
		}
	}
	s := secretstore.FromStoreV5(memory.New(), "")
	if _, err := s.Google().S3Credentials("internal/backup/r2"); !errors.Is(err, secretstore.ErrRef) {
		t.Fatalf("google view took a backup ref: %v", err)
	}
	if _, err := s.Backup().S3Credentials("internal/backup/r2"); err != nil {
		t.Fatal(err)
	}
}

// A view's prefix is its module, and each module is named once.
func TestV5ViewsAreScopedToTheirModule(t *testing.T) {
	s := secretstore.FromStoreV5(memory.New(), "")
	views := map[secretstore.Module]interface{ Prefix() string }{
		secretstore.ModuleOIDC: s.OIDC(), secretstore.ModuleGitHub: s.GitHub(), secretstore.ModuleSlack: s.Slack(),
		secretstore.ModuleCloudflare: s.Cloudflare(), secretstore.ModuleGoogle: s.Google(), secretstore.ModuleBackup: s.Backup(),
	}
	if len(views) != len(secretstore.Modules()) {
		t.Fatalf("%d views for %d modules", len(views), len(secretstore.Modules()))
	}
	for m, v := range views {
		if v.Prefix() != "internal/"+string(m) {
			t.Errorf("%s: prefix %s", m, v.Prefix())
		}
		if got, err := secretstore.ParseModule(string(m)); err != nil || got != m {
			t.Errorf("ParseModule(%s) = %v, %v", m, got, err)
		}
	}
	if _, err := secretstore.ParseModule("config"); err == nil {
		t.Fatal("config is not a module")
	}
	if _, err := s.Cloudflare().Minter("minted"); !errors.Is(err, secretstore.ErrRef) {
		t.Fatalf("an account named like the records directory: %v", err)
	}
}
