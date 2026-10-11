package issuer_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A description is what a caller reads. Built from anything but a constant,
// it is a way for a reason -- an error's text, a client id, a group, an email
// -- to reach a caller, which is the one thing a refusal must not do. So the
// package may not set one from a variable: WithDescription takes constants,
// and the one place an error gets the words a caller reads is the refusal
// helper (refusal.go), which gives fixed words and the request id and puts the
// reason in the error's parent and the log.
//
// This is a lint and not a convention because the leak it prevents is
// invisible in review: `WithDescription("%s", err)` reads as diligence.

// refusalHelperFile is the one file allowed to set an error's description
// from a value, and to build an oidc.Error by hand.
const refusalHelperFile = "refusal.go"

func TestNoDynamicErrorDescriptions(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, v := range descriptionViolations(t, file, string(source)) {
			t.Errorf("%s: %s\n\tSet no description from a value: call deny, malformed or fault with the reason, "+
				"which logs it under the request id and gives the caller fixed words.", file, v)
		}
	}
	if checked < 20 {
		t.Fatalf("checked %d files; the lint is looking in the wrong place", checked)
	}
}

// The lint has to fail on what it exists to catch, or it passes by looking
// at nothing.
func TestTheDescriptionLintCatchesWhatItIsFor(t *testing.T) {
	t.Parallel()
	const header = "package issuer\nconst fixed = \"fixed\"\n"
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"an error's text":         {`func f(err error) { _ = oidc.ErrInvalidGrant().WithDescription("%s", err) }`, 1},
		"a format with arguments": {`func f(id string) { _ = oidc.ErrInvalidClient().WithDescription("unknown client: %q", id) }`, 1},
		"a variable format":       {`func f(s string) { _ = oidc.ErrInvalidGrant().WithDescription(s) }`, 1},
		"a built string":          {`func f(s string) { _ = oidc.ErrInvalidGrant().WithDescription("subject_token: " + s) }`, 1},
		"a hand-built error":      {`func f() { _ = &oidc.Error{ErrorType: oidc.InvalidTarget, Description: "x"} }`, 1},
		"an assigned description": {`func f(e *oidc.Error, s string) { e.Description = s }`, 1},
		"a constant":              {`func f() { _ = oidc.ErrInvalidGrant().WithDescription("fixed words") }`, 0},
		"a constant by name":      {`func f() { _ = oidc.ErrInvalidGrant().WithDescription(fixed) }`, 0},
		"a constant joined":       {`func f() { _ = oidc.ErrInvalidGrant().WithDescription("fixed " + "words") }`, 0},
		"a metric's description":  {`func f(m metric.Meter) { _ = metric.WithDescription("Sign-ins, by " + x) }`, 0},
	} {
		got := descriptionViolations(t, "snippet.go", header+tc.body)
		if len(got) != tc.want {
			t.Errorf("%s: %d violations %v, want %d", name, len(got), got, tc.want)
		}
	}
}

// descriptionViolations are the places in a file that set an error's
// description from something that is not a constant.
func descriptionViolations(t *testing.T, name, source string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	helper := filepath.Base(name) == refusalHelperFile

	constants := map[string]bool{}
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
			for _, spec := range gen.Specs {
				for _, id := range spec.(*ast.ValueSpec).Names {
					constants[id.Name] = true
				}
			}
		}
	}
	var constant func(ast.Expr) bool
	constant = func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.BasicLit:
			return e.Kind == token.STRING
		case *ast.ParenExpr:
			return constant(e.X)
		case *ast.BinaryExpr:
			return e.Op == token.ADD && constant(e.X) && constant(e.Y)
		case *ast.Ident:
			return constants[e.Name]
		}
		return false
	}

	var out []string
	at := func(n ast.Node) string { return fset.Position(n.Pos()).String() }
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			selector, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "WithDescription" {
				return true
			}
			// OpenTelemetry's option of the same name describes a metric.
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "metric" {
				return true
			}
			for _, arg := range n.Args {
				if !constant(arg) {
					out = append(out, at(n)+": WithDescription is given a value that is not a constant")
					break
				}
			}
		case *ast.CompositeLit:
			if helper {
				return true
			}
			if selector, ok := n.Type.(*ast.SelectorExpr); ok && selector.Sel.Name == "Error" {
				if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "oidc" {
					out = append(out, at(n)+": an oidc.Error is built by hand, with a description of its own")
				}
			}
		case *ast.AssignStmt:
			if helper {
				return true
			}
			for _, lhs := range n.Lhs {
				if selector, ok := lhs.(*ast.SelectorExpr); ok && selector.Sel.Name == "Description" {
					out = append(out, at(n)+": a Description is assigned")
				}
			}
		}
		return true
	})
	return out
}
