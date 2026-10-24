package config_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/truvity/sluis/config"
	"github.com/truvity/sluis/internal/audit"
	internalconfig "github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	_ "github.com/truvity/sluis/internal/port/invoke" // registers the adapter the preset names for the trigger
	"github.com/truvity/sluis/internal/store"
)

// Every function builds the adapters its document names, at start and for a
// tick (the directory refresh builds the same). A module document that lacks
// the settings of one of them fails there, not at render time: this resolves
// the adapter table of each module's document the way the function does and
// holds each concern to the settings it needs.
func TestEveryModuleDocumentResolvesItsAdapters(t *testing.T) {
	for _, name := range moduleFixtures {
		t.Run(name, func(t *testing.T) {
			_, docs := renderModules(t, name)
			if len(docs.Modules) == 0 {
				t.Fatal("no module document")
			}
			for module, raw := range docs.Modules {
				t.Run(module, func(t *testing.T) {
					var cfg store.Config
					var auditCfg audit.Config
					switch module {
					case config.ModuleGitHub:
						doc := load[internalconfig.ControllerGitHub](t, raw)
						cfg = store.FromRoster(&doc.Roster)
						cfg.SetSecrets(doc.Secrets)
					case config.ModuleSlack:
						doc := load[internalconfig.ControllerSlack](t, raw)
						cfg = store.FromRoster(&doc.Roster)
						cfg.SetSecrets(doc.Secrets)
					case config.ModuleGoogle:
						file := filepath.Join(t.TempDir(), "google.yaml")
						if err := os.WriteFile(file, raw, 0o600); err != nil {
							t.Fatal(err)
						}
						doc, err := internalconfig.LoadGoogle(file)
						if err != nil {
							t.Fatal(err)
						}
						if cfg, err = store.FromServe(doc.Serve()); err != nil {
							t.Fatal(err)
						}
					case config.ModuleOutline:
						doc := load[internalconfig.SluisOutline](t, raw)
						cfg = store.FromRoster(&doc.Roster)
						cfg.SetSecrets(doc.Secrets)
					case config.ModuleCloudflare:
						doc := load[internalconfig.Sluis](t, raw)
						var err error
						if cfg, err = store.FromServe(&doc.Serve); err != nil {
							t.Fatal(err)
						}
					default:
						t.Fatalf("module %q has no case: add what its function builds", module)
					}
					table, err := cfg.As(port.Module(module)).Plan(context.Background(), slog.New(slog.DiscardHandler))
					if err != nil {
						t.Fatal(err)
					}
					for _, concern := range port.Concerns {
						if table[concern].Adapter == "" {
							t.Errorf("%s: no adapter resolved", concern)
						}
					}
					if _, err = audit.FromPlan(auditCfg, table); err != nil {
						t.Errorf("audit: %v", err)
					}
					if got := table[port.ConcernAudit]; got.Adapter != "sqs" {
						t.Errorf("audit adapter is %q, want sqs: the installation names the queue", got.Adapter)
					}
				})
			}
		})
	}
}
