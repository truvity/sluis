package lambdaapp_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"

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
