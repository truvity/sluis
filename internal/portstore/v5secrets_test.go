package portstore_test

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/secretstore"
	slackcatalogueapp "github.com/truvity/sluis/internal/slackapp/catalogueapp"
	slackconnection "github.com/truvity/sluis/internal/slackroster/connection"
	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/memory"
)

// recorder is a secrets store that remembers every address written or removed,
// in full, below the root it was made at.
type recorder struct {
	state.Store
	path string
	log  *recLog
}

type recLog struct {
	mu      sync.Mutex
	puts    []string
	deletes []string
}

func newRecorder() (*recorder, *recLog) {
	l := &recLog{}
	return &recorder{Store: memory.New(), log: l}, l
}

func (r *recorder) at(key string) string {
	if r.path == "" {
		return key
	}
	return r.path + "/" + key
}

func (r *recorder) Put(ctx context.Context, key string, value []byte, ifRev state.Rev) (state.Rev, error) {
	r.log.mu.Lock()
	r.log.puts = append(r.log.puts, r.at(key))
	r.log.mu.Unlock()
	return r.Store.Put(ctx, key, value, ifRev)
}

func (r *recorder) Delete(ctx context.Context, key string) error {
	r.log.mu.Lock()
	r.log.deletes = append(r.log.deletes, r.at(key))
	r.log.mu.Unlock()
	return r.Store.Delete(ctx, key)
}

func (r *recorder) Child(prefix string, opts ...state.Option) state.Store {
	return &recorder{Store: r.Store.Child(prefix, opts...), path: r.at(prefix), log: r.log}
}

// written are the addresses written, in order.
func (l *recLog) written() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.puts)
}

func (l *recLog) removed() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.deletes)
}

func (l *recLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.puts, l.deletes = nil, nil
}

// wantPaths fails unless every pattern matches exactly one of the addresses
// and no address is left over. A fresh ref is 24 hex digits.
func wantPaths(t *testing.T, what string, got []string, patterns ...string) {
	t.Helper()
	if len(got) != len(patterns) {
		t.Errorf("%s: got %v, want %d addresses like %v", what, got, len(patterns), patterns)
		return
	}
	for _, p := range patterns {
		re := regexp.MustCompile("^" + strings.ReplaceAll(p, "<ref>", "[0-9a-f]{24}") + "$")
		if !slices.ContainsFunc(got, re.MatchString) {
			t.Errorf("%s: no address like %s in %v", what, p, got)
		}
	}
}

// v5base is a Base whose secrets are layout v5 over a recording fake and whose
// Secrets port is unset, as on a layout v5 installation.
func v5base(t *testing.T, e env) (*portstore.Base, *recLog, *secretstore.StoresV5) {
	t.Helper()
	set := e.open(t)
	set.Secrets = nil
	root, l := newRecorder()
	stores := secretstore.FromStoreV5(root, "")
	return portstore.New(set).WithV5(stores), l, stores
}

func TestGoogleWorkspaceKeyIsOnLayoutV5(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		b, l, _ := v5base(t, e)
		creds := portstore.NewCredentials(b)
		cred := backend.Credential{Type: "service-account-key", Admin: "root@example.test", Data: []byte(`{"private_key":"TOPSECRET"}`)}
		for range 2 { // a rewrite replaces the key in place: one address
			if err := creds.Save(ctx, "C01", cred); err != nil {
				t.Fatal(err)
			}
		}
		wantPaths(t, "Save", slices.Compact(l.written()), "internal/google/workspaces/C01/key")
		got, found, err := creds.Load(ctx, "C01")
		if err != nil || !found || string(got.Data) != string(cred.Data) || got.Admin != cred.Admin {
			t.Fatalf("Load = %+v, %v, %v", got, found, err)
		}
		// A workspace record beside the key neither moves nor drops it.
		ws := portstore.NewWorkspaces(b)
		record := hub.Workspace{ID: "C01", Backend: "google", Domains: []string{"example.test"}, Admin: "root@example.test",
			Credential: hub.CredentialServiceAccountKey, ConnectedAt: time.Unix(1700000000, 0).UTC()}
		for range 2 {
			if err = ws.Put(ctx, record); err != nil {
				t.Fatal(err)
			}
		}
		if _, found, _ = creds.Load(ctx, "C01"); !found {
			t.Error("a record write dropped the key")
		}
		if got := l.removed(); len(got) != 0 {
			t.Errorf("nothing was to be removed: %v", got)
		}
		if err = creds.Delete(ctx, "C01"); err != nil {
			t.Fatal(err)
		}
		wantPaths(t, "Delete", l.removed(), "internal/google/workspaces/C01/key")
		if _, found, _ = creds.Load(ctx, "C01"); found {
			t.Error("the key outlived Delete")
		}
	})
}

