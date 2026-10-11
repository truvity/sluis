package renderedcheck_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/audit"
	backupapp "github.com/truvity/sluis/internal/backup/app"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port/invoke"
	"github.com/truvity/sluis/internal/store"
)

var _ = invoke.KindGitHub // the issuer's plan names the invoke trigger; the binary links it

// Every document the Pulumi library renders (deploy/pulumi,
// TestEveryRenderedDocumentLoadsInItsRuntime) is loaded here by the loader of the
// function it is for, and the adapters it names are resolved the way the
// function resolves them: the adapter table is planned and the audit adapter
// applied to the audit configuration. A document a runtime refuses, an unknown
// settings field included, fails here. The library cannot import the runtime, so
// it renders to a directory of `<function>.<case>.yaml` files and runs this test
// over it. The package imports no frontend (the console is built separately), so
// the issuer's document is held to the service loader and its adapter plan, not
// to the issuer's whole assembly. Nothing is opened: no store is reached.
func TestEveryRenderedDocumentLoadsInItsRuntime(t *testing.T) {
	dir := os.Getenv("SLUIS_RENDERED_DOCS")
	if dir == "" {
		t.Skip("SLUIS_RENDERED_DOCS names the directory deploy/pulumi renders its documents to; it is set by that test")
	}
	// The clients connect to nothing at construction; these only stop the
	// default chain from looking for a profile.
	t.Setenv("AWS_ACCESS_KEY_ID", "k")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "s")
	t.Setenv("AWS_REGION", "eu-west-1")
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// An empty sweep proves nothing.
	if len(files) < 8 {
		t.Fatalf("%d documents in %s: the sweep is empty or short", len(files), dir)
	}
	seen := map[string]int{}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".yaml")
		fn, _, _ := strings.Cut(name, ".")
		seen[fn]++
		t.Run(name, func(t *testing.T) {
			switch fn {
			case "backup", "restore":
				checkBackup(t, file, name, fn)
			case "issuer":
				checkIssuer(t, file, name)
			default:
				t.Fatalf("no loader for the function %q", fn)
			}
		})
	}
	for _, fn := range []string{"issuer", "backup", "restore"} {
		if seen[fn] == 0 {
			t.Errorf("no %s document was rendered", fn)
		}
	}
}

// wantSQS is whether the document's case sends its audit records to a queue.
func wantSQS(name string) bool {
	return !strings.Contains(name, "audit-log") && !strings.Contains(name, "audit-off")
}

func checkAudit(t *testing.T, name string, got audit.Config) {
	t.Helper()
	if want := wantSQS(name); (got.SQS != nil) != want {
		t.Errorf("audit sends to a queue = %v, want %v: %+v", got.SQS != nil, want, got.SQS)
	}
}

func checkBackup(t *testing.T, file, name, fn string) {
	cfg, err := backupapp.Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Role() != fn {
		t.Fatalf("role = %q, want %q", cfg.Role(), fn)
	}
	got, err := cfg.AuditConfig(context.Background(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("the module cannot start on the document: %v", err)
	}
	checkAudit(t, name, got)
}

func checkIssuer(t *testing.T, file, name string) {
	svc, err := config.Load[config.Sluis](file)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := store.FromServe(&svc.Serve)
	if err != nil {
		t.Fatalf("FromServe: %v", err)
	}
	plan, err := cfg.Plan(context.Background(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("the adapters: %v", err)
	}
	got, err := audit.FromPlan(audit.Config{}, plan)
	if err != nil {
		t.Fatalf("the audit adapter: %v", err)
	}
	checkAudit(t, name, got)
}
