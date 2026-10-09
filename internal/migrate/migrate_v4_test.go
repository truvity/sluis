package migrate_test

import (
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/store"
	statememory "github.com/truvity/sluis/storage/state/memory"
)

// A destination on layout v4: the Secrets port is the layout's and the stores
// are the installation's.
func v4Side(t *testing.T) (*store.Stores, *secretstore.Stores) {
	t.Helper()
	v4 := secretstore.FromStore(statememory.New(), "alias/example")
	set := memory.New().Set()
	set.Secrets = secretstore.NewSecrets(v4, 0)
	st := portSide(set, store.AdapterDynamoDB)
	st.V4 = v4
	return st, v4
}

const ringEntry = `{"first":"2026-09-01T00:00:00Z","wrapContext":{"purpose":"sign","instance":"old"}}`

func TestTheLegacyMoveWritesStraightIntoV4(t *testing.T) {
	src := newLegacy(t)
	seed(t, src.stores)
	seedLogins(t, src.stores)
	state := issuer.NewPortState(src.stores.Ports.State, src.stores.Ports.Index)
	if err := state.Set(ctx, "issuer:keyring:entry:ES384:kid2", []byte(ringEntry), 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	dst, v4 := v4Side(t)

	to := side("new", dst)
	opt := installation
	opt.ExportedGitHubApp = func(id string) bool { return id == "renovate" }
	report, err := migrate.Run(ctx, side("old", src.stores), to, opt)
	if err != nil || !report.OK {
		t.Fatalf("Run = %v\n%s", err, report.JSON())
	}
	noSecrets(t, report)

	// The Apps' keys are the external documents, with ids from the records.
	gh, _, err := v4.External.GitHubRunnerApp("stable", "acme").Get(ctx)
	if err != nil || gh.AppID != "3" || gh.InstallationID != "4" || gh.PrivateKey != "RUNNER-KEY" {
		t.Errorf("runner App = %+v, %v", gh, err)
	}
	if gh, _, err = v4.External.GitHubApp("renovate").Get(ctx); err != nil || gh.PrivateKey != "CAT-KEY" {
		t.Errorf("catalogue App = %+v, %v", gh, err)
	}
	if sl, _, err := v4.External.SlackApp("notifier").Get(ctx); err != nil || sl.BotToken != "xoxb-BOT" {
		t.Errorf("Slack App = %+v, %v", sl, err)
	}
	// Everything else is internal, and v3's tree does not exist.
	if names, err := dst.Ports.Secrets.List(ctx, "credentials/console"); err != nil || len(names) != 1 {
		t.Errorf("internal/credentials/console = %v, %v", names, err)
	}
	if names, err := dst.Ports.Secrets.List(ctx, "export"); err != nil || len(names) != 0 {
		t.Errorf("exports = %v, %v", names, err)
	}
	notes := strings.Join(report.Notes, "\n")

	// The ring is copied as it is: each entry keeps the context it was wrapped under.
	var got string
	err = dst.Ports.State.(port.StateExporter).ExportState(ctx, "issuer:keyring:entry:ES384:kid2", func(x port.Exported) error {
		got = string(x.Value)
		return nil
	})
	if err != nil || !strings.Contains(got, `"wrapContext"`) || !strings.Contains(got, `"instance":"old"`) {
		t.Errorf("the ring entry = %s, %v: its recorded wrap context is kept", got, err)
	}
	if !strings.Contains(notes, "wrapped under") {
		t.Errorf("the report does not say what happens to the ring: %q", notes)
	}
}

func TestADestinationWithoutARingStartsFresh(t *testing.T) {
	src := newLegacy(t)
	seed(t, src.stores)
	seedLogins(t, src.stores)
	dst, _ := v4Side(t)
	opt := installation
	opt.Skip = []string{migrate.DomainIssuer}
	report, err := migrate.Run(ctx, side("old", src.stores), side("new", dst), opt)
	if err != nil || !report.OK {
		t.Fatalf("Run = %v\n%s", err, report.JSON())
	}
	var n int
	err = dst.Ports.State.(port.StateExporter).ExportState(ctx, "issuer:keyring:", func(port.Exported) error { n++; return nil })
	if err != nil || n != 0 {
		t.Errorf("the destination holds %d ring entries (%v), want none: the issuer starts a ring of its own", n, err)
	}
}
