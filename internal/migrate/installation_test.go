package migrate_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/store"
)

// installation is the options of an installation's move: the writers are
// stopped, and the sessions are not copied.
var installation = migrate.Options{WritersStopped: true}

func TestAnInstallationMovesTheKeyRingAndNotTheSessions(t *testing.T) {
	src := newLegacy(t)
	seed(t, src.stores)
	seedLogins(t, src.stores)
	state := issuer.NewPortState(src.stores.Ports.State, src.stores.Ports.Index)
	if err := state.Set(ctx, "issuer:kms:state-secret-fingerprint", []byte(`"abc"`), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	dst := portSide(memory.New().Set(), store.AdapterDynamoDB)

	report, err := migrate.Run(ctx, side("old", src.stores), side("new", dst), installation)
	if err != nil || !report.OK {
		t.Fatalf("Run = %v\n%s", err, report.JSON())
	}
	if s := step(t, report, "issuer", "keyring"); s.Source != 1 || s.Copied != 1 || s.Verified != 1 {
		t.Errorf("the key ring = %+v, want its one entry copied", s)
	}
	for _, s := range report.Steps {
		if s.Domain == "issuer" && s.Kind != "keyring" {
			t.Errorf("an installation's move copied the issuer's %s", s.Kind)
		}
	}
	for _, c := range []struct {
		prefix string
		want   int
	}{{"issuer:keyring:", 1}, {"issuer:code:", 0}, {"issuer:session", 0}, {"issuer:kms:", 0}} {
		var n int
		err = dst.Ports.State.(port.StateExporter).ExportState(ctx, c.prefix, func(port.Exported) error { n++; return nil })
		if err != nil || n != c.want {
			t.Errorf("the destination holds %d records under %s (%v), want %d", n, c.prefix, err, c.want)
		}
	}
	// The credentials went to the Secrets port under credentials/<kind>/<id>/<ref>, and State
	// holds only the records that name them.
	paths, err := dst.Ports.Secrets.List(ctx, "credentials")
	if err != nil || len(paths) < 6 {
		t.Fatalf("the destination's Secrets hold %v (%v), want the credentials under credentials/", paths, err)
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "credentials/") {
			t.Errorf("secret %s is not under credentials/", p)
		}
	}
	got, err := dst.Ports.Secrets.Get(ctx, paths[0])
	if err != nil || len(got.Value) == 0 {
		t.Errorf("secret %s = %v", paths[0], err)
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "people sign in again") {
		t.Errorf("the report does not say the sessions are left behind: %v", report.Notes)
	}
	if len(report.Concerns) != 3 || report.Concerns[0].Items == 0 || report.Concerns[1].Items == 0 {
		t.Errorf("concerns = %+v, want state, secrets and blobs counted", report.Concerns)
	}
}

