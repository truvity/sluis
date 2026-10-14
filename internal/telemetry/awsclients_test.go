package telemetry_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every AWS SDK client in the repository is built with awsotel.Providers: the
// SDK takes its tracer and meter per client, so one built without is silent.
// This reads the Go source of every module and fails on a NewFromConfig of an
// AWS service whose options do not name it. Test helpers that talk to a local
// emulator are exempt; they are not the service's clients.
func TestEveryAWSClientUsesAwsotel(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skip("not inside the repository")
	}
	exempt := filepath.ToSlash(filepath.Join("audit", "internal", "s3test")) + "/"
	skipDir := map[string]bool{".git": true, "node_modules": true, ".devbox": true, "deploy": true}
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(rel, exempt) ||
			strings.HasPrefix(rel, "storage/awsotel/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		aws := map[string]bool{} // local names of AWS service packages
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(p, "github.com/aws/aws-sdk-go-v2/service/") {
				continue
			}
			name := p[strings.LastIndex(p, "/")+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}
			aws[name] = true
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "NewFromConfig" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || !aws[pkg.Name] {
				return true
			}
			checked++
			text := src[fset.Position(call.Pos()).Offset:fset.Position(call.End()).Offset]
			if !bytes.Contains(text, []byte("awsotel.Providers()")) {
				t.Errorf("%s: NewFromConfig does not set the tracer and meter provider from awsotel.Providers()", fset.Position(call.Pos()))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 10 {
		t.Fatalf("only %d NewFromConfig calls were found: the scan is not reading the repository", checked)
	}
}
