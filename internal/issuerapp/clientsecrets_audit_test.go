package issuerapp_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/secrets"
)

// inputDir is a file source with the input secret of each id in it.
func inputDir(t *testing.T, byID map[string]string) secrets.File {
	t.Helper()
	root := t.TempDir()
	for id, value := range byID {
		file := filepath.Join(root, filepath.FromSlash(secrets.ClientSecret(id)))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return secrets.File{Root: root}
}

func auditedBoot(t *testing.T, mem port.Secrets, input secrets.Source, policyBody string) (*issuerapp.App, *audittest.Recorder, error) {
	t.Helper()
	trail := audittest.New(t)
	stores := withSecrets(mem, "memory")
	stores.Secrets = input
	app, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: stores, Audit: trail}, replacePolicy(t, policyBody))
	return app, trail, err
}

func dataString(t *testing.T, trail *audittest.Recorder, action string, i int, key string) string {
	t.Helper()
	records := trail.Find(action)
	if len(records) <= i {
		t.Fatalf("no %s record #%d in %v", action, i, trail.Actions())
	}
	return records[i].GetData().GetFields()[key].GetStringValue()
}

func targetsOf(trail *audittest.Recorder, action string) []string {
	var out []string
	for _, r := range trail.Find(action) {
		for _, tg := range r.GetTargets() {
			out = append(out, tg.GetId())
		}
	}
	return out
}

func TestACreatedClientSecretIsAuditedOnce(t *testing.T) {
	t.Parallel()
	mem := memory.NewSecrets()
	app, trail, err := auditedBoot(t, mem, inputDir(t, nil), generatingPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetsOf(trail, "roster.client.secret.created"); len(got) != 1 || got[0] != "grafana" {
		t.Fatalf("created for %v; all: %v", got, trail.Actions())
	}
	if a := trail.Find("roster.client.secret.created")[0].GetActor(); a.GetKind() != "system" {
		t.Errorf("actor = %v", a)
	}
	if dataString(t, trail, "roster.client.secret.created", 0, "created") == "" {
		t.Error("no created time")
	}
	app.ReconcileClientSecrets(context.Background())
	app.ReconcileClientSecrets(context.Background())
	if got := trail.Actions(); len(got) != 1 {
		t.Errorf("later passes audited again: %v", got)
	}
	// No value in the trail.
	got, _ := mem.Get(context.Background(), clientcreds.Path("grafana"))
	rec, _ := clientcreds.DecodeRecord(got.Value)
	if strings.Contains(fmt.Sprint(trail.Records()), rec.Current) {
		t.Error("the secret is in the audit trail")
	}
}

func TestAnAdoptedInputSecretIsAuditedWithItsSource(t *testing.T) {
	t.Parallel()
	mem := memory.NewSecrets()
	_, trail, err := auditedBoot(t, mem, inputDir(t, map[string]string{"grafana": "the-secret-in-use"}), generatingPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetsOf(trail, "roster.client.secret.adopted"); len(got) != 1 || got[0] != "grafana" {
		t.Fatalf("adopted for %v; all: %v", got, trail.Actions())
	}
	if src := dataString(t, trail, "roster.client.secret.adopted", 0, "source"); src != "input" {
		t.Errorf("source = %q", src)
	}
	if len(trail.Find("roster.client.secret.created")) != 0 {
		t.Error("an adoption was also audited as a creation")
	}
	got, _ := mem.Get(context.Background(), clientcreds.Path("grafana"))
	if rec, _ := clientcreds.DecodeRecord(got.Value); rec.Current != "the-secret-in-use" {
		t.Errorf("stored %q", rec.Current)
	}
	if strings.Contains(fmt.Sprint(trail.Records()), "the-secret-in-use") {
		t.Error("the secret is in the audit trail")
	}
}

func TestAnOrphanIsAuditedOnceAndARestoredClientIsAuditedAsAdoptedFromTheRecord(t *testing.T) {
	t.Parallel()
	mem := memory.NewSecrets()
	// What an earlier run left: a record for a client that is not in the policy,
	// and the record of a client that is, marked orphaned while it was away.
	put := func(id string, rec clientcreds.Record) {
		body, err := rec.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = mem.Put(context.Background(), clientcreds.Path(id), body); err != nil {
			t.Fatal(err)
		}
	}
	away := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	put("old-client", clientcreds.Record{Current: "old-secret", Created: away})
	put("grafana", clientcreds.Record{Current: "kept-secret", Created: away, Orphaned: away.Add(time.Hour)})

	app, trail, err := auditedBoot(t, mem, inputDir(t, map[string]string{"grafana": "an-input-that-must-not-win"}), generatingPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetsOf(trail, "roster.client.secret.orphaned"); len(got) != 1 || got[0] != "old-client" {
		t.Fatalf("orphaned %v; all: %v", got, trail.Actions())
	}
	if got := targetsOf(trail, "roster.client.secret.adopted"); len(got) != 1 || got[0] != "grafana" {
		t.Fatalf("adopted %v; all: %v", got, trail.Actions())
	}
	if src := dataString(t, trail, "roster.client.secret.adopted", 0, "source"); src != "record" {
		t.Errorf("source = %q, want record", src)
	}
	if len(trail.Find("roster.client.secret.created")) != 0 {
		t.Errorf("created was audited: %v", trail.Actions())
	}
	if rec := func() clientcreds.Record {
		got, _ := mem.Get(context.Background(), clientcreds.Path("grafana"))
		r, _ := clientcreds.DecodeRecord(got.Value)
		return r
	}(); rec.Current != "kept-secret" || !rec.Orphaned.IsZero() {
		t.Errorf("restored record = %+v", rec)
	}

	// Not reported again, whoever asks.
	before := len(trail.Records())
	app.ReconcileClientSecrets(context.Background())
	app.ReconcileClientSecrets(context.Background())
	if got := len(trail.Records()); got != before {
		t.Errorf("later passes audited %d more: %v", got-before, trail.Actions())
	}
	if strings.Contains(fmt.Sprint(trail.Records()), "kept-secret") || strings.Contains(fmt.Sprint(trail.Records()), "old-secret") {
		t.Error("a secret is in the audit trail")
	}
}

// An installation that has dropped its last generated client still hears of the
// records it left.
func TestOrphansAreReportedWhenThereIsNoGeneratedClientAtAll(t *testing.T) {
	t.Parallel()
	mem := memory.NewSecrets()
	body, _ := clientcreds.Record{Current: "left-behind", Created: time.Now()}.Encode()
	if _, err := mem.Put(context.Background(), clientcreds.Path("old-client"), body); err != nil {
		t.Fatal(err)
	}
	const named = `
version: 1
groups:
  platform: { members: [platform@north.example] }
clients:
  argocd: { kind: confidential, secret: argocd-oidc, requires: [platform] }
`
	app, trail, err := auditedBoot(t, mem, inputDir(t, nil), named)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetsOf(trail, "roster.client.secret.orphaned"); len(got) != 1 || got[0] != "old-client" {
		t.Fatalf("orphaned %v; all: %v", got, trail.Actions())
	}
	if res := app.ReconcileClientSecrets(context.Background()); len(res.Outcomes) != 0 {
		t.Errorf("outcomes = %v", res.Outcomes)
	}
	if got := trail.Find("roster.client.secret.orphaned"); len(got) != 1 {
		t.Errorf("reported %d times", len(got))
	}
}