func TestADryRunRefusesWhatTheDestinationWouldRefuseAndWritesNothing(t *testing.T) {
	src := newLegacy(t)
	seed(t, src.stores)
	d, err := migrate.OpenDomains(ctx, src.stores, true)
	if err != nil {
		t.Fatal(err)
	}
	// A credential over MaxSecret, and a record over MaxValue.
	big := make([]byte, port.MaxSecret+1)
	if err = d.Workspaces.Put(ctx, hub.Workspace{ID: "C-BIG", Backend: "google", ConnectedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = d.Credentials.Save(ctx, "C-BIG", backend.Credential{Type: "service-account-key", Data: big}); err != nil {
		t.Fatal(err)
	}
	if err = d.Workspaces.Put(ctx, hub.Workspace{ID: "C-HUGE", Backend: "google", Admin: strings.Repeat("a", port.MaxValue+1), ConnectedAt: now}); err != nil {
		t.Fatal(err)
	}
	dst := portSide(memory.New().Set(), store.AdapterDynamoDB)

	for _, opt := range []migrate.Options{
		{DryRun: true},
		{WritersStopped: true},
		{WritersStopped: true, Overwrite: true},
	} {
		report, err := migrate.Run(ctx, side("old", src.stores), side("new", dst), opt)
		if !errors.Is(err, migrate.ErrRefused) || report == nil || report.OK {
			t.Fatalf("Run(%+v) = %v, want ErrRefused", opt, err)
		}
		if len(report.Refused) != 2 || report.Totals.Copied != 0 {
			t.Fatalf("refused = %+v, copied %d: want the two oversize items named and nothing written", report.Refused, report.Totals.Copied)
		}
		var secretRefused, stateRefused bool
		for _, p := range report.Refused {
			secretRefused = secretRefused || (p.Key == "C-BIG" && strings.Contains(p.Reason, "Secrets limit"))
			stateRefused = stateRefused || (p.Key == "C-HUGE" && strings.Contains(p.Reason, "State limit"))
		}
		if !secretRefused || !stateRefused {
			t.Errorf("refused = %+v", report.Refused)
		}
		if c := report.Concerns; c[0].Refused != 1 || c[1].Refused != 1 {
			t.Errorf("concerns = %+v, want one refusal each under state and secrets", c)
		}
		for _, prefix := range []string{"ws.", "gh.", "app.", "rec."} {
			if n := countState(t, dst, prefix); n != 0 {
				t.Errorf("a refused run wrote %d records under %s", n, prefix)
			}
		}
	}
}

func TestAnInstallationRerunIsIdempotentAndDoesNotOverwriteANewerRecord(t *testing.T) {
	src := newLegacy(t)
	seed(t, src.stores)
	seedLogins(t, src.stores)
	dst := portSide(memory.New().Set(), store.AdapterDynamoDB)

	first, err := migrate.Run(ctx, side("old", src.stores), side("new", dst), installation)
	if err != nil || first.Totals.Copied == 0 {
		t.Fatalf("first = %v\n%s", err, first.JSON())
	}
	again, err := migrate.Run(ctx, side("old", src.stores), side("new", dst), installation)
	if err != nil || again.Totals.Copied != 0 || again.Totals.Present != again.Totals.Source {
		t.Fatalf("a re-run = %v\n%s", err, again.JSON())
	}

	// The destination moved on (a newer record the new installation wrote):
	// the run refuses, naming it, and writes nothing, unless it is told to.
	dd, err := migrate.OpenDomains(ctx, dst, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = dd.Workspaces.Put(ctx, hub.Workspace{ID: "C02", Backend: "google", Domains: []string{"newer.example"}, ConnectedAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rep, err := migrate.Run(ctx, side("old", src.stores), side("new", dst), installation)
	if !errors.Is(err, migrate.ErrConflict) || rep.Totals.Copied != 0 {
		t.Fatalf("a run over a newer record = %v, copied %d", err, rep.Totals.Copied)
	}
	if _, err = migrate.Run(ctx, side("old", src.stores), side("new", dst), migrate.Options{WritersStopped: true, Overwrite: true}); err != nil {
		t.Fatalf("--overwrite = %v", err)
	}
}

// A copy is written with the lifetime the source has LEFT when it is written,
// not the one it had when it was read: it must not outlive the source by the
// time the run took.
func TestACopyIsWrittenWithTheLifetimeLeftAtWriteTime(t *testing.T) {
	src := newLegacy(t)
	seed(t, src.stores)
	seedLogins(t, src.stores)
	dst := portSide(memory.New().Set(), store.AdapterDynamoDB)
	start := time.Now()
	calls := 0
	opt := migrate.Options{WritersStopped: true, Sessions: true, Now: func() time.Time {
		calls++
		if calls == 1 {
			return start
		}
		return start.Add(4 * time.Minute) // the run took four minutes between the read and the writes
	}}
	report, err := migrate.Run(ctx, side("old", src.stores), side("new", dst), opt)
	if err != nil || !report.OK {
		t.Fatalf("Run = %v\n%s", err, report.JSON())
	}
	var found bool
	err = dst.Ports.State.(port.StateExporter).ExportState(ctx, "issuer:code:", func(x port.Exported) error {
		found = true
		if x.TTL > time.Minute+5*time.Second {
			t.Errorf("the code was written with %s left, the source's five minutes less the four the run took", x.TTL)
		}
		return nil
	})
	if err != nil || !found {
		t.Fatalf("the code is not on the destination (%v)", err)
	}
}
