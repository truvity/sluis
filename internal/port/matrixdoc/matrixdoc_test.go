package matrixdoc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/port"
)

// TestTheMatrixIsNotStale fails when docs/reference/adapters.md is not what
// the registry says. Fix it with `just adapters-doc`.
func TestTheMatrixIsNotStale(t *testing.T) {
	want := Render()
	got, err := os.ReadFile(filepath.Join("..", "..", "..", File))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s is stale: run `just adapters-doc` and commit the result", File)
	}
}

// registrants returns the import paths of every non-test package under
// internal/ that registers an adapter: a call to port.Register (or
// Register inside package port itself).
func registrants(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	var found []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		registers := false
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if x, ok := fn.X.(*ast.Ident); ok && x.Name == "port" && fn.Sel.Name == "Register" {
					registers = true
				}
			case *ast.Ident:
				if f.Name.Name == "port" && fn.Name == "Register" {
					registers = true
				}
			}
			return true
		})
		if registers {
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			found = append(found, "github.com/truvity/sluis/"+filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(found)
	return slices.Compact(found)
}

func deps(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	out, err := exec.Command("go", "list", "-e", "-deps", "-f", "{{.ImportPath}}", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	set := map[string]bool{}
	for _, l := range strings.Fields(string(out)) {
		set[l] = true
	}
	return set
}

// TestEveryRegisteringPackageIsInTheMatrix fails when a package that
// registers an adapter is not linked into the generator. The generator sees
// only what its own binary imports, so an adapter that only another binary
// imports (invoke, only in cmd/sluis-lambda) was documented as planned.
func TestEveryRegisteringPackageIsInTheMatrix(t *testing.T) {
	regs := registrants(t)
	if len(regs) < 5 {
		t.Fatalf("found only %v: the scan is not seeing the adapters", regs)
	}
	have := deps(t, "github.com/truvity/sluis/internal/port/matrixdoc")
	for _, r := range regs {
		if !have[r] {
			t.Errorf("%s registers an adapter and matrixdoc does not import it: add a blank import to matrixdoc.go, or the matrix says the adapter is planned", r)
		}
	}
}

// TestEveryBinaryAdapterIsInTheMatrix is the same check from the other side:
// whatever a cmd/* binary links must be linked into the generator too.
func TestEveryBinaryAdapterIsInTheMatrix(t *testing.T) {
	regs := registrants(t)
	have := deps(t, "github.com/truvity/sluis/internal/port/matrixdoc")
	cmds, err := filepath.Glob(filepath.Join("..", "..", "..", "cmd", "*"))
	if err != nil || len(cmds) == 0 {
		t.Fatalf("no cmd/*: %v", err)
	}
	for _, c := range cmds {
		for pkg := range deps(t, "github.com/truvity/sluis/cmd/"+filepath.Base(c)) {
			if slices.Contains(regs, pkg) && !have[pkg] {
				t.Errorf("cmd/%s links %s, which matrixdoc does not", filepath.Base(c), pkg)
			}
		}
	}
}

// TestPresetsNameOnlyImplementedAdapters holds every preset, the deprecated
// alias included, to the registry as every binary sees it: a preset names
// only implemented adapters, or is marked unavailable and refused at load. A
// preset in neither state, or marked while it has nothing missing, fails.
func TestPresetsNameOnlyImplementedAdapters(t *testing.T) {
	for _, p := range append(slices.Clone(port.Presets), port.PresetAWSEKS) {
		var missing []string
		for _, c := range port.Concerns {
			name := port.PresetTable(p)[c]
			if d, ok := port.Default.Lookup(c, name); !ok || d.Status != port.StatusImplemented {
				missing = append(missing, string(c)+"="+name)
			}
		}
		why := p.Unavailable()
		switch {
		case len(missing) > 0 && why == "":
			t.Errorf("preset %s names adapters that are not built (%v) and is not marked unavailable in internal/port/resolve.go", p, missing)
		case len(missing) == 0 && why != "":
			t.Errorf("preset %s is marked unavailable (%s) but every adapter it names is built: remove the mark", p, why)
		}
		_, err := port.Resolve(port.Selection{Preset: p})
		if (err != nil) != (why != "") {
			t.Errorf("preset %s: unavailable=%q but Resolve returned %v", p, why, err)
		}
		if err != nil && !strings.Contains(err.Error(), "unavailable") {
			t.Errorf("preset %s: the refusal does not say the preset is unavailable: %v", p, err)
		}
	}
}
