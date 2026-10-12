package config_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/config"
	cfapp "github.com/truvity/sluis/internal/cloudflare/app"
	"github.com/truvity/sluis/internal/deploycheck"
	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	googleapp "github.com/truvity/sluis/internal/googleroster/app"
	"github.com/truvity/sluis/internal/lambdaapp/cloudflarefn"
	"github.com/truvity/sluis/internal/lambdaapp/githubfn"
	"github.com/truvity/sluis/internal/lambdaapp/googlefn"
	"github.com/truvity/sluis/internal/lambdaapp/issuerfn"
	"github.com/truvity/sluis/internal/lambdaapp/slackfn"
	_ "github.com/truvity/sluis/internal/port/invoke" // registers the adapter the preset names for the trigger
	"github.com/truvity/sluis/internal/rosterapp"
	slackapp "github.com/truvity/sluis/internal/slackroster/app"
)

// moduleCheck is what a module function's post-deploy check builds: Verify is
// the adapters half on its own (it passes or fails with no SSM to read), and
// Check is the whole {"kind":"check"} answer, which refuses on an adapter first.
type moduleCheck struct {
	verify func(ctx context.Context, file string) error
	check  func(file string) func(context.Context) (deploycheck.Report, error)
}

func verifier[C any](load func(string) (C, error), verify func(C, context.Context, *slog.Logger) error) func(context.Context, string) error {
	return func(ctx context.Context, file string) error {
		c, err := load(file)
		if err != nil {
			return fmt.Errorf("load: %w", err)
		}
		return verify(c, ctx, slog.New(slog.DiscardHandler))
	}
}

var moduleChecks = map[string]moduleCheck{
	config.ModuleGitHub:     {verifier(githubapp.Load, githubapp.Config.Verify), githubfn.CheckFile},
	config.ModuleSlack:      {verifier(slackapp.Load, slackapp.Config.Verify), slackfn.CheckFile},
	config.ModuleGoogle:     {verifier(googleapp.Load, googleapp.Config.Verify), googlefn.CheckFile},
	config.ModuleCloudflare: {verifier(cfapp.Load, cfapp.Config.Verify), cloudflarefn.CheckFile},
	config.ModuleIssuer:     {verifier(rosterapp.Load, rosterapp.Config.Verify), issuerfn.CheckFile},
}

// writeModule writes a module document, and the policy it names, into a
// directory of its own and returns the document's path.
func writeModule(t *testing.T, doc, policy []byte) string {
	t.Helper()
	dir := t.TempDir()
	pol := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(pol, policy, 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sluis.yaml")
	doc = []byte(strings.ReplaceAll(string(doc), "/opt/sluis/policy.yaml", pol))
	if err := os.WriteFile(file, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// withoutAudit is the document with `adapters.audit` taken out: the document the
// Google module had in v1.76.0-rc.3, whose function then refused every refresh
// tick with `audit adapter "sqs" needs settings.queueURL` while the check, which
// read only the declared secrets, passed.
func withoutAudit(t *testing.T, doc []byte) []byte {
	t.Helper()
	var generic map[string]any
	if err := yaml.Unmarshal(doc, &generic); err != nil {
		t.Fatal(err)
	}
	adapters, _ := generic["adapters"].(map[string]any)
	if _, ok := adapters["audit"]; !ok {
		t.Fatal("the document names no adapters.audit: the fixture does not test the case")
	}
	delete(adapters, "audit")
	out, err := yaml.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func documentsOf(docs *config.ModuleDocuments) map[string][]byte {
	out := map[string][]byte{config.ModuleIssuer: docs.Issuer}
	for m, doc := range docs.Modules {
		out[m] = doc
	}
	return out
}

// The post-deploy check of every module function builds every adapter the
// function builds, from the document the deploy rendered for it, and passes on
// a full document. This is the rendered fixtures of both installations.
func TestEveryRenderedModuleDocumentPassesItsAdapterCheck(t *testing.T) {
	for _, name := range moduleFixtures {
		_, docs := renderModules(t, name)
		for module, doc := range documentsOf(docs) {
			t.Run(name+"/"+module, func(t *testing.T) {
				c, ok := moduleChecks[module]
				if !ok {
					t.Fatalf("module %q has no check: add what its function builds", module)
				}
				if err := c.verify(t.Context(), writeModule(t, doc, docs.Policy)); err != nil {
					t.Errorf("the check refuses the document the deploy renders: %v", err)
				}
			})
		}
	}
}

// A document that lacks `adapters.audit` fails the check of every module, with
// the setting named, where the check of the declared secrets alone passed it (the
// Google module of v1.76.0-rc.3).
func TestTheCheckFailsOnADocumentWithoutTheAuditAdapter(t *testing.T) {
	for _, name := range moduleFixtures {
		_, docs := renderModules(t, name)
		for module, doc := range documentsOf(docs) {
			t.Run(name+"/"+module, func(t *testing.T) {
				c := moduleChecks[module]
				file := writeModule(t, withoutAudit(t, doc), docs.Policy)
				want := `audit adapter "sqs" needs settings.queueURL`
				if err := c.verify(t.Context(), file); err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("the adapters check: %v, want %q", err, want)
				}
				// And the function's own answer is that refusal, before any secret is read.
				if _, err := c.check(file)(t.Context()); err == nil || !strings.Contains(err.Error(), "adapters: ") ||
					!strings.Contains(err.Error(), want) {
					t.Errorf("the check event: %v, want an adapters refusal naming %q", err, want)
				}
			})
		}
	}
}
