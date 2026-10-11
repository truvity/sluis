package audit_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/sdk/record"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/audit/audittest"
)

// exportedConstructors lists, from the source, every exported function of
// package audit that returns a *record.Record, and which of them take a
// Member. The list is read from the code, so a constructor added later is
// held to the guards below without anyone remembering to list it.
func exportedConstructors(t *testing.T) (all, takingMember []string) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range parsed.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			star, ok := fn.Type.Results.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Record" {
				continue
			}
			all = append(all, fn.Name.Name)
			for _, p := range fn.Type.Params.List {
				if id, ok := p.Type.(*ast.Ident); ok && id.Name == "Member" {
					takingMember = append(takingMember, fn.Name.Name)
					break
				}
			}
		}
	}
	sort.Strings(all)
	sort.Strings(takingMember)
	return all, takingMember
}

// calledIn returns the names called as audit.<Name>(...) in a test file.
func calledIn(t *testing.T, file string) map[string]bool {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	called := map[string]bool{}
	ast.Inspect(parsed, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "audit" {
					called[sel.Sel.Name] = true
				}
			}
		}
		return true
	})
	return called
}

// Every constructor that can build a record is exercised by every(), which the
// catalogue then validates. A constructor added without a line in every() is
// an action nobody has checked against the catalogue.
func TestEveryConstructorIsExercisedAgainstTheCatalogue(t *testing.T) {
	all, _ := exportedConstructors(t)
	if len(all) < 50 {
		t.Fatalf("found only %d constructors: the sweep is not looking at the code (%v)", len(all), all)
	}
	called := calledIn(t, "events_test.go")
	for _, name := range all {
		if !called[name] {
			t.Errorf("audit.%s builds a record and events_test.go every() never calls it", name)
		}
	}
}

// A constructor that takes a Member builds different targets for different
// Members: a team makes a "team" target, a login an account. The catalogue
// must declare every kind the code can produce for each action, so each such
// constructor is run over every shape of Member that the controller can pass.
func TestEveryMemberShapeIsDeclaredByTheCatalogueOfEveryMemberAction(t *testing.T) {
	_, takingMember := exportedConstructors(t)
	build := map[string]func(audit.Member) *record.Record{
		"GitHubMemberInvited": func(m audit.Member) *record.Record { return audit.GitHubMemberInvited(m, audit.Succeeded()) },
		"GitHubMemberAdded":   func(m audit.Member) *record.Record { return audit.GitHubMemberAdded(m, audit.Succeeded()) },
		"GitHubMemberRoleSet": func(m audit.Member) *record.Record { return audit.GitHubMemberRoleSet(m, audit.Succeeded()) },
		"GitHubMemberRemoved": func(m audit.Member) *record.Record { return audit.GitHubMemberRemoved(m, audit.Succeeded()) },
		"GitHubMemberHeld":    func(m audit.Member) *record.Record { return audit.GitHubMemberHeld(m, "remove", "held") },
		"GitHubOwnerReported": func(m audit.Member) *record.Record { return audit.GitHubOwnerReported(m, "remove", "reported") },
	}
	for _, name := range takingMember {
		if build[name] == nil {
			t.Errorf("audit.%s takes a Member and this test does not run it over the Member shapes: add it to build", name)
		}
	}
	for name, fn := range build {
		for _, team := range []string{"", "platform"} {
			for _, login := range []string{"", "aperson"} {
				t.Run(fmt.Sprintf("%s/team=%q/login=%q", name, team, login), func(t *testing.T) {
					rec := audittest.New(t)
					rec.Record(context.Background(), fn(audit.Member{
						Person: "a.person@example.com", Org: "example", Team: team, Login: login, Role: "member",
					}))
					if got := len(rec.Records()); got != 1 {
						t.Fatalf("the catalogue kept %d records, want 1", got)
					}
				})
			}
		}
	}
}
