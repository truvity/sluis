package ssm_test

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/exports"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/port/secretsexport"
	"github.com/truvity/sluis/internal/port/ssm"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/rails"
	slackcatalogueapp "github.com/truvity/sluis/internal/slackapp/catalogueapp"
	"github.com/truvity/sluis/internal/slackroster/connection"
)

func names(f *fakeAPI) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.params {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Layout v2: the credentials of the domain stores are parameters under
// `/sluis/private/credentials/<kind>/<id>/<ref>`.
func TestACredentialIsAParameterUnderTheCredentialsPrefix(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	set := port.Set{State: memory.New(), Secrets: newSecrets(t, f, ssm.Config{})}
	base := portstore.New(set)
	at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)

	if err := portstore.NewCredentials(base).Save(ctx, "C01ipl6j0", backend.Credential{Type: "service-account-key", Admin: "a@b.example", Data: []byte(`{"k":"v"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := portstore.NewSlackWorkspaces(base).Put(ctx,
		connection.Record{Workspace: "acme", TeamID: "T1", AppID: "A1", BotUserID: "U1", ConnectedAt: at, ConnectedBy: "ada@acme.example"},
		connection.Credential{Workspace: "acme", AppID: "A1", ClientID: "c1", ClientSecret: "s", BotToken: "xoxb-1"}); err != nil {
		t.Fatal(err)
	}
	if err := portstore.NewSlackCatalogueApps(base).Put(ctx,
		slackcatalogueapp.Record{ID: "alerts", Workspace: "acme", AppID: "A2", ClientID: "c2", TeamID: "T1", CreatedAt: at, CreatedBy: "ada@acme.example"},
		slackcatalogueapp.Credentials{ClientSecret: "s", BotToken: "xoxb-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := base.SessionKey(ctx, func() ([]byte, error) { return []byte("key"), nil }); err != nil {
		t.Fatal(err)
	}
	want := []*regexp.Regexp{
		regexp.MustCompile(`^/sluis/private/credentials/directory/google/C01ipl6j0/[0-9a-f]{24}$`),
		regexp.MustCompile(`^/sluis/private/credentials/slack-workspace/acme/[0-9a-f]{24}$`),
		regexp.MustCompile(`^/sluis/private/credentials/slack-app/alerts/[0-9a-f]{24}$`),
		regexp.MustCompile(`^/sluis/private/credentials/console/session-key$`),
	}
	got := names(f)
	if len(got) != len(want) {
		t.Fatalf("parameters = %v, want %d", got, len(want))
	}
	for _, re := range want {
		if !slices.ContainsFunc(got, re.MatchString) {
			t.Errorf("no parameter matches %s in %v", re, got)
		}
	}
}

// An exports pass over the ssm Secrets adapter writes `/sluis/export/<name>`,
// one JSON object of the properties.
func TestAnExportsPassWritesTheExportPrefix(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	secrets := newSecrets(t, f, ssm.Config{})
	state := memory.New()
	slacks := portstore.NewSlackCatalogueApps(portstore.New(state.Set()))
	at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	if err := slacks.Put(ctx,
		slackcatalogueapp.Record{ID: "alerts", Workspace: "acme", AppID: "A1", ClientID: "c1", TeamID: "T1", CreatedAt: at, CreatedBy: "ada@acme.example"},
		slackcatalogueapp.Credentials{ClientSecret: "s", BotToken: "xoxb-BOT"}); err != nil {
		t.Fatal(err)
	}
	specs, err := exports.FromConfig([]config.Export{{Source: "slack-app", App: "alerts", Path: "slack-app-alerts"}},
		exports.Declared{SlackApps: []string{"alerts"}})
	if err != nil {
		t.Fatal(err)
	}
	runner := &exports.Runner{
		Specs: specs, Sources: exports.Sources{SlackCatalogueApps: slacks},
		Export: secretsexport.New(secrets), State: state,
		Leases: &rails.Leases{State: state, Holder: "test"},
	}
	if res := runner.Pass(ctx); res.Done != 1 || res.Failed != 0 {
		t.Fatalf("Pass = %+v", res)
	}
	got, err := secrets.Get(ctx, "export/slack-app-alerts")
	if err != nil {
		t.Fatal(err)
	}
	var props map[string]string
	if err := json.Unmarshal(got.Value, &props); err != nil || props["bot_token"] != "xoxb-BOT" || len(props) != 1 {
		t.Fatalf("the export = %s (%v)", got.Value, err)
	}
	if want := []string{"/sluis/export/slack-app-alerts"}; !slices.Equal(names(f), want) {
		t.Errorf("parameters = %v, want %v", names(f), want)
	}
	// A second pass makes no new version.
	if res := runner.Pass(ctx); res.Failed != 0 {
		t.Fatalf("second Pass = %+v", res)
	}
	if again, _ := secrets.Get(ctx, "export/slack-app-alerts"); again.Version != got.Version {
		t.Errorf("an identical export made a new version: %s -> %s", got.Version, again.Version)
	}
}
