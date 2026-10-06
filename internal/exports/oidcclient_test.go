package exports_test

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/exports"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/rails"
)

func clients() exports.Declared {
	d := declared()
	d.Clients = map[string]bool{"grafana": true, "argocd": false, "console": false, "metabase": true}
	return d
}

func oidcExport(client, path string) config.Export {
	return config.Export{Source: "oidc-client", Client: client, Namespace: "example", Path: path}
}

func TestAnOIDCClientExportIsAReplaceWithTheDefaultProperties(t *testing.T) {
	t.Parallel()
	specs, err := exports.FromConfig([]config.Export{oidcExport("grafana", "oidc/grafana")}, clients())
	if err != nil {
		t.Fatal(err)
	}
	s := specs[0]
	if s.Name != "oidc-client.grafana" || s.Source != exports.SourceOIDCClient || s.Client != "grafana" {
		t.Errorf("spec = %+v", s)
	}
	if s.Mode() != port.ExportReplace {
		t.Errorf("mode = %v, want replace", s.Mode())
	}
	if want := map[string]string{"client-id": "client-id", "client-secret": "client-secret"}; !maps.Equal(s.Properties, want) {
		t.Errorf("properties = %v", s.Properties)
	}
	if s.Target != (port.ExportTarget{Namespace: "example", Path: "oidc/grafana"}) || s.Interval != exports.DefaultInterval {
		t.Errorf("target %v interval %v", s.Target, s.Interval)
	}
	// An own name replaces the default.
	e := oidcExport("grafana", "oidc/grafana")
	e.Name = "grafana-copy"
	if specs, err = exports.FromConfig([]config.Export{e}, clients()); err != nil || specs[0].Name != "grafana-copy" {
		t.Errorf("named: %+v, %v", specs, err)
	}
}

func TestAnOIDCClientExportIsRefused(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		mutate func(*config.Export)
		want   string
	}{
		"no client":                    {func(e *config.Export) { e.Client = "" }, "needs `client`"},
		"an unknown client":            {func(e *config.Export) { e.Client = "nobody" }, "not a client of the policy"},
		"a client with a named secret": {func(e *config.Export) { e.Client = "argocd" }, "does not have `secret: {generate: true}`"},
		"a public or exchange client":  {func(e *config.Export) { e.Client = "console" }, "does not have `secret: {generate: true}`"},
		"a stray app":                  {func(e *config.Export) { e.App = "alerts" }, "no app, tier, org or bundle"},
		"a stray tier":                 {func(e *config.Export) { e.Tier = "stable" }, "no app, tier, org or bundle"},
		"a stray org":                  {func(e *config.Export) { e.Org = "truvity" }, "no app, tier, org or bundle"},
		"a stray bundle":               {func(e *config.Export) { e.Bundle = "github-apps" }, "no app, tier, org or bundle"},
		"a property it has not got":    {func(e *config.Export) { e.Properties = map[string]string{"private_key": "x"} }, "not a property of oidc-client"},
		"a bad property name":          {func(e *config.Export) { e.Properties = map[string]string{"client-secret": "a b"} }, "not a property name"},
		"two properties to one name": {func(e *config.Export) {
			e.Properties = map[string]string{"client-id": "x", "client-secret": "x"}
		}, "both written as"},
		"no path": {func(e *config.Export) { e.Path = "" }, "path"},
	} {
		e := oidcExport("grafana", "oidc/grafana")
		c.mutate(&e)
		if _, err := exports.FromConfig([]config.Export{e}, clients()); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, c.want)
		}
	}
}

