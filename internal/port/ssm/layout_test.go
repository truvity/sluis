package ssm_test

import (
	"context"
	"regexp"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/port/ssm"
	"github.com/truvity/sluis/internal/portstore"
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
// `/sluis/<instance>/private/credentials/<kind>/<id>/<ref>`.
func TestACredentialIsAParameterUnderTheCredentialsPrefix(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	set := port.Set{State: memory.New(), Secrets: newSecrets(t, f, ssm.Config{})}
	base := portstore.New(set)
	at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)

	cred := backend.Credential{Type: "service-account-key", Admin: "a@b.example", Data: []byte(`{"k":"v"}`)}
	if err := portstore.NewCredentials(base).Save(ctx, "C01ipl6j0", cred); err != nil {
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
		regexp.MustCompile(`^/sluis/test/private/credentials/directory/google/C01ipl6j0/[0-9a-f]{24}$`),
		regexp.MustCompile(`^/sluis/test/private/credentials/slack-workspace/acme/[0-9a-f]{24}$`),
		regexp.MustCompile(`^/sluis/test/private/credentials/slack-app/alerts/[0-9a-f]{24}$`),
		regexp.MustCompile(`^/sluis/test/private/credentials/console/session-key$`),
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
