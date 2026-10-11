package rosterapp_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"testing"

	backuprpc "github.com/truvity/sluis/internal/backup/rpc"
	cfrpc "github.com/truvity/sluis/internal/cloudflare/rpc"
	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
	githubroster "github.com/truvity/sluis/internal/githubroster/app"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	"github.com/truvity/sluis/internal/modcall"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
)

// registered is every module's server as the functions assemble it, over
// services that are never called: only what each method allows is read.
func registered() map[string]*modcall.Server {
	github := githubroster.MintServer(nil, &http.Client{})
	ghconsole.Register(github, ghconsole.New(ghconsole.Deps{}))
	cloudflare := modcall.NewServer("cloudflare")
	cfrpc.Register(cloudflare, nil)
	cfrpc.RegisterConsole(cloudflare, nil)
	backup := modcall.NewServer("backup")
	backuprpc.Register(backup, nil)
	restore := modcall.NewServer("restore")
	backuprpc.RegisterRestore(restore, nil)
	return map[string]*modcall.Server{
		"github":     github,
		"slack":      slackconsole.NewServer(slackconsole.Static(slackconsole.Deps{})),
		"google":     googleconsole.NewService(nil, nil, nil, slog.New(slog.DiscardHandler)).Server(),
		"cloudflare": cloudflare,
		"backup":     backup,
		"restore":    restore,
	}
}

// Every method of the four provider modules answers the console or the issuer,
// as designed: a console method is not the issuer's to call and the reverse,
// since on Lambda the two classes are only two aliases of one role and the
// separation is this check (v1.76 E-IAM). The one method both may call is the
// Cloudflare grants listing. A method with no Allow would answer any caller.
func TestEveryProviderMethodAnswersTheConsoleOrTheIssuerAsDesigned(t *testing.T) {
	both := map[string]bool{"cloudflare.granted": true}
	var console, issuer int
	for module, srv := range registered() {
		if module == "backup" || module == "restore" {
			continue
		}
		for _, m := range srv.Methods() {
			name := module + "." + m.Name
			t.Run(name, func(t *testing.T) {
				allow := slices.Clone(m.Allow)
				switch {
				case both[name]:
					if !slices.Equal(allow, []string{"console", "issuer"}) {
						t.Fatalf("Allow = %v, want console and issuer", allow)
					}
					return
				case slices.Equal(allow, []string{"console"}):
					console++
				case slices.Equal(allow, []string{"issuer"}):
					issuer++
				default:
					t.Fatalf("Allow = %v, want [console] or [issuer]", allow)
				}
				// And the transport refuses the other class, before any handler.
				other := "issuer"
				if allow[0] == "issuer" {
					other = "console"
				}
				_, err := modcall.Local{module: srv}.As(other).Call(context.Background(), module, m.Name, []byte(`{}`))
				var e *modcall.Error
				if !errors.As(err, &e) || e.Code != modcall.CodeForbidden {
					t.Errorf("called as %s: %v, want forbidden", other, err)
				}
				// Nor does a caller with no class pass.
				_, err = modcall.Local{module: srv}.Call(context.Background(), module, m.Name, []byte(`{}`))
				if !errors.As(err, &e) || e.Code != modcall.CodeForbidden {
					t.Errorf("called with no class: %v, want forbidden", err)
				}
			})
		}
	}
	if console < 20 || issuer < 4 {
		t.Errorf("only %d console and %d issuer methods were found: the sweep is not reaching the modules", console, issuer)
	}
}

// The backup and restore methods are the operators' and the console's, never
// the issuer's, and none answers any caller.
func TestNoBackupMethodAnswersTheIssuerOrAnyCaller(t *testing.T) {
	for module, srv := range registered() {
		if module != "backup" && module != "restore" {
			continue
		}
		for _, m := range srv.Methods() {
			if len(m.Allow) == 0 || slices.Contains(m.Allow, "issuer") {
				t.Errorf("%s.%s: Allow = %v", module, m.Name, m.Allow)
			}
		}
	}
}
