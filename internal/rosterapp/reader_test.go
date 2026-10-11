package rosterapp_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/rosterapp"
)

const readerDoc = `
apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: https://access.example
ports: {adapter: memory}
`

// `google.remote` makes the issuer's hub a reader of the Google module's
// snapshots; without it the hub owns its directory.
func TestGoogleRemoteMakesTheIssuersHubAReader(t *testing.T) {
	t.Parallel()
	if cfg := load(t, readerDoc); cfg.Directory.Reader {
		t.Error("a local install, with no google.remote, reads snapshots only")
	}
	remote := readerDoc + "google: {remote: {function: sluis-test-google}}\n"
	if cfg := load(t, remote); !cfg.Directory.Reader {
		t.Error("google.remote left the issuer's hub refreshing")
	}
	// A google section with no remote is the module in this process.
	if cfg := load(t, readerDoc+"google: {}\n"); cfg.Directory.Reader {
		t.Error("google with no remote made the hub a reader")
	}
}

// A reader adopts no workspace and reads no key at start: the declared
// directory is the Google module's, and the issuer's role could not write its
// records. The same document without the remote has the issuer adopt, which
// needs the key.
func TestAReaderIssuerStartsWithoutAdoptingADeclaredWorkspace(t *testing.T) {
	t.Parallel()
	declared := readerDoc + `
directory:
  workspaces:
    - {id: acme.example, backend: google, admin: admin@acme.example, keySecret: workspaces/acme/key, serve: [acme.example]}
`
	start := func(doc string) error {
		cfg := load(t, doc)
		// The module's function is not called at start; there is no Lambda here.
		cfg.Dial = func(context.Context, modcall.Config, string) (modcall.Caller, error) { return modcall.Local{}, nil }
		app, err := rosterapp.New(context.Background(), cfg, slog.New(slog.DiscardHandler))
		if err == nil {
			app.Close()
		}
		return err
	}
	if err := start(declared); err == nil {
		t.Fatal("the issuer adopted a declared workspace whose key does not exist")
	}
	if err := start(declared + "google: {remote: {function: sluis-test-google}}\n"); err != nil {
		t.Fatalf("a reader issuer did not start: %v", err)
	}
}

// With the GitHub, Slack and Google modules and the Cloudflare minter all
// another function's, the issuer starts over a fake invoker alone: it calls
// each as the class it needs (github and google as the issuer for the mint and
// the sign-in, every module as the console for its pages) and opens none of the
// providers' stores (v1.76 E-FIN).
func TestAnIssuerWithEveryProviderRemoteStartsOverAFakeInvoker(t *testing.T) {
	t.Parallel()
	cfg := load(t, readerDoc+`
github: {remote: {function: sluis-test-github}}
slack: {remote: {function: sluis-test-slack}}
google: {remote: {function: sluis-test-google}}
`)
	if !cfg.Directory.GitHubRemote || !cfg.Directory.SlackRemote || !cfg.Directory.Reader {
		t.Fatalf("the directory config does not know the modules are remote: %+v", cfg.Directory)
	}
	var dialled []string
	cfg.Dial = func(_ context.Context, c modcall.Config, class string) (modcall.Caller, error) {
		for module := range c.Modules {
			dialled = append(dialled, module+"/"+class)
		}
		return modcall.Local{}, nil
	}
	app, err := rosterapp.New(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("an issuer with every provider remote did not start: %v", err)
	}
	app.Close()
	slices.Sort(dialled)
	for _, want := range []string{"github/issuer", "github/console", "slack/console", "google/issuer", "google/console"} {
		if !slices.Contains(dialled, want) {
			t.Errorf("no %s call was dialled: %v", want, dialled)
		}
	}
	for _, not := range []string{"slack/issuer"} {
		if slices.Contains(dialled, not) {
			t.Errorf("dialled %s: %v", not, dialled)
		}
	}
}
