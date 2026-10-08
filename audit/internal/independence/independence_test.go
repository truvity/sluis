// Package independence holds audit to the rule that lets it live inside the
// sluis repository and still be used without it: audit never imports sluis.
package independence

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	sluis = "github.com/truvity/sluis"
	audit = "github.com/truvity/sluis/audit"
)

// ours reports whether an import path is audit's own: the audit module, the
// SDK and the Pulumi library all live under it.
func ours(path string) bool { return path == audit || strings.HasPrefix(path, audit+"/") }

// theirs reports whether an import path is sluis's, which audit must not use.
func theirs(path string) bool {
	return (path == sluis || strings.HasPrefix(path, sluis+"/")) && !ours(path)
}

// root is the audit directory, two levels above this package.
func root(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNoGoFileImportsSluis(t *testing.T) {
	base := root(t)
	files, imports := 0, 0
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".devbox", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		for _, spec := range f.Imports {
			p, _ := strconv.Unquote(spec.Path.Value)
			imports++
			if theirs(p) {
				rel, _ := filepath.Rel(base, path)
				t.Errorf("%s imports %s: audit never imports sluis", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A walk that found nothing proves nothing.
	if files < 100 || imports < 500 {
		t.Fatalf("only %d Go files and %d imports were read; the walk found nothing", files, imports)
	}
}

func TestNoModuleRequiresSluis(t *testing.T) {
	base := root(t)
	mods := 0
	for _, dir := range []string{".", "sdk", "deploy/pulumi"} {
		raw, err := os.ReadFile(filepath.Join(base, dir, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		mods++
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
			if len(fields) > 0 && theirs(fields[0]) {
				t.Errorf("%s/go.mod names %s: audit never depends on sluis", dir, fields[0])
			}
		}
	}
	if mods != 3 {
		t.Fatalf("read %d go.mod files, want 3", mods)
	}
}