func TestClientIsRefusedOnTheOtherSources(t *testing.T) {
	t.Parallel()
	for name, e := range map[string]config.Export{
		"slack-app":  {Source: "slack-app", App: "alerts", Client: "grafana", Path: "a/b"},
		"github-app": {Source: "github-app", App: "renovate", Client: "grafana", Path: "a/b"},
		"runner-app": {Source: "runner-app", Tier: "stable", Org: "truvity", Client: "grafana", Path: "a/b"},
		"bundle":     {Source: "bundle", Bundle: "github-apps", Client: "grafana", Path: "a/b"},
	} {
		if _, err := exports.FromConfig([]config.Export{e}, clients()); err == nil || !strings.Contains(err.Error(), "client") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAnOIDCClientExportMayRenameOrNarrowItsProperties(t *testing.T) {
	t.Parallel()
	e := oidcExport("grafana", "oidc/grafana")
	e.Properties = map[string]string{"client-secret": "GF_AUTH_SECRET"}
	specs, err := exports.FromConfig([]config.Export{e}, clients())
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"client-secret": "GF_AUTH_SECRET"}; !maps.Equal(specs[0].Properties, want) {
		t.Errorf("properties = %v", specs[0].Properties)
	}
}

func TestTwoReplaceExportsOnOneTargetClash(t *testing.T) {
	t.Parallel()
	a, b := oidcExport("grafana", "oidc/shared"), oidcExport("metabase", "oidc/shared")
	if _, err := exports.FromConfig([]config.Export{a, b}, clients()); err == nil || !strings.Contains(err.Error(), "replaces") {
		t.Errorf("two clients to one key: %v", err)
	}
	// One namespace apart, or one path apart, is fine.
	b.Namespace = "devel"
	if _, err := exports.FromConfig([]config.Export{a, b}, clients()); err != nil {
		t.Errorf("another namespace: %v", err)
	}
	b = oidcExport("metabase", "oidc/metabase")
	if _, err := exports.FromConfig([]config.Export{a, b}, clients()); err != nil {
		t.Errorf("another path: %v", err)
	}
	// And a client's secret over a key an App patches would erase it.
	app := config.Export{Source: "slack-app", App: "alerts", Namespace: "example", Path: "oidc/grafana"}
	both := []config.Export{app, oidcExport("grafana", "oidc/grafana")}
	if _, err := exports.FromConfig(both, clients()); err == nil || !strings.Contains(err.Error(), "replaces") {
		t.Errorf("a client over a patched key: %v", err)
	}
	// The same client twice, to two places, is allowed; the default name then clashes.
	again := oidcExport("grafana", "oidc/elsewhere")
	if _, err := exports.FromConfig([]config.Export{oidcExport("grafana", "oidc/grafana"), again}, clients()); err == nil {
		t.Error("two exports with one default name were accepted")
	}
	again.Name = "grafana-elsewhere"
	if _, err := exports.FromConfig([]config.Export{oidcExport("grafana", "oidc/grafana"), again}, clients()); err != nil {
		t.Errorf("the same client twice under two names: %v", err)
	}
}

func TestClientsAreNotCheckedWhenTheyAreNotKnown(t *testing.T) {
	t.Parallel()
	if _, err := exports.FromConfig([]config.Export{oidcExport("anything", "oidc/x")}, declared()); err != nil {
		t.Errorf("no clients given: %v", err)
	}
}

// --- Sources over a Secrets port

func specFor(t *testing.T, e config.Export) exports.Spec {
	t.Helper()
	specs, err := exports.FromConfig([]config.Export{e}, clients())
	if err != nil {
		t.Fatal(err)
	}
	return specs[0]
}

func putRecord(t *testing.T, s port.Secrets, client string, rec clientcreds.Record) {
	t.Helper()
	body, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put(ctx, clientcreds.Path(client), body); err != nil {
		t.Fatal(err)
	}
}

func TestSourcesReadsTheCurrentSecretAndNeverThePrevious(t *testing.T) {
	t.Parallel()
	store := memory.NewSecrets()
	putRecord(t, store, "grafana", clientcreds.Record{
		Current: "the-current", Previous: "the-previous", PreviousValidUntil: time.Now().Add(time.Hour), Created: time.Now(),
	})
	got, found, err := exports.Sources{Secrets: store}.Read(ctx, specFor(t, oidcExport("grafana", "oidc/grafana")))
	if err != nil || !found {
		t.Fatalf("%v, %v", found, err)
	}
	if want := map[string]string{"client-id": "grafana", "client-secret": "the-current"}; !maps.Equal(got, want) {
		t.Errorf("read %v, want %v", got, want)
	}
	for _, v := range got {
		if v == "the-previous" {
			t.Error("the previous secret was exported")
		}
	}

	e := oidcExport("grafana", "oidc/grafana")
	e.Properties = map[string]string{"client-secret": "GF_SECRET"}
	got, _, err = exports.Sources{Secrets: store}.Read(ctx, specFor(t, e))
	if err != nil || !maps.Equal(got, map[string]string{"GF_SECRET": "the-current"}) {
		t.Errorf("narrowed: %v, %v", got, err)
	}
}

func TestSourcesAMissingRecordIsNothingToCopyAndNotAnError(t *testing.T) {
	t.Parallel()
	_, found, err := exports.Sources{Secrets: memory.NewSecrets()}.Read(ctx, specFor(t, oidcExport("grafana", "oidc/grafana")))
	if err != nil || found {
		t.Errorf("found %v, err %v", found, err)
	}
}

type getFails struct {
	port.Secrets
	err error
}

func (g getFails) Get(context.Context, string) (port.Secret, error) { return port.Secret{}, g.err }

func TestSourcesARecordThatCannotBeReadIsAnErrorWithoutTheSecret(t *testing.T) {
	t.Parallel()
	spec := specFor(t, oidcExport("grafana", "oidc/grafana"))
	for name, body := range map[string]string{
		"corrupt":       `{"v":1,"current":"leaky-value","created":`,
		"wrong version": `{"v":9,"current":"leaky-value"}`,
		"no current":    `{"v":1,"current":""}`,
	} {
		store := memory.NewSecrets()
		if _, err := store.Put(ctx, clientcreds.Path("grafana"), []byte(body)); err != nil {
			t.Fatal(err)
		}
		_, found, err := exports.Sources{Secrets: store}.Read(ctx, spec)
		if err == nil || found {
			t.Errorf("%s: found %v, err %v", name, found, err)
			continue
		}
		if strings.Contains(err.Error(), "leaky-value") {
			t.Errorf("%s: the error holds the secret: %v", name, err)
		}
	}
	boom := errors.New("openbao sealed")
	_, _, err := exports.Sources{Secrets: getFails{memory.NewSecrets(), boom}}.Read(ctx, spec)
	if !errors.Is(err, boom) {
		t.Errorf("a Get error was not propagated: %v", err)
	}
}

func TestSourcesCheckRefusesAnOIDCClientExportWithoutASecretsPort(t *testing.T) {
	t.Parallel()
	specs, err := exports.FromConfig([]config.Export{oidcExport("grafana", "oidc/grafana")}, clients())
	if err != nil {
		t.Fatal(err)
	}
	if err = (exports.Sources{}).Check(specs); err == nil {
		t.Error("accepted with no Secrets port")
	}
	if err = (exports.Sources{Secrets: memory.NewSecrets()}).Check(specs); err != nil {
		t.Errorf("refused with one: %v", err)
	}
}

// --- The runner

type clientRig struct {
	store  *memory.Secrets
	out    *memory.Export
	runner *exports.Runner
	state  *memory.Store
}

func newClientRig(t *testing.T, clientIDs ...string) *clientRig {
	t.Helper()
	state := memory.New()
	rig := &clientRig{store: memory.NewSecrets(), out: memory.NewExport(), state: state}
	var entries []config.Export
	for _, id := range clientIDs {
		entries = append(entries, oidcExport(id, "oidc/"+id))
	}
	specs, err := exports.FromConfig(entries, clients())
	if err != nil {
		t.Fatal(err)
	}
	rig.runner = &exports.Runner{
		Specs: specs, Sources: exports.Sources{Secrets: rig.store}, Export: rig.out, State: state,
		Leases: &rails.Leases{State: state, Holder: "test"},
		Settle: 5 * time.Millisecond, MinGap: 5 * time.Millisecond, Backoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
		NoStagger: true,
	}
	return rig
}

func (r *clientRig) target(id string) port.ExportTarget {
	return port.ExportTarget{Namespace: "example", Path: "oidc/" + id}
}

func (r *clientRig) seed(t *testing.T, id string) clientcreds.Record {
	t.Helper()
	clientcreds.Reconcile(ctx, []string{id}, r.store, nil, time.Now(), nil, clientcreds.Hooks{})
	got, err := r.store.Get(ctx, clientcreds.Path(id))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := clientcreds.DecodeRecord(got.Value)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestPassWritesAClientSecretByReplaceAndIsIdempotent(t *testing.T) {
	t.Parallel()
	r := newClientRig(t, "grafana", "metabase")
	rec := r.seed(t, "grafana")

	res := r.runner.Pass(ctx)
	if res.Exports != 2 || res.Done != 2 || res.Failed != 0 || res.Contended != 0 {
		t.Fatalf("pass = %+v", res)
	}
	got, ok := r.out.Get(r.target("grafana"))
	if !ok || !maps.Equal(got, map[string]string{"client-id": "grafana", "client-secret": rec.Current}) {
		t.Errorf("grafana copy = %v, %v", got, ok)
	}
	if _, ok = r.out.Get(r.target("metabase")); ok {
		t.Error("a copy was made for a client with no record")
	}
	writes := r.out.Writes()

	// Again: the same copy, no new write of a different value.
	if res = r.runner.Pass(ctx); res.Done != 2 || res.Failed != 0 {
		t.Fatalf("second pass = %+v", res)
	}
	if again, _ := r.out.Get(r.target("grafana")); !maps.Equal(again, got) {
		t.Errorf("the copy changed: %v", again)
	}
	_ = writes
}

func TestAfterARotationTheCopyChanges(t *testing.T) {
	t.Parallel()
	r := newClientRig(t, "grafana")
	before := r.seed(t, "grafana")
	r.runner.Pass(ctx)

	manager := &clientcreds.Manager{Store: r.store, Generated: func(id string) bool { return id == "grafana" }}
	manager.Changed = func(ctx context.Context, id string) { r.runner.RefreshClient(ctx, id) }
	if _, err := manager.Rotate(ctx, "grafana", time.Hour); err != nil {
		t.Fatal(err)
	}
	after, ok := r.out.Get(r.target("grafana"))
	if !ok {
		t.Fatal("no copy")
	}
	if after["client-secret"] == before.Current || len(after["client-secret"]) != 43 {
		t.Errorf("the copy still holds the old secret: %v", after)
	}
	rec, _ := r.store.Get(ctx, clientcreds.Path("grafana"))
	stored, _ := clientcreds.DecodeRecord(rec.Value)
	if after["client-secret"] != stored.Current {
		t.Error("the copy is not the record's current secret")
	}
	for _, v := range after {
		if v == stored.Previous {
			t.Error("the previous secret was copied")
		}
	}
	if len(after) != 2 {
		t.Errorf("a replace left other properties: %v", after)
	}
}

func TestRefreshClientRunsOnlyTheMatchingExports(t *testing.T) {
	t.Parallel()
	r := newClientRig(t, "grafana", "metabase")
	r.seed(t, "grafana")
	r.seed(t, "metabase")
	r.runner.RefreshClient(ctx, "grafana")
	if _, ok := r.out.Get(r.target("grafana")); !ok {
		t.Error("the matching export did not run")
	}
	if _, ok := r.out.Get(r.target("metabase")); ok {
		t.Error("an export of another client ran")
	}
	if r.out.Writes() != 1 {
		t.Errorf("%d writes, want 1", r.out.Writes())
	}
	r.runner.RefreshClient(ctx, "nobody")
	if r.out.Writes() != 1 {
		t.Error("a client with no export wrote")
	}
}

func TestRefreshClientSkipsAnExportWhoseLeaseIsHeld(t *testing.T) {
	t.Parallel()
	r := newClientRig(t, "grafana")
	r.seed(t, "grafana")
	other := &rails.Leases{State: r.state, Holder: "another-replica"}
	var once sync.Once
	released := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_, _ = other.Do(ctx, exports.KindExport, "oidc-client.grafana", func(context.Context) {
			once.Do(func() { close(held) })
			<-released
		})
	}()
	<-held
	r.runner.RefreshClient(ctx, "grafana")
	if _, ok := r.out.Get(r.target("grafana")); ok || r.out.Writes() != 0 {
		t.Error("an export ran under another replica's lease")
	}
	close(released)
	eventually(t, "the lease to be free", func() bool {
		r.runner.RefreshClient(ctx, "grafana")
		_, ok := r.out.Get(r.target("grafana"))
		return ok
	})
}

func TestAPassTellsAContendedExportAndAFailedOne(t *testing.T) {
	t.Parallel()
	r := newClientRig(t, "grafana")
	r.seed(t, "grafana")
	r.out.Fail(errors.Join(port.ErrUnavailable, errors.New("sealed")))
	if res := r.runner.Pass(ctx); res.Failed != 1 || res.Done != 0 {
		t.Errorf("a failing store: %+v", res)
	}
	r.out.Fail(nil)
	if res := r.runner.Pass(ctx); res.Done != 1 {
		t.Errorf("recovered: %+v", res)
	}
}
