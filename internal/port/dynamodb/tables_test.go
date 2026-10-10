package dynamodb

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/porttest"
	"github.com/truvity/sluis/internal/port/porttest/grantcost"
)

// newTables opens the table of every module over one fake, made by Create.
func newTables(t *testing.T, f *fakeAPI) *Tables {
	t.Helper()
	inst := fmt.Sprintf("t%d", tables.Add(1))
	tt, err := NewTables(ctx(), f, Config{Tables: DefaultTables(inst), Create: true}, WithPollInterval(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func (f *fakeAPI) count() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.calls)
}

// has reports whether the table holds an item with that partition and sort key.
func (f *fakeAPI) has(table, pk, sk string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.tables[table][pk][sk]
	return ok
}

func TestTableNamesAreSluisInstanceModule(t *testing.T) {
	got := DefaultTables("prod")
	if len(got) != len(port.Modules()) {
		t.Fatalf("%d names for %d modules", len(got), len(port.Modules()))
	}
	for _, m := range port.Modules() {
		if want := "sluis-prod-" + string(m); got[m] != want {
			t.Errorf("%s: %q, want %q", m, got[m], want)
		}
	}
}

// The families each module's table holds in the conformance run: a record with
// a lifetime, and a permanent one (a key of no module is every table's own).
var conformanceFamilies = map[port.Module][2]string{
	port.ModuleOIDC:       {"tok.", "rec.t."},
	port.ModuleGitHub:     {"gate.github.", "gh.org."},
	port.ModuleSlack:      {"cache.slack.user.", "ws.slack."},
	port.ModuleGoogle:     {"cache.", "ws.dir.google."},
	port.ModuleCloudflare: {"cache.", "rec.t."},
	port.ModuleBackup:     {"cache.", "rec.t."},
}

// The whole conformance suite over the table of every module, each through the
// bare Store (the ownership check is the adapter's own).
func TestConformanceOfEveryModulesTableOverTheFake(t *testing.T) {
	for _, m := range port.Modules() {
		t.Run(string(m), func(t *testing.T) {
			porttest.Run(t, func(t *testing.T) porttest.Env {
				tt := newTables(t, newFake())
				s, _ := tt.Store(m)
				fam := conformanceFamilies[m]
				return porttest.Env{Set: s.Set(), Advance: s.Advance, Skips: otherPorts, RecordPrefix: fam[0], PermanentPrefix: fam[1]}
			})
		})
	}
}

func TestAWriteOfAnotherModulesKeyIsRefusedBeforeAnyRequest(t *testing.T) {
	f := newFake()
	tt := newTables(t, f)
	gh, err := tt.Set(port.ModuleGitHub)
	if err != nil {
		t.Fatal(err)
	}
	slackKey := "ws.slack.T01"
	before := f.count()
	for name, call := range map[string]func() error{
		"Put":    func() error { _, err := gh.State.Put(ctx(), slackKey, []byte("x"), time.Hour); return err },
		"Create": func() error { _, err := gh.State.Create(ctx(), slackKey, []byte("x"), time.Hour); return err },
		"Update": func() error { _, err := gh.State.Update(ctx(), slackKey, []byte("x"), time.Hour, "1"); return err },
		"Delete": func() error { return gh.State.Delete(ctx(), slackKey) },
		"DeleteIfRevision": func() error {
			return gh.State.DeleteIfRevision(ctx(), slackKey, "1")
		},
		"a lease of another module": func() error {
			_, err := gh.State.Create(ctx(), "lease.slack-tick:T01", []byte("x"), time.Hour)
			return err
		},
		"Index.Add": func() error { return gh.Index.Add(ctx(), "issuer:sso-clients:ada", "m", time.Hour) },
	} {
		if err := call(); !errors.Is(err, port.ErrNotOwner) {
			t.Errorf("%s: err = %v, want ErrNotOwner", name, err)
		}
	}
	// The bare store refuses too (the Owned wrapper is not the only guard).
	bare, _ := tt.Store(port.ModuleGitHub)
	if _, err := bare.Put(ctx(), slackKey, []byte("x"), time.Hour); !errors.Is(err, port.ErrNotOwner) {
		t.Errorf("bare Put: %v", err)
	}
	// A read of it is absent, and costs nothing: it is not in this table.
	if _, err := gh.State.Get(ctx(), slackKey); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("Get: %v", err)
	}
	if p, err := gh.State.List(ctx(), "ws.slack.", "", 0); err != nil || len(p.Records) != 0 {
		t.Errorf("List: %v %v", p, err)
	}
	if got := f.count(); !maps.Equal(got, before) {
		t.Errorf("requests were made: %v -> %v", before, got)
	}
	// Its own keys, and a key of no module, are written.
	for _, k := range []string{"gh.org.acme", "lease.github-tick:acme", "gate.x.y"} {
		if _, err := gh.State.Put(ctx(), k, []byte("x"), time.Hour); err != nil {
			t.Errorf("own key %s: %v", k, err)
		}
	}
	// A backend with no module is not written anywhere.
	if _, err := gh.State.Put(ctx(), "ws.dir.entra.x", []byte("x"), time.Hour); !errors.Is(err, port.ErrUnsupported) {
		t.Errorf("no-module backend: %v", err)
	}
}

