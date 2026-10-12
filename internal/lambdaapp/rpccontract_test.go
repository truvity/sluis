package lambdaapp_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/truvity/sluis/gen/directoryroster/v1/directoryrosterv1connect"
	backuprpc "github.com/truvity/sluis/internal/backup/rpc"
	cloudflarerpc "github.com/truvity/sluis/internal/cloudflare/rpc"
	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
	ghroster "github.com/truvity/sluis/internal/githubroster/app"
	"github.com/truvity/sluis/internal/lambdaapp/googlefn"
	"github.com/truvity/sluis/internal/lambdaapp/slackfn"
	"github.com/truvity/sluis/internal/modcall"

	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
)

// forwarded are the methods the issuer's console (and the issuer, for the mint,
// the sign-in and the grants) calls on a module, read from the constants the
// module's package declares for them: a name starting `Method`, or a value
// starting `console.` (the GitHub module's wire package names its methods by
// what they do). A method added to such a file is held to being registered
// without anyone adding it to this test.
func forwarded(t *testing.T, files ...string) []string {
	t.Helper()
	var out []string
	for _, file := range files {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					lit, ok := vs.Values[min(i, len(vs.Values)-1)].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(name.Name, "Method") || strings.HasPrefix(v, "console.") {
						out = append(out, v)
					}
				}
			}
		}
	}
	slices.Sort(out)
	if len(out) == 0 {
		t.Fatalf("no method constants found in %v: the table is empty and would pass anything", files)
	}
	return out
}

func registered(s *modcall.Server) []string {
	var out []string
	for _, m := range s.Methods() {
		out = append(out, m.Name)
	}
	return out
}

// TestEveryMethodTheIssuerCallsOnAModuleIsRegisteredByItsFunction holds each
// module's server, as its function assembles it, to answering every method the
// module's wire declares. A console procedure forwarded to a module that did
// not register it reaches the person as a failure of the module with nothing
// in the module's log.
func TestEveryMethodTheIssuerCallsOnAModuleIsRegisteredByItsFunction(t *testing.T) {
	noService := func(context.Context) (*googleconsole.Service, error) { return nil, errors.New("not opened") }
	noSlack := func(context.Context) (*slackconsole.Service, error) { return nil, errors.New("not opened") }

	// The GitHub function serves the mint and the console on one server
	// (Minter.ServeConsole): the same two calls, over no stores.
	github := ghroster.MintServer(nil, nil)
	ghconsole.Register(github, ghconsole.New(ghconsole.Deps{}))
	// The Cloudflare module's server is `serve` of internal/cloudflare/app.
	cloudflare := modcall.NewServer(cloudflarerpc.Module)
	cloudflarerpc.Register(cloudflare, nil)
	cloudflarerpc.RegisterConsole(cloudflare, nil)
	backup := modcall.NewServer(backuprpc.Module)
	backuprpc.Register(backup, nil)

	for _, tc := range []struct {
		module string
		server *modcall.Server
		files  []string
	}{
		{"google", googlefn.Server(slog.Default(), noService), []string{"../googleapp/console/wire.go"}},
		{"slack", slackfn.Server(slog.Default(), noSlack), []string{"../slackapp/console/register.go"}},
		{"github", github, []string{"../githubroster/consolewire/consolewire.go", "../githubapp/githubtokens/rpc.go"}},
		{"cloudflare", cloudflare, []string{"../cloudflare/rpc/rpc.go"}},
		{"backup", backup, []string{"../backup/rpc/rpc.go"}},
	} {
		t.Run(tc.module, func(t *testing.T) {
			have := registered(tc.server)
			for _, method := range forwarded(t, tc.files...) {
				if !slices.Contains(have, method) {
					t.Errorf("the %s function does not register %q, which the issuer calls; it registers %v", tc.module, method, have)
				}
			}
		})
	}
}

// consoleGitHubAnsweredHere are the GitHubService methods the issuer's console
// answers from what it holds itself when the GitHub module is remote, each with
// the reason it is on purpose. A method not here must reach the module.
var consoleGitHubAnsweredHere = map[string]string{
	"ListGitHubAppTokens": "the ring of recent requests is written by the issuer, which decides each mint; " +
		"the module keeps none, and is asked only which App it is and whether the person may operate it",
}

// reachesModule reports whether a function of the console package, by the
// functions and methods it calls in that package, ends in a call to the GitHub
// module.
func reachesModule(decls map[string][]*ast.FuncDecl, name string, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	for _, decl := range decls[name] {
		reached := false
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || reached {
				return !reached
			}
			switch id.Name {
			case "githubModule", "ProxyJSON", "Proxy":
				reached = true
			default:
				reached = reachesModule(decls, id.Name, seen)
			}
			return !reached
		})
		if reached {
			return true
		}
	}
	return false
}

// TestEveryGitHubConsoleMethodIsForwardedOrAnsweredHereOnPurpose holds the
// issuer's console, for a deployment whose GitHub module is another function,
// to having no GitHub method that fails for want of the records only the
// module opens: each GitHubService method reaches the module, or is named in
// [consoleGitHubAnsweredHere] with a reason. The people search narrows by the
// GitHub links, which are the module's too, so it is held to reaching the
// module for them.
func TestEveryGitHubConsoleMethodIsForwardedOrAnsweredHereOnPurpose(t *testing.T) {
	files, err := filepath.Glob("../server/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no console sources found: %v", err)
	}
	decls := map[string][]*ast.FuncDecl{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				decls[fn.Name.Name] = append(decls[fn.Name.Name], fn)
			}
		}
	}

	handler := reflect.TypeOf((*directoryrosterv1connect.GitHubServiceHandler)(nil)).Elem()
	if handler.NumMethod() == 0 {
		t.Fatal("GitHubServiceHandler has no methods: the check would pass anything")
	}
	declared := map[string]bool{}
	for i := range handler.NumMethod() {
		method := handler.Method(i).Name
		declared[method] = true
		forwarded := reachesModule(decls, method, map[string]bool{})
		reason, here := consoleGitHubAnsweredHere[method]
		switch {
		case !forwarded && !here:
			t.Errorf("the console serves GitHubService.%s without reaching the GitHub module, and says nowhere that it should not: "+
				"with the module remote it can only fail for want of records it does not open", method)
		case here && reason == "":
			t.Errorf("GitHubService.%s is answered here without a reason", method)
		}
	}
	for method := range consoleGitHubAnsweredHere {
		if !declared[method] {
			t.Errorf("%s is listed as answered here but is no GitHubService method", method)
		}
	}
	if !reachesModule(decls, "githubLogins", map[string]bool{}) {
		t.Error("the people search's GitHub filter does not reach the GitHub module: its links are the module's when it is remote")
	}
}
