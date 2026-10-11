package rosterapp_test

import (
	"context"
	"log/slog"
	"testing"

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
		app, err := rosterapp.New(context.Background(), load(t, doc), slog.New(slog.DiscardHandler))
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