func TestGitHubLinkTokensAreOnLayoutV5(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		b, l, _ := v5base(t, e)
		s := portstore.NewGitHubLinks(b)
		if _, err := s.Claim(ctx, selfLink(101, "ada", "ada@example.test"), time.Now()); err != nil {
			t.Fatal(err)
		}
		wantPaths(t, "Claim", l.written(), "internal/github/links/101/<ref>")
		got := byID(t, s, 101)
		if got.AccessToken != "ghu_ACCESS_ada" || got.RefreshToken != "ghr_REFRESH_ada" {
			t.Fatalf("the tokens did not come back: %+v", got)
		}
		// A refresh writes the new pair under a fresh ref and removes the old.
		l.reset()
		if _, err := s.Update(ctx, []link.Link{refreshed(got)}); err != nil {
			t.Fatal(err)
		}
		wantPaths(t, "Update writes", l.written(), "internal/github/links/101/<ref>")
		wantPaths(t, "Update removes", l.removed(), "internal/github/links/101/<ref>")
		if again := byID(t, s, 101); again.RefreshToken != "ghr_NEW_ada" {
			t.Errorf("the refreshed tokens did not land: %+v", again)
		}
	})
}

func refreshed(l link.Link) link.Link {
	l.AccessToken, l.RefreshToken = "ghu_NEW_ada", "ghr_NEW_ada"
	return l
}

func TestSlackCredentialsAreOnLayoutV5(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		b, l, stores := v5base(t, e)
		ws := portstore.NewSlackWorkspaces(b)
		record := slackconnection.Record{Workspace: "acme", TeamID: "T1", AppID: "A1", BotUserID: "U1", ConnectedAt: time.Unix(1700000000, 0).UTC()}
		cred := slackconnection.Credential{Workspace: "acme", AppID: "A1", ClientID: "c1", ClientSecret: "CLIENT-SECRET", BotToken: "xoxb-TOPSECRET"}
		if err := ws.Put(ctx, record, cred); err != nil {
			t.Fatal(err)
		}
		wantPaths(t, "workspace Put", l.written(), "internal/slack/workspaces/acme/<ref>")
		if _, got, found, err := ws.Get(ctx, "acme"); err != nil || !found || got.BotToken != cred.BotToken {
			t.Fatalf("Get = %+v, %v, %v", got, found, err)
		}
		l.reset()
		if err := ws.Delete(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		wantPaths(t, "workspace Delete", l.removed(), "internal/slack/workspaces/acme/<ref>")

		// A catalogue App's bot token is external/slack/<id>; its client secret is internal.
		l.reset()
		apps := portstore.NewSlackCatalogueApps(b)
		srec := slackcatalogueapp.Record{
			ID: "notifier", Workspace: "acme", AppID: "A1", ClientID: "c1", AuthorizeURL: "https://slack.example/install",
			CreatedAt: time.Unix(1700000000, 0).UTC(), TeamID: "T1", BotUserID: "U1",
		}
		if err := apps.Put(ctx, srec, slackcatalogueapp.Credentials{ClientSecret: "CLIENT-SECRET", BotToken: "xoxb-BOT"}); err != nil {
			t.Fatal(err)
		}
		wantPaths(t, "app Put", l.written(), "external/slack/notifier", "internal/slack/apps/notifier/<ref>")
		_, got, ok, err := apps.Get(ctx, "notifier")
		if err != nil || !ok || got.BotToken != "xoxb-BOT" || got.ClientSecret != "CLIENT-SECRET" {
			t.Fatalf("app Get = %+v, %v, %v", got, ok, err)
		}
		if doc, _, err := stores.SlackExternal().App("notifier").Get(ctx); err != nil || doc.BotToken != "xoxb-BOT" {
			t.Fatalf("the exported token = %v", err)
		}
		l.reset()
		if err = apps.Delete(ctx, "notifier"); err != nil {
			t.Fatal(err)
		}
		wantPaths(t, "app Delete", l.removed(), "external/slack/notifier", "internal/slack/apps/notifier/<ref>")
		if _, _, err = stores.SlackExternal().App("notifier").Get(ctx); !errors.Is(err, state.ErrNotFound) {
			t.Errorf("the exported token outlived the App: %v", err)
		}
	})
}

// An organisation has no key of its own on layout v5: it names an App.
func TestAnOrganisationHasNoKeyOnLayoutV5(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		b, l, _ := v5base(t, e)
		_, ok, err := portstore.NewGitHubOrgs(b).Credential(ctx, "acme")
		if err != nil || ok {
			t.Fatalf("Credential of nobody = %v, %v", ok, err)
		}
		if got := l.written(); len(got) != 0 {
			t.Errorf("a read wrote %v", got)
		}
	})
}
