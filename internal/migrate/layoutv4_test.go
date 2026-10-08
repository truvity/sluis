package migrate_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/storage/state"
	statememory "github.com/truvity/sluis/storage/state/memory"
)

// The values the layout fixtures add to seed's. None may appear in a report or
// an error.
var layoutSecrets = []string{
	"SEEDED-STATE-SECRET", "SEEDED-CLIENT-SECRET", "GENERATED-CURRENT", "GENERATED-PREVIOUS", "OTHER-SECRET", "RECOVERY-PW",
}

// fakeConfig is internal/config as a map.
type fakeConfig struct {
	mu sync.Mutex
	m  map[string]string
}

func (f *fakeConfig) Get(_ context.Context, name string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[name]
	return v, ok, nil
}

func (f *fakeConfig) Create(_ context.Context, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[name]; ok {
		return state.ErrConflict
	}
	f.m[name] = value
	return nil
}

// layoutEnv is an installation on layout v3 with one of everything in it, and
// an empty v4.
type layoutEnv struct {
	set  port.Set
	root state.Store
	dest *secretstore.Stores
	cfg  *fakeConfig
}

func newLayoutEnv(t *testing.T) *layoutEnv {
	t.Helper()
	set := memory.New().Set()
	seed(t, portSide(set, store.AdapterDynamoDB))
	for path, v := range map[string]string{
		"config/issuer/state-secret":    "SEEDED-STATE-SECRET",
		"config/recovery/password":      "RECOVERY-PW",
		"config/clients/rp/secret":      "SEEDED-CLIENT-SECRET",
		"config/providers/google/g/id":  "google-id",
		"config/providers/google/g/sec": "google-sec",
	} {
		if _, err := set.Secrets.Put(ctx, path, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	rec, err := clientcreds.Record{Current: "GENERATED-CURRENT", Previous: "GENERATED-PREVIOUS", PreviousValidUntil: time.Now().Add(time.Hour)}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = set.Secrets.Put(ctx, "credentials/oidc-client/gen/secret", rec); err != nil {
		t.Fatal(err)
	}
	if _, err = set.Secrets.Put(ctx, "export/old-copy", []byte("EXPORT-COPY")); err != nil {
		t.Fatal(err)
	}
	root := statememory.New()
	return &layoutEnv{set: set, root: root, dest: secretstore.FromStore(root, secretstore.LayoutV4, ""), cfg: &fakeConfig{m: map[string]string{}}}
}

func (e *layoutEnv) options() migrate.SecretsLayoutOptions {
	return migrate.SecretsLayoutOptions{
		V3: e.set.Secrets, State: e.set.State, Dest: e.dest, Config: e.cfg,
		ExportedGitHubApp: func(id string) bool { return id == "renovate" }, Layout: "transition",
	}
}

func noValues(t *testing.T, what, text string) {
	t.Helper()
	for _, s := range append(append([]string{}, secrets...), layoutSecrets...) {
		if strings.Contains(text, s) {
			t.Errorf("%s contains %q", what, s)
		}
	}
}

func TestADryRunNamesAddressesAndWritesNothing(t *testing.T) {
	e := newLayoutEnv(t)
	o := e.options()
	o.DryRun = true
	rep, err := migrate.MoveSecretsLayout(ctx, o)
	if err != nil || !rep.OK {
		t.Fatalf("dry run = %v\n%s", err, rep.JSON())
	}
	if rep.New == 0 || rep.Written != 0 {
		t.Errorf("new %d written %d, want a plan and no writes", rep.New, rep.Written)
	}
	have := map[string]bool{}
	for _, it := range rep.Items {
		have[it.Address] = true
	}
	for _, want := range []string{
		"external/oidc/rp", "external/oidc/gen", "external/github/runner-stable-acme", "external/github/renovate",
		"external/slack/notifier", "internal/credentials/console/session-key", "internal/config/issuer/state-secret",
	} {
		if !have[want] {
			t.Errorf("the plan lacks %s: %v", want, have)
		}
	}
	for _, skipped := range rep.Skipped {
		if skipped != "export/old-copy" {
			t.Errorf("skipped %s", skipped)
		}
	}
	noValues(t, "the dry-run report", string(rep.JSON()))
	if names, err := e.dest.Internal.Store().List(ctx); err != nil || len(names) != 0 {
		t.Errorf("internal holds %v (%v) after a dry run", names, err)
	}
	if len(e.cfg.m) != 0 {
		t.Errorf("config holds %v after a dry run", e.cfg.m)
	}
}

func TestTheMoveWritesEveryItemAndReadsItBack(t *testing.T) {
	e := newLayoutEnv(t)
	rep, err := migrate.MoveSecretsLayout(ctx, e.options())
	if err != nil || !rep.OK {
		t.Fatalf("move = %v\n%s", err, rep.JSON())
	}
	if rep.Written == 0 || rep.Written != rep.Verified || rep.Conflicts != 0 {
		t.Errorf("written %d verified %d conflicts %d", rep.Written, rep.Verified, rep.Conflicts)
	}
	noValues(t, "the report", string(rep.JSON()))

	if doc, _, err := e.dest.External.OIDC("rp").Get(ctx); err != nil || doc.ClientSecret != "SEEDED-CLIENT-SECRET" || doc.ClientID != "rp" {
		t.Errorf("oidc/rp = %+v, %v", doc, err)
	}
	cur, prev, err := e.dest.External.OIDC("gen").Rotating(ctx, time.Hour)
	if err != nil || cur.ClientSecret != "GENERATED-CURRENT" || prev == nil || prev.ClientSecret != "GENERATED-PREVIOUS" {
		t.Errorf("oidc/gen rotating = %+v, %+v, %v: the previous secret is the previous revision", cur, prev, err)
	}
	gh, _, err := e.dest.External.GitHubRunnerApp("stable", "acme").Get(ctx)
	if err != nil || gh.AppID != "3" || gh.InstallationID != "4" || gh.PrivateKey != "RUNNER-KEY" {
		t.Errorf("github runner = %+v, %v", gh, err)
	}
	gh, _, err = e.dest.External.GitHubApp("renovate").Get(ctx)
	if err != nil || gh.AppID != "8" || gh.InstallationID != "11" || gh.PrivateKey != "CAT-KEY" {
		t.Errorf("github renovate = %+v, %v", gh, err)
	}
	if sl, _, err := e.dest.External.SlackApp("notifier").Get(ctx); err != nil || sl.BotToken != "xoxb-BOT" {
		t.Errorf("slack = %+v, %v", sl, err)
	}
	if e.cfg.m["issuer/state-secret"] != "SEEDED-STATE-SECRET" || e.cfg.m["recovery/password"] != "RECOVERY-PW" {
		t.Errorf("config = %v", e.cfg.m)
	}
	if _, ok := e.cfg.m["clients/rp/secret"]; ok {
		t.Error("a confidential client's secret was copied as config; it is an oidc/v1 document")
	}
	// The keys of the Apps are external once: no internal copy.
	for _, dir := range []string{"credentials/github-runner-app/stable/acme", "credentials/github-app/renovate"} {
		if names, err := e.dest.Internal.Store().Child(dir).List(ctx); err != nil || len(names) != 0 {
			t.Errorf("internal %s holds %v (%v): the key is at its external address", dir, names, err)
		}
	}
	// A Slack App's client secret stays internal; its bot token does not.
	names, err := e.dest.Internal.Store().Child("credentials/slack-app/notifier").List(ctx)
	if err != nil || len(names) != 1 {
		t.Fatalf("slack app credential = %v, %v", names, err)
	}
	raw, _, err := state.NewValue(e.dest.Internal.Store(), "credentials/slack-app/notifier/"+names[0], state.Raw()).Get(ctx)
	if err != nil || !strings.Contains(string(raw), "SLACK-CLIENT-SECRET") || strings.Contains(string(raw), "xoxb-BOT") {
		t.Errorf("internal slack credential = %s, %v", raw, err)
	}
	// Everything the callers read through the layout is there.
	sec := secretstore.NewSecrets(e.dest, e.set.Secrets, 0)
	for _, path := range []string{"credentials/console/session-key", "credentials/oidc-client/gen/secret"} {
		if _, err := sec.Get(ctx, path); err != nil {
			t.Errorf("layout Get(%s) = %v", path, err)
		}
	}

	// A second run finds everything in place and writes nothing.
	again, err := migrate.MoveSecretsLayout(ctx, e.options())
	if err != nil || again.Written != 0 || again.New != 0 || again.Unchanged != rep.Written {
		t.Errorf("rerun = %v written %d new %d unchanged %d (first wrote %d)", err, again.Written, again.New, again.Unchanged, rep.Written)
	}
}

// flaky is a store whose writes fail after limit of them have succeeded.
type flaky struct {
	state.Store
	n     *int
	limit int
}

func (f flaky) Put(ctx context.Context, key string, value []byte, ifRev state.Rev) (state.Rev, error) {
	if *f.n >= f.limit {
		return "", errors.New("the disk is on fire")
	}
	*f.n++
	return f.Store.Put(ctx, key, value, ifRev)
}

func (f flaky) Child(prefix string, opts ...state.Option) state.Store {
	return flaky{Store: f.Store.Child(prefix, opts...), n: f.n, limit: f.limit}
}

func TestAFailedRunIsResumed(t *testing.T) {
	e := newLayoutEnv(t)
	n := 0
	broken := e.options()
	broken.Dest = secretstore.FromStore(flaky{Store: e.root, n: &n, limit: 5}, secretstore.LayoutV4, "")
	rep, err := migrate.MoveSecretsLayout(ctx, broken)
	if err == nil || rep.OK {
		t.Fatalf("the run with a failing store = %v", err)
	}
	noValues(t, "the error", err.Error())
	if rep.Written == 0 || rep.Written >= rep.New {
		t.Fatalf("the failed run wrote %d of %d", rep.Written, rep.New)
	}
	first := rep.Written

	rep, err = migrate.MoveSecretsLayout(ctx, e.options())
	if err != nil || !rep.OK {
		t.Fatalf("resume = %v\n%s", err, rep.JSON())
	}
	if rep.Unchanged < first {
		t.Errorf("the resume found %d unchanged, the failed run wrote %d", rep.Unchanged, first)
	}
	if rep.Written != rep.New || rep.Verified != rep.Written+rep.Unchanged {
		t.Errorf("resume: new %d written %d verified %d unchanged %d", rep.New, rep.Written, rep.Verified, rep.Unchanged)
	}
}

func TestADifferentDocumentIsRefusedNamingBothRevisions(t *testing.T) {
	e := newLayoutEnv(t)
	rev, err := e.dest.External.OIDC("rp").Put(ctx, secretstore.OIDCv1{ClientID: "rp", ClientSecret: "OTHER-SECRET"}, "")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := migrate.MoveSecretsLayout(ctx, e.options())
	if !errors.Is(err, migrate.ErrLayoutConflict) {
		t.Fatalf("move = %v, want ErrLayoutConflict", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "external/oidc/rp") || !strings.Contains(msg, "v4 revision "+string(rev)) || !strings.Contains(msg, "version ") {
		t.Errorf("the refusal does not name both revisions: %s", msg)
	}
	noValues(t, "the refusal", msg+string(rep.JSON()))
	if rep.Written != 0 {
		t.Errorf("wrote %d before refusing", rep.Written)
	}
	if names, _ := e.dest.Internal.Store().List(ctx); len(names) != 0 {
		t.Errorf("internal holds %v: nothing is written before a refusal", names)
	}
	if doc, _, _ := e.dest.External.OIDC("rp").Get(ctx); doc.ClientSecret != "OTHER-SECRET" {
		t.Error("the existing document was overwritten")
	}
}

func TestTwoV3SourcesForOneAddressMustAgree(t *testing.T) {
	e := newLayoutEnv(t)
	rec, _ := clientcreds.Record{Current: "OTHER-SECRET"}.Encode()
	if _, err := e.set.Secrets.Put(ctx, "credentials/oidc-client/rp/secret", rec); err != nil {
		t.Fatal(err)
	}
	rep, err := migrate.MoveSecretsLayout(ctx, e.options())
	if !errors.Is(err, migrate.ErrLayoutConflict) || rep.Written != 0 {
		t.Fatalf("move = %v written %d, want a refusal before any write", err, rep.Written)
	}
	noValues(t, "the refusal", err.Error())
}

func TestARotationWrittenHalfwayIsCompleted(t *testing.T) {
	e := newLayoutEnv(t)
	if _, err := e.dest.External.OIDC("gen").Put(ctx, secretstore.OIDCv1{ClientID: "gen", ClientSecret: "GENERATED-PREVIOUS"}, ""); err != nil {
		t.Fatal(err)
	}
	rep, err := migrate.MoveSecretsLayout(ctx, e.options())
	if err != nil || !rep.OK {
		t.Fatalf("move = %v\n%s", err, rep.JSON())
	}
	cur, prev, err := e.dest.External.OIDC("gen").Rotating(ctx, time.Hour)
	if err != nil || cur.ClientSecret != "GENERATED-CURRENT" || prev == nil || prev.ClientSecret != "GENERATED-PREVIOUS" {
		t.Errorf("gen = %+v, %+v, %v", cur, prev, err)
	}
}

func TestV3IsDeletedOnlyOnceTheInstallationIsOnV4(t *testing.T) {
	e := newLayoutEnv(t)
	o := e.options()
	if _, err := migrate.DeleteV3(ctx, o); !errors.Is(err, migrate.ErrNotV4) {
		t.Fatalf("delete on transition = %v, want ErrNotV4", err)
	}
	o.Layout = "v4"
	rep, err := migrate.DeleteV3(ctx, o)
	if !errors.Is(err, migrate.ErrV4Incomplete) {
		t.Fatalf("delete before the move = %v, want ErrV4Incomplete", err)
	}
	noValues(t, "the refusal", err.Error()+string(rep.JSON()))
	before, _ := e.set.Secrets.List(ctx, "")

	if _, err = migrate.MoveSecretsLayout(ctx, e.options()); err != nil {
		t.Fatal(err)
	}
	o.DryRun = true
	if rep, err = migrate.DeleteV3(ctx, o); err != nil {
		t.Fatalf("second delete = %v", err)
	} else if rep.Deleted != 0 {
		t.Fatalf("dry-run delete = %v deleted %d", err, rep.Deleted)
	}
	if after, _ := e.set.Secrets.List(ctx, ""); len(after) != len(before) {
		t.Fatalf("a dry run deleted %d paths", len(before)-len(after))
	}
	o.DryRun = false
	rep, err = migrate.DeleteV3(ctx, o)
	if err != nil || rep.Deleted == 0 {
		t.Fatalf("delete = %v\n%s", err, rep.JSON())
	}
	left, _ := e.set.Secrets.List(ctx, "")
	if len(left) != 1 || left[0] != "export/old-copy" {
		t.Errorf("v3 still holds %v, want only the exports copy", left)
	}
	// What the callers read is all still there.
	sec := secretstore.NewSecrets(e.dest, nil, 0)
	if _, err = sec.Get(ctx, "credentials/console/session-key"); err != nil {
		t.Errorf("session key after the delete: %v", err)
	}
	if _, _, err = e.dest.External.GitHubRunnerApp("stable", "acme").Get(ctx); err != nil {
		t.Errorf("runner App after the delete: %v", err)
	}
	// And a rerun has nothing to do.
	if rep, err = migrate.DeleteV3(ctx, o); err != nil {
		t.Fatalf("second delete = %v", err)
	} else if rep.Deleted != 0 {
		t.Errorf("second delete = %v deleted %d", err, rep.Deleted)
	}
}
