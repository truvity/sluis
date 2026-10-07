package access_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agent-consent acceptance is minted in exactly one place: the consent
// page, after authentication (docs/decisions/0040-agent-class-sessions.md,
// decision 6). Held mechanically: across the module's code (tests mint
// their own, and are left out), IssueAgentConsent has one call site, in
// internal/issuer/consent.go, and the purpose an acceptance carries
// (AgentConsentPurpose, or its value) appears nowhere but
// internal/access/consent.go, so no other code can issue a state with it.
// A second site is a second door that mints an acceptance, which is what
// this test exists to stop without a review.
func TestAnAgentConsentIsMintedInOnePlace(t *testing.T) {
	root := moduleRoot(t)

	var sites, purposes []string

	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".devbox", "dist", "testdata", "frontend", "ts":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanned++

		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				if selector, ok := node.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "IssueAgentConsent" {
					sites = append(sites, rel)
				}
			case *ast.Ident:
				// The purpose, by name: a state issued with it elsewhere is
				// an acceptance minted by another door.
				if node.Name == "AgentConsentPurpose" && rel != "internal/access/consent.go" {
					purposes = append(purposes, rel)
				}
			case *ast.BasicLit:
				// And by value, so that the name cannot be sidestepped.
				if node.Kind == token.STRING && strings.Trim(node.Value, "`\"") == "agent-consent" && rel != "internal/access/consent.go" {
					purposes = append(purposes, rel)
				}
			}
			return true
		})

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A sweep that read nothing proves nothing.
	if scanned < 100 {
		t.Fatalf("scanned %d Go files under %s, want the whole module", scanned, root)
	}

	if len(sites) != 1 || sites[0] != "internal/issuer/consent.go" {
		t.Errorf("IssueAgentConsent is called at %v, want exactly once, in internal/issuer/consent.go", sites)
	}

	if len(purposes) != 0 {
		t.Errorf("the agent-consent purpose is used at %v, want only in internal/access/consent.go", purposes)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	for {
		if _, err = os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}

		dir = parent
	}
}
