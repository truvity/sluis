package blobprefix_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/truvity/sluis/blobprefix"
	"github.com/truvity/sluis/internal/backup/export"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/port"
)

// Every prefix is below its module's root: the root is what the module's
// function role is granted.
func TestEveryPrefixIsBelowItsModulesRoot(t *testing.T) {
	t.Parallel()
	for module, prefixes := range blobprefix.Used() {
		for _, p := range prefixes {
			if !strings.HasPrefix(p, blobprefix.Root(module)) || !strings.HasSuffix(p, "/") {
				t.Errorf("%q is not a prefix below %q", p, blobprefix.Root(module))
			}
		}
	}
}

// What the code uses is what this package names: the backup export and the
// hub's cache take their prefixes from here.
func TestTheCodeUsesThePrefixesOfThisPackage(t *testing.T) {
	t.Parallel()
	used := blobprefix.Used()
	for module, prefixes := range export.BlobPrefixes {
		for _, p := range prefixes {
			if !slices.Contains(used[string(module)], p) {
				t.Errorf("the backup export keeps %q of %s, which is not one of blobprefix.Used", p, module)
			}
		}
	}
	if !slices.Contains(used[string(port.ModuleGoogle)], hub.SnapshotBlobPrefix) {
		t.Errorf("the hub's cache %q is not one of blobprefix.Used", hub.SnapshotBlobPrefix)
	}
}

// No Go file but this package writes a blob prefix as a literal: a prefix
// spelled out in place is the way the reports came to sit at the root of the
// bucket, outside every module's grant. The layout v4 names are
// blobprefix.Legacy*, for the migration alone.
func TestNoBlobPrefixIsSpelledOutOutsideThisPackage(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{"reports/", "snapshots/", "github/reports", "slack/reports", "google/cache"}
	fset := token.NewFileSet()
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "blobprefix"):
			return filepath.SkipDir
		case d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil // not this test's business
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, b := range banned {
				if strings.HasPrefix(v, b) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s: the literal %q is a blob prefix; use the constant of package blobprefix", fset.Position(lit.Pos()).String()+" ("+rel+")", v)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Fatalf("only %d Go files were checked: the walk is not reaching the tree", checked)
	}
}
