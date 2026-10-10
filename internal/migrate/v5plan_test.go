package migrate_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	sluissecrets "github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/secretstore/secretrec"
	"github.com/truvity/sluis/internal/store"
)

var update = flag.Bool("update", false, "rewrite the golden plan")

// planSecrets are the values the plan fixtures hold beyond [secrets]: a plan
// must never contain one.
var planSecrets = []string{
	"STATE-SECRET-VALUE", "RECOVERY-PW", "GOOGLE-CLIENT-ID", "GOOGLE-CLIENT-SECRET", "DIRECTORY-KEY", "CLIENT-INPUT",
	"CF-MINTER-TOKEN", "CF-PRESET-TOKEN", "AKIDEXAMPLE", "S3-SECRET", "ROSTER-KEY",
}

// v4Installation is a full layout v4 installation: the stores of a deployment on
// the single table and the old parameters, recorded.
type v4Installation struct {
	st  *store.Stores
	mem *memory.Store
	v4  *secretstore.Stores
	rec *secretrec.Store
	env map[string]string
}

func newV4Installation(t *testing.T) *v4Installation {
	t.Helper()
	rec := secretrec.New()
	v4 := secretstore.FromStore(rec, "alias/example")
	mem := memory.New()
	set := mem.Set()
	set.Secrets = secretstore.NewSecrets(v4, 0)
	st := portSide(set, store.AdapterDynamoDB)
	st.V4 = v4
	env := map[string]string{
		sluissecrets.EnvName("issuer/state-secret"):                 "STATE-SECRET-VALUE",
		sluissecrets.EnvName("recovery/password"):                   "RECOVERY-PW",
		sluissecrets.EnvName("providers/google/main/client-id"):     "GOOGLE-CLIENT-ID",
		sluissecrets.EnvName("providers/google/main/client-secret"): "GOOGLE-CLIENT-SECRET",
		sluissecrets.EnvName("directory/C01/key"):                   "DIRECTORY-KEY",
		sluissecrets.EnvName("clients/local-dev/secret"):            "CLIENT-INPUT",
	}
	st.Secrets = sluissecrets.Env{Getenv: func(k string) string { return env[k] }}
	return &v4Installation{st: st, mem: mem, v4: v4, rec: rec, env: env}
}

