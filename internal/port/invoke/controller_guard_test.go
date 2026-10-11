package invoke_test

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

// repoRoot is the module's root: the nearest parent with a go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}

// walkGo parses every non-test Go file of the module, except generated trees.
func walkGo(t *testing.T, visit func(path string, fset *token.FileSet, f *ast.File)) {
	t.Helper()
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "gen", "dist", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		visit(rel, fset, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestEveryEventNamesItsController fails when any constructor of a tick or run
// event leaves Controller out: a name that is both a GitHub organisation and a
// Slack workspace is refused by the function when the event does not say which.
func TestEveryEventNamesItsController(t *testing.T) {
	found := 0
	walkGo(t, func(path string, fset *token.FileSet, f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isEventType(lit.Type, f.Name.Name == "invoke") {
				return true
			}
			found++
			for _, el := range lit.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Controller" {
						return true
					}
				}
			}
			t.Errorf("%s: an invoke.Event literal sets no Controller", fset.Position(lit.Pos()))
			return true
		})
	})
	if found == 0 {
		t.Fatal("no invoke.Event literal found: the guard checks nothing")
	}
}

func isEventType(e ast.Expr, inPackage bool) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && id.Name == "invoke" && x.Sel.Name == "Event"
	case *ast.Ident:
		return inPackage && x.Name == "Event"
	}
	return false
}

// TestNoControllerlessNotify fails when a sender outside the trigger
// adapters calls Trigger.Notify, which cannot say the controller: senders use
// port.NotifyController.
func TestNoControllerlessNotify(t *testing.T) {
	allowed := []string{"internal/port/", "internal/lambdaapp/"}
	walkGo(t, func(path string, fset *token.FileSet, f *ast.File) {
		for _, a := range allowed {
			if strings.HasPrefix(filepath.ToSlash(path), a) {
				return
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Notify" && len(call.Args) == 2 {
				t.Errorf("%s: Notify names no controller; use port.NotifyController", fset.Position(call.Pos()))
			}
			return true
		})
	})
}