func TestAPeerReadGoesToThePeersTableOnly(t *testing.T) {
	f := newFake()
	tt := newTables(t, f)
	gh, _ := tt.Set(port.ModuleGitHub)
	sl, _ := tt.Set(port.ModuleSlack)
	issuer, err := tt.Set(port.ModuleOIDC, port.ModuleGitHub, port.ModuleSlack, port.ModuleGoogle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gh.State.Put(ctx(), "gh.org.acme", []byte("gh"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err = sl.State.Put(ctx(), "ws.slack.T01", []byte("sl"), 0); err != nil {
		t.Fatal(err)
	}
	ghT, _ := tt.Store(port.ModuleGitHub)
	slT, _ := tt.Store(port.ModuleSlack)
	oiT, _ := tt.Store(port.ModuleOIDC)
	if !f.has(ghT.Table(), "org", "acme") || f.has(slT.Table(), "org", "acme") || f.has(oiT.Table(), "org", "acme") {
		t.Fatal("the github record is not in the github table alone")
	}

	peer, ok := issuer.Peer(port.ModuleGitHub)
	if !ok {
		t.Fatal("no github peer")
	}
	rec, err := peer.Get(ctx(), "gh.org.acme")
	if err != nil || string(rec.Value) != "gh" {
		t.Fatalf("peer Get: %v %q", err, rec.Value)
	}
	// The slack record is not visible through the github peer.
	if _, err = peer.Get(ctx(), "ws.slack.T01"); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("a slack key through the github peer: %v", err)
	}
	if page, err := peer.List(ctx(), "gh.org.", "", 0); err != nil || len(page.Records) != 1 {
		t.Errorf("peer List: %v %v", page, err)
	}
	// The own State does not see the peer's records either.
	if _, err = issuer.State.Get(ctx(), "gh.org.acme"); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("own Get of a github key: %v", err)
	}
	// The peer cannot write: it carries no State.
	if _, writes := any(peer).(port.State); writes {
		t.Error("a peer view can be asserted back to a State")
	}
	// A module not granted is not there.
	if _, ok = issuer.Peer(port.ModuleBackup); ok {
		t.Error("a backup peer without a grant")
	}
	if _, ok = gh.Peer(port.ModuleSlack); ok {
		t.Error("a peer on a set with none")
	}
}

func TestEachTableHasItsOwnLeaseAndNotify(t *testing.T) {
	f := newFake()
	tt := newTables(t, f)
	gh, _ := tt.Store(port.ModuleGitHub)
	sl, _ := tt.Store(port.ModuleSlack)

	// The same lease key is free in each table: they do not share a lease.
	for _, s := range []*Store{gh, sl} {
		if _, err := s.Create(ctx(), "lease.tick:all", []byte("a"), time.Hour); err != nil {
			t.Fatalf("%s: %v", s.Table(), err)
		}
		if _, err := s.Create(ctx(), "lease.tick:all", []byte("b"), time.Hour); !errors.Is(err, port.ErrExists) {
			t.Fatalf("%s: second taker: %v", s.Table(), err)
		}
	}
	la, err := port.Locate5("lease.tick:all")
	if err != nil || la.Kind != "lease" || la.Module != "" {
		t.Fatalf("lease address %+v %v", la, err)
	}
	if !f.has(gh.Table(), "lease", la.ID) || !f.has(sl.Table(), "lease", la.ID) {
		t.Fatal("the lease is not an item of each table")
	}

	// A notification reaches the subscribers of its own table only.
	got := make(chan string, 4)
	other := make(chan string, 4)
	stop := gh.Subscribe(func(target string) { got <- "gh:" + target })
	defer stop()
	stop2 := sl.Subscribe(func(target string) { other <- "sl:" + target })
	defer stop2()
	if err := gh.Notify(ctx(), "github.acme"); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v != "gh:github.acme" {
			t.Fatalf("delivered %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the notification did not arrive")
	}
	select {
	case v := <-other:
		t.Fatalf("a slack subscriber got %q", v)
	case <-time.After(150 * time.Millisecond):
	}
	if !f.has(gh.Table(), "notify", "github.acme") || f.has(sl.Table(), "notify", "github.acme") {
		t.Fatal("notify is not an item of the notifying table alone")
	}
}

func TestWatchAndExportStatePerTable(t *testing.T) {
	f := newFake()
	tt := newTables(t, f)
	gh, _ := tt.Set(port.ModuleGitHub)
	sl, _ := tt.Set(port.ModuleSlack)
	ghBare, _ := tt.Store(port.ModuleGitHub)

	ch, err := ghBare.Watch(ctx(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gh.State.Put(ctx(), "gh.org.acme", []byte("1"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err = sl.State.Put(ctx(), "ws.slack.T01", []byte("2"), 0); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Key != "gh.org.acme" {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	select {
	case ev := <-ch:
		t.Fatalf("the github table's watch saw %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}

	var keys []string
	exp := gh.State.(port.StateExporter)
	if err = exp.ExportState(ctx(), "", func(e port.Exported) error { keys = append(keys, e.Key); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "gh.org.acme" {
		t.Fatalf("exported %v, want the github record alone", keys)
	}
}

func TestTheIndexIsTheOIDCTablesAlone(t *testing.T) {
	tt := newTables(t, newFake())
	oi, _ := tt.Set(port.ModuleOIDC)
	if err := oi.Index.Add(ctx(), "issuer:sso-clients:ada", "c1", time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err := oi.Index.Members(ctx(), "issuer:sso-clients:ada")
	if err != nil || len(got) != 1 || got[0] != "c1" {
		t.Fatalf("members: %v %v", got, err)
	}
	gh, _ := tt.Set(port.ModuleGitHub)
	if got, err = gh.Index.Members(ctx(), "issuer:sso-clients:ada"); err != nil || len(got) != 0 {
		t.Fatalf("another table: %v %v", got, err)
	}
}

func TestOpeningMakesNoRequestAndPingNamesTheMissingTable(t *testing.T) {
	f := newFake()
	cfg := Config{Tables: DefaultTables("lazy")}
	tt, err := NewTables(ctx(), f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.count()); n != 0 {
		t.Fatalf("opening made requests: %v", f.count())
	}
	st, _ := tt.Set(port.ModuleGitHub)
	// First use of a table that is not there is the store being down.
	if _, err = st.State.Get(ctx(), "gh.org.acme"); !errors.Is(err, port.ErrUnavailable) {
		t.Fatalf("Get on a missing table: %v, want ErrUnavailable", err)
	}
	err = tt.Ping(ctx())
	if !errors.Is(err, port.ErrUnavailable) {
		t.Fatalf("Ping: %v", err)
	}
	for _, m := range port.Modules() {
		if !strings.Contains(err.Error(), "sluis-lazy-"+string(m)) {
			t.Errorf("Ping does not name %s: %v", m, err)
		}
	}
	// Made by Create, the probe passes.
	cfg.Create = true
	tt, err = NewTables(ctx(), f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = tt.Ping(ctx()); err != nil {
		t.Fatalf("Ping after Create: %v", err)
	}
}

func TestSetRefusesAModuleWithoutATable(t *testing.T) {
	tt, err := NewTables(ctx(), newFake(), Config{Tables: map[port.Module]string{port.ModuleGitHub: "only-github"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tt.Set(port.ModuleSlack); err == nil {
		t.Error("a set for a module with no table")
	}
	if _, err = tt.Set(port.ModuleGitHub, port.ModuleSlack); err == nil {
		t.Error("a peer with no table")
	}
	if got := tt.Modules(); len(got) != 1 || got[0] != port.ModuleGitHub {
		t.Errorf("Modules = %v", got)
	}
}

func TestTheConfigurationIsChecked(t *testing.T) {
	f := newFake()
	for name, cfg := range map[string]Config{
		"none":           {},
		"both":           {Table: "a", Tables: map[port.Module]string{port.ModuleOIDC: "b"}},
		"not a module":   {Tables: map[port.Module]string{"nope": "b"}},
		"an empty name":  {Tables: map[port.Module]string{port.ModuleOIDC: ""}},
		"a shared table": {Tables: map[port.Module]string{port.ModuleOIDC: "x", port.ModuleGitHub: "x"}},
	} {
		if _, err := NewTables(ctx(), f, cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A layout 4 table with tables set is refused by New too.
	if _, err := New(ctx(), f, Config{Table: "a", Tables: map[port.Module]string{port.ModuleOIDC: "b"}}); err == nil {
		t.Error("New accepted both")
	}
}

// What one grant costs over the tables, through the router: the same budget as
// one table, so the split does not add a request.
func TestGrantCostOverTheTables(t *testing.T) {
	grantcost.Run(t, func(t *testing.T) grantcost.Env {
		f := newFake()
		tt := newTables(t, f)
		r := tt.Router()
		return grantcost.Env{
			Set:     port.Set{State: r, Index: r},
			Advance: tt.Advance,
			Calls:   f.count,
		}
	})
}

func TestTheRouterSendsAKeyToItsModulesTable(t *testing.T) {
	f := newFake()
	tt := newTables(t, f)
	r := tt.Router()
	for key, mod := range map[string]port.Module{
		"gh.org.acme": port.ModuleGitHub, "ws.slack.T01": port.ModuleSlack, "ws.dir.google.w1": port.ModuleGoogle,
		"tok.x": port.ModuleOIDC, "gate.y.z": port.ModuleOIDC, "lease.slack-tick:a": port.ModuleSlack,
	} {
		if _, err := r.Put(ctx(), key, []byte("v"), time.Hour); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		a, _ := port.Locate5(key)
		s, _ := tt.Store(mod)
		if !f.has(s.Table(), a.Kind, a.ID) {
			t.Errorf("%s is not in the %s table", key, mod)
		}
		if _, err := r.Get(ctx(), key); err != nil {
			t.Errorf("%s: Get: %v", key, err)
		}
	}
	if _, err := r.List(ctx(), "ws.", "", 0); !errors.Is(err, port.ErrUnsupported) {
		t.Errorf("a prefix of two modules: %v", err)
	}
}

func TestTheRouterExportsStateAndIndex(t *testing.T) {
	tt := newTables(t, newFake())
	r := tt.Router()
	for key, ttl := range map[string]time.Duration{
		"issuer:session-token:abc": time.Hour, "issuer:keyring:entry:ES384:k1": 24 * time.Hour,
		"issuer:kms:state-secret-fingerprint": 48 * time.Hour, "gh.org.acme": 0, "ws.slack.T01": 0,
	} {
		if _, err := r.Put(ctx(), key, []byte("v:"+key), ttl); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	if err := r.Add(ctx(), "issuer:sessions-of:ada", "s1", time.Hour); err != nil {
		t.Fatal(err)
	}

	// The issuer's prefix spans several kinds and lies in the oidc table alone.
	got := map[string]port.Exported{}
	if err := r.ExportState(ctx(), "issuer:", func(x port.Exported) error { got[x.Key] = x; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("exported %d issuer records, want 3: %v", len(got), got)
	}
	if x := got["issuer:keyring:entry:ES384:k1"]; string(x.Value) != "v:issuer:keyring:entry:ES384:k1" || x.TTL <= 23*time.Hour || x.TTL > 24*time.Hour+2*time.Second {
		t.Errorf("ring entry = %q, %v", x.Value, x.TTL)
	}
	// One module's family is read from its table, and a prefix in none from all.
	var keys []string
	collect := func(x port.Exported) error { keys = append(keys, x.Key); return nil }
	if err := r.ExportState(ctx(), "gh.org.", collect); err != nil || len(keys) != 1 || keys[0] != "gh.org.acme" {
		t.Errorf("gh.org. = %v, %v", keys, err)
	}
	keys = nil
	if err := r.ExportState(ctx(), "", collect); err != nil || len(keys) != 5 {
		t.Errorf("everything = %v, %v", keys, err)
	}

	sets := map[string]port.Exported{}
	if err := r.ExportIndex(ctx(), "issuer:", func(x port.Exported) error { sets[x.Key] = x; return nil }); err != nil {
		t.Fatal(err)
	}
	if x := sets["issuer:sessions-of:ada"]; len(sets) != 1 || len(x.Members) != 1 || x.Members[0] != "s1" || x.TTL <= 0 {
		t.Errorf("sets = %v", sets)
	}
}