// seedFull fills the installation with one of everything: every domain, the
// operator's secrets, the Cloudflare module's, the blob credentials, the
// issuer's logins and a report.
func (v *v4Installation) seedFull(t *testing.T) {
	t.Helper()
	seed(t, v.st)
	seedLogins(t, v.st)
	d, err := migrate.OpenDomains(ctx, v.st, true)
	if err != nil {
		t.Fatal(err)
	}
	// The App the organisation acme is installed by.
	if err = d.Catalogue.Put(ctx, catalogueapp.Record{
		ID: "roster", Org: "acme", AppID: 7, AppSlug: "acme-roster", InstallationID: 9, ConnectedAt: now, ConnectedBy: "ada@acme.example",
	}, "ROSTER-KEY"); err != nil {
		t.Fatal(err)
	}
	state := issuer.NewPortState(v.st.Ports.State, v.st.Ports.Index)
	for key, value := range map[string]string{
		"issuer:keyring:entry:ES384:kid2":        ringEntry,
		"issuer:keyring:retired:ES384:old":       `{"retired":"2026-08-01T00:00:00Z"}`,
		"issuer:kms:state-secret-fingerprint":    `"abc"`,
		"issuer:session-token:" + "0123456789ab": `"live"`,
	} {
		if err = state.Set(ctx, key, []byte(value), 30*24*time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if err = v.st.Ports.Index.Add(ctx, "issuer:keyring:index:ES384", "kid2", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err = v.st.Ports.Blob.Write(ctx, "reports/github/acme", []byte(`{"report":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = v.st.Ports.Blob.Write(ctx, "reports/slack/acme", []byte(`{"report":2}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = v.st.Ports.Blob.Write(ctx, "google/C01", []byte(`a cache`)); err != nil {
		t.Fatal(err)
	}
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(v.v4.External.OIDC("local-dev").Put(ctx, secretstore.OIDCv1{ClientID: "local-dev", ClientSecret: "CLIENT-INPUT"}, ""))
	minter, err := v.v4.Internal.CloudflareMinter("internal/cloudflare/prod/minter")
	if err != nil {
		t.Fatal(err)
	}
	must(minter.Put(ctx, secretstore.CloudflareMinterv1{Token: "CF-MINTER-TOKEN"}, ""))
	must(v.v4.Internal.CloudflareMinted("r2").Put(ctx, secretstore.CloudflareMinted{
		Tokens: []secretstore.CloudflareMintedToken{{ID: "tok1", ExpiresOn: now}},
	}, ""))
	must(v.v4.External.Cloudflare("r2").Put(ctx, secretstore.Cloudflarev1{Token: "CF-PRESET-TOKEN", ExpiresOn: now.Format(time.RFC3339)}, ""))
	s3, err := v.v4.Internal.S3Credentials("internal/blobs/r2")
	if err != nil {
		t.Fatal(err)
	}
	must(s3.Put(ctx, secretstore.S3Credentialsv1{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "S3-SECRET"}, ""))
}

// v5Installation is an empty installation on layout v5.
type v5Installation struct {
	st  *store.Stores
	v5  *secretstore.StoresV5
	rec *secretrec.Store
	mem *memory.Store
}

func newV5Installation(t *testing.T) *v5Installation {
	t.Helper()
	rec := secretrec.New()
	v5 := secretstore.FromStoreV5(rec, "alias/example")
	mem := memory.New()
	set := mem.Set()
	set.Secrets = nil
	st := portSide(set, store.AdapterDynamoDB)
	st.V5 = v5
	return &v5Installation{st: st, v5: v5, rec: rec, mem: mem}
}

func planOptions() migrate.PlanOptions {
	return migrate.PlanOptions{
		Sessions:           true,
		ExportedGitHubApp:  func(id string) bool { return id == "renovate" },
		AppRef:             func(org string) string { return map[string]string{"acme": "roster"}[org] },
		ConfigNames:        []string{"providers/google/main/client-id", "providers/google/main/client-secret", "directory/C01/key"},
		CloudflareAccounts: []string{"prod"},
		S3Ref:              "internal/blobs/r2",
		S3RefTo:            "internal/google/blobs-r2",
	}
}

func noPlanSecrets(t *testing.T, raw []byte) {
	t.Helper()
	for _, s := range append(slices.Clone(secrets), planSecrets...) {
		if bytes.Contains(raw, []byte(s)) {
			t.Errorf("the plan contains %q", s)
		}
	}
}

func TestPlanOfAFullV4Installation(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)

	report, err := migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions())
	if err != nil {
		t.Fatalf("Plan = %v\n%s", err, report.JSON())
	}
	raw := report.JSON()
	noPlanSecrets(t, raw)

	const golden = "testdata/v5-plan.json"
	if *update {
		if err = os.WriteFile(golden, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Errorf("the plan differs from %s (go test -run TestPlanOfAFullV4Installation -update rewrites it):\n%s", golden, raw)
	}
	// Nothing is on the destination, so nothing is the same or refused.
	if c := report.Totals; c.Same != 0 || c.Different != 0 || c.Refused != 0 || c.New == 0 {
		t.Errorf("totals = %+v", c)
	}
}

func TestPlanSeesWhatTheDestinationAlreadyHolds(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	// A first pass by hand: one secret the same, one different.
	if _, err := dst.v5.OIDC().StateSecret().Put(ctx, []byte("STATE-SECRET-VALUE"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.v5.OIDC().RecoveryPassword().Put(ctx, []byte("ANOTHER"), ""); err != nil {
		t.Fatal(err)
	}
	// And the records written through the destination's own stores.
	d, err := migrate.OpenDomainsExporting(ctx, dst.st, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws, cred := firstWorkspace(t, src)
	if err = d.Workspaces.Put(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if err = d.Credentials.Save(ctx, ws.ID, cred); err != nil {
		t.Fatal(err)
	}

	report, err := migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions())
	if err != nil {
		t.Fatal(err)
	}
	find := func(module, concern, kind string) migrate.PlanItem {
		t.Helper()
		for _, m := range report.Modules {
			for _, it := range m.Items {
				if m.Module == module && it.Concern == concern && it.Kind == kind {
					return it
				}
			}
		}
		t.Fatalf("no %s %s/%s row", module, concern, kind)
		return migrate.PlanItem{}
	}
	if it := find("oidc", "secret", "state-secret"); it.Status != migrate.PlanSame || it.ToVersion == "" {
		t.Errorf("state secret = %+v, want same with its version", it)
	}
	if it := find("oidc", "secret", "recovery-password"); it.Status != migrate.PlanDifferent {
		t.Errorf("recovery password = %+v, want different", it)
	}
	// The secret's v4 side has no version of its own; the destination's does.
	if it := find("google", "state", "workspace"); it.Status != migrate.PlanSame {
		t.Errorf("workspace = %+v, want same: written through the destination's own store", it)
	}
	noPlanSecrets(t, report.JSON())
}

func firstWorkspace(t *testing.T, src *v4Installation) (hub.Workspace, backend.Credential) {
	t.Helper()
	d, err := migrate.OpenDomains(ctx, src.st, false)
	if err != nil {
		t.Fatal(err)
	}
	all, err := d.Workspaces.List(ctx)
	if err != nil || len(all) == 0 {
		t.Fatalf("workspaces = %v, %v", all, err)
	}
	cred, found, err := d.Credentials.Load(ctx, all[0].ID)
	if err != nil || !found {
		t.Fatalf("credential = %v, %v", found, err)
	}
	return all[0], cred
}

func TestPlanRefusesWhatLayoutV5CannotCarry(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	opt := planOptions()
	// No App for acme, and a catalogue id the runner Apps' prefix owns.
	opt.AppRef = func(string) string { return "" }
	report, err := migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if !errors.Is(err, migrate.ErrRefused) || report.OK {
		t.Fatalf("Plan = %v, want ErrRefused", err)
	}
	var org migrate.PlanItem
	for _, m := range report.Modules {
		for _, it := range m.Items {
			if it.Kind == "org" {
				org = it
			}
		}
	}
	if org.Status != migrate.PlanRefused || !strings.Contains(org.Reason, "appRefs") {
		t.Errorf("organisation = %+v", org)
	}
	// An App whose id does not match the installation is refused as well.
	opt.AppRef = func(string) string { return "renovate" }
	report, err = migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if !errors.Is(err, migrate.ErrRefused) || !strings.Contains(report.Error, "would not sign") {
		t.Errorf("Plan with a wrong App = %v", err)
	}
}

func TestPlanReportsAnUnreadableItem(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	if _, err := src.mem.Put(ctx, "rec.slack.shared.broken", []byte("{not json"), 0); err != nil {
		t.Fatal(err)
	}
	report, err := migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions())
	if !errors.Is(err, migrate.ErrUnreadable) || report.OK {
		t.Fatalf("Plan = %v, want ErrUnreadable", err)
	}
	var got []migrate.PlanItem
	for _, m := range report.Modules {
		for _, it := range m.Items {
			if it.Status == migrate.PlanRefused {
				got = append(got, it)
			}
		}
	}
	if len(got) != 1 || got[0].ID != "broken" || !strings.HasPrefix(got[0].Reason, "unreadable") || got[0].From != "rec.slack.shared.broken" {
		t.Errorf("refused = %+v, want the one broken record", got)
	}
	// The rest is planned.
	if report.Totals.New == 0 {
		t.Errorf("totals = %+v", report.Totals)
	}
}

// tally counts every write that reaches a port.
type tally struct {
	mu sync.Mutex
	n  map[string]int
}

func (t *tally) hit(op string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n == nil {
		t.n = map[string]int{}
	}
	t.n[op]++
}

func (t *tally) total() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	var n int
	for _, c := range t.n {
		n += c
	}
	return n
}

type cState struct {
	port.State
	t *tally
}

func (c cState) Put(ctx context.Context, k string, v []byte, ttl time.Duration) (port.Revision, error) {
	c.t.hit("state.put")
	return c.State.Put(ctx, k, v, ttl)
}

func (c cState) Create(ctx context.Context, k string, v []byte, ttl time.Duration) (port.Revision, error) {
	c.t.hit("state.create")
	return c.State.Create(ctx, k, v, ttl)
}

func (c cState) Update(ctx context.Context, k string, v []byte, ttl time.Duration, rev port.Revision) (port.Revision, error) {
	c.t.hit("state.update")
	return c.State.Update(ctx, k, v, ttl, rev)
}

func (c cState) Delete(ctx context.Context, k string) error {
	c.t.hit("state.delete")
	return c.State.Delete(ctx, k)
}

func (c cState) DeleteIfRevision(ctx context.Context, k string, rev port.Revision) error {
	c.t.hit("state.delete")
	return c.State.DeleteIfRevision(ctx, k, rev)
}

func (c cState) ExportState(ctx context.Context, prefix string, fn func(port.Exported) error) error {
	return c.State.(port.StateExporter).ExportState(ctx, prefix, fn)
}

type cIndex struct {
	port.Index
	t *tally
}

func (c cIndex) Add(ctx context.Context, k, m string, ttl time.Duration) error {
	c.t.hit("index.add")
	return c.Index.Add(ctx, k, m, ttl)
}

func (c cIndex) Remove(ctx context.Context, k, m string) error {
	c.t.hit("index.remove")
	return c.Index.Remove(ctx, k, m)
}

func (c cIndex) ExportIndex(ctx context.Context, prefix string, fn func(port.Exported) error) error {
	return c.Index.(port.IndexExporter).ExportIndex(ctx, prefix, fn)
}

type cBlob struct {
	port.Blob
	t *tally
}

func (c cBlob) Write(ctx context.Context, n string, b []byte) (string, error) {
	c.t.hit("blob.write")
	return c.Blob.Write(ctx, n, b)
}

func (c cBlob) WriteIfVersion(ctx context.Context, n string, b []byte, v string) (string, error) {
	c.t.hit("blob.write")
	return c.Blob.WriteIfVersion(ctx, n, b, v)
}

func (c cBlob) Delete(ctx context.Context, n string) error {
	c.t.hit("blob.delete")
	return c.Blob.Delete(ctx, n)
}

type cSecrets struct {
	port.Secrets
	t *tally
}

func (c cSecrets) Put(ctx context.Context, p string, v []byte) (string, error) {
	c.t.hit("secrets.put")
	return c.Secrets.Put(ctx, p, v)
}

func (c cSecrets) PutIfVersion(ctx context.Context, p string, v []byte, ver string) (string, error) {
	c.t.hit("secrets.put")
	return c.Secrets.PutIfVersion(ctx, p, v, ver)
}

func (c cSecrets) Delete(ctx context.Context, p string) error {
	c.t.hit("secrets.delete")
	return c.Secrets.Delete(ctx, p)
}

// counted is st with every write port wrapped to count.
func counted(st *store.Stores, t *tally) *store.Stores {
	c := *st
	c.Ports.State = cState{st.Ports.State, t}
	c.Ports.Index = cIndex{st.Ports.Index, t}
	c.Ports.Blob = cBlob{st.Ports.Blob, t}
	if st.Ports.Secrets != nil {
		c.Ports.Secrets = cSecrets{st.Ports.Secrets, t}
	}
	return &c
}

func TestPlanWritesNothing(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	var writes tally
	from, to := counted(src.st, &writes), counted(dst.st, &writes)
	src.rec.Reset()
	dst.rec.Reset()

	report, err := migrate.Plan(ctx, side("v4.yaml", from), side("v5.yaml", to), planOptions())
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.New == 0 {
		t.Fatalf("the plan found nothing to do: %+v", report.Totals)
	}
	if n := writes.total(); n != 0 {
		t.Errorf("the plan wrote through the ports: %v", writes.n)
	}
	for name, rec := range map[string]*secretrec.Store{"source": src.rec, "destination": dst.rec} {
		for _, op := range []string{"put", "delete"} {
			if got := rec.Addresses(op); len(got) != 0 {
				t.Errorf("the plan's %s %s %v", name, op, got)
			}
		}
	}
	// The counting is not vacuous: the plan did read the parameters.
	if len(src.rec.Addresses("get")) == 0 || len(dst.rec.Addresses("get")) == 0 {
		t.Errorf("reads: source %v, destination %v", src.rec.Addresses("get"), dst.rec.Addresses("get"))
	}
}

func TestPlanNeedsALayoutV4SourceAndALayoutV5Destination(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	if _, err := migrate.Plan(ctx, side("a", dst.st), side("b", src.st), planOptions()); err == nil {
		t.Error("a plan from layout v5 to layout v4 was accepted")
	}
	if _, err := migrate.Plan(ctx, side("a", src.st), side("b", src.st), planOptions()); err == nil {
		t.Error("a plan to layout v4 was accepted")
	}
	opt := planOptions()
	opt.Skip = []string{"nothing"}
	if _, err := migrate.Plan(ctx, side("a", src.st), side("b", dst.st), opt); err == nil {
		t.Error("an unknown --skip was accepted")
	}
}

func TestPlanWithoutSessionsPlansTheRingOnly(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	opt := planOptions()
	opt.Sessions = false
	opt.Skip = []string{migrate.DomainBlobs}
	report, err := migrate.Plan(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range report.Modules {
		for _, it := range m.Items {
			if it.Concern == "blob" {
				t.Errorf("a blob was planned with --skip blobs: %+v", it)
			}
			if m.Module == "oidc" && it.Concern == "state" && it.Kind == "session-token" {
				t.Errorf("a session was planned without sessions: %+v", it)
			}
		}
	}
}
