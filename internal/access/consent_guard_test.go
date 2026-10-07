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
// internal/issuer/consent.go. A second one is a second door that mints an
// acceptance, which is what this test exists to stop without a review.
func TestAnAgentConsentIsMintedInOnePlace(t *testing.T) {
	root := moduleRoot(t)

	var sites []string

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

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "IssueAgentConsent" {
				rel, _ := filepath.Rel(root, path)
				sites = append(sites, filepath.ToSlash(rel))
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
