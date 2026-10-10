package migrate_test

import (
	"bytes"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
)

// fakeClock is the time the stores and the planner count lifetimes by.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// exportAll is every live issuer record of a side, by key.
func exportAll(t *testing.T, ports port.Set) map[string]port.Exported {
	t.Helper()
	out := map[string]port.Exported{}
	ex, ok := ports.State.(port.StateExporter)
	if !ok {
		t.Fatalf("%T cannot export", ports.State)
	}
	if err := ex.ExportState(ctx, "issuer:", func(x port.Exported) error { out[x.Key] = x; return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func exportSets(t *testing.T, ports port.Set) map[string]port.Exported {
	t.Helper()
	out := map[string]port.Exported{}
	if err := ports.Index.(port.IndexExporter).ExportIndex(ctx, "issuer:", func(x port.Exported) error { out[x.Key] = x; return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCopyCarriesTheIssuerByteForByte(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	// A refresh token spent before the copy, and a set of whose sessions are whose.
	for key, ttl := range map[string]time.Duration{
		"issuer:session-rotated:spent-one": 12 * time.Hour,
		"issuer:request:req-1":             7 * time.Minute,
		"issuer:sso:browser-1":             24 * time.Hour,
	} {
		if _, err := src.st.Ports.State.Put(ctx, key, []byte(`{"v":"`+key+`"}`), ttl); err != nil {
			t.Fatal(err)
		}
	}
	if err := src.st.Ports.Index.Add(ctx, "issuer:sessions-of:ada@north.example", "sess-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	before := exportAll(t, src.st.Ports)
	beforeSets := exportSets(t, src.st.Ports)

	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), copyOptions())
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	noPlanSecrets(t, report.JSON())
	if report.Verify == nil || !report.Verify.OK || report.Totals.Expired != 0 {
		t.Errorf("verify = %+v, expired = %d", report.Verify, report.Totals.Expired)
	}

	after := exportAll(t, dst.st.Ports)
	for _, kind := range []string{"keyring:entry:", "keyring:retired:", "kms:", "session-token:", "session-rotated:", "code:", "request:", "sso:"} {
		var n int
		for key, x := range before {
			if !strings.HasPrefix(key, "issuer:"+kind) {
				continue
			}
			n++
			got, ok := after[key]
			if !ok {
				t.Errorf("the destination lacks a %s record", kind)
				continue
			}
			if !bytes.Equal(got.Value, x.Value) {
				t.Errorf("a %s record differs on the destination", kind)
			}
		}
		if n == 0 {
			t.Errorf("the fixture holds no %s record", kind)
		}
	}
	if got := after["issuer:keyring:entry:ES384:kid2"]; string(got.Value) != ringEntry {
		t.Errorf("the ring entry is not byte-identical")
	}
	if len(after) != len(before) {
		t.Errorf("the destination holds %d issuer records, the source %d", len(after), len(before))
	}
	afterSets := exportSets(t, dst.st.Ports)
	for key, x := range beforeSets {
		if got, ok := afterSets[key]; !ok || !slices.Equal(got.Members, x.Members) {
			t.Errorf("the Index set %s is not on the destination as it was", key)
		}
	}

	// The source is untouched.
	if got := exportAll(t, src.st.Ports); len(got) != len(before) {
		t.Errorf("the source holds %d issuer records after the copy, %d before", len(got), len(before))
	}
}

// steppedClock returns a planner clock that moves the shared clock by step at its
// second reading, which is the first write: the records were read before it.
func steppedClock(c *fakeClock, step time.Duration) (now func() time.Time, readings *int) {
	n := new(int)
	return func() time.Time {
		*n++
		if *n == 2 {
			c.Advance(step)
		}
		return c.Now()
	}, n
}

func clockedSides(t *testing.T, c *fakeClock) (*v4Installation, *v5Installation) {
	t.Helper()
	return newV4Installation(t, memory.WithClock(c.Now)), newV5Installation(t, memory.WithClock(c.Now))
}

func TestCopyKeepsTheRemainingLifetime(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}
	src, dst := clockedSides(t, clock)
	seeded := map[string]time.Duration{
		"issuer:code:one-time-code":            10 * time.Minute,
		"issuer:keyring:entry:ES384:kid1":      30 * 24 * time.Hour,
		"issuer:session-token:0123456789ab":    12 * time.Hour,
		"issuer:session-rotated:spent-refresh": 90 * time.Minute,
	}
	for key, ttl := range seeded {
		if _, err := src.st.Ports.State.Put(ctx, key, []byte(`"x"`), ttl); err != nil {
			t.Fatal(err)
		}
	}
	if err := src.st.Ports.Index.Add(ctx, "issuer:sessions-of:ada", "s1", 2*time.Hour); err != nil {
		t.Fatal(err)
	}

	const gap = 3 * time.Minute
	opt := copyOptions()
	opt.Skip = []string{migrate.DomainBlobs}
	opt.Now, _ = steppedClock(clock, gap)
	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}

	// Three minutes passed between the read and the write: each record has what
	// it had left then, which is what the source says it has now.
	got := exportAll(t, dst.st.Ports)
	want := exportAll(t, src.st.Ports)
	for key, ttl := range seeded {
		d, ok := got[key]
		if !ok {
			t.Fatalf("%s did not arrive", key)
		}
		if diff := (ttl - gap) - d.TTL; diff < -time.Second || diff > time.Second {
			t.Errorf("%s has %v left, want %v", key, d.TTL, ttl-gap)
		}
		if diff := want[key].TTL - d.TTL; diff < -time.Second || diff > time.Second {
			t.Errorf("%s has %v left, the source %v", key, d.TTL, want[key].TTL)
		}
	}
	set := exportSets(t, dst.st.Ports)["issuer:sessions-of:ada"]
	if diff := (2*time.Hour - gap) - set.TTL; diff < -time.Second || diff > time.Second {
		t.Errorf("the Index set has %v left, want %v", set.TTL, 2*time.Hour-gap)
	}
}

func TestCopySkipsAndCountsWhatExpiredBeforeItWasWritten(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}
	src, dst := clockedSides(t, clock)
	for key, ttl := range map[string]time.Duration{
		"issuer:code:short-lived":         2 * time.Minute,
		"issuer:keyring:entry:ES384:kid1": 30 * 24 * time.Hour,
	} {
		if _, err := src.st.Ports.State.Put(ctx, key, []byte(`"x"`), ttl); err != nil {
			t.Fatal(err)
		}
	}
	opt := copyOptions()
	opt.Skip = []string{migrate.DomainBlobs}
	opt.Now, _ = steppedClock(clock, 3*time.Minute)
	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	if report.Totals.Expired != 1 {
		t.Errorf("totals = %+v, want one expired\n%s", report.Totals, report.JSON())
	}
	got := exportAll(t, dst.st.Ports)
	if _, ok := got["issuer:code:short-lived"]; ok {
		t.Error("a record that had expired was written")
	}
	if _, ok := got["issuer:keyring:entry:ES384:kid1"]; !ok {
		t.Error("the ring entry did not arrive")
	}
}

func TestSessionsSkipLeavesThemOut(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	if _, err := src.st.Ports.State.Put(ctx, "issuer:session-rotated:spent", []byte(`"x"`), time.Hour); err != nil {
		t.Fatal(err)
	}
	opt := copyOptions()
	opt.Sessions = false
	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	got := exportAll(t, dst.st.Ports)
	for key := range got {
		for _, bare := range []string{"session", "code:", "sso", "request"} {
			if strings.HasPrefix(key, "issuer:"+bare) {
				t.Errorf("%s was copied with --sessions skip", strings.SplitN(key, ":", 3)[1])
			}
		}
	}
	for _, key := range []string{"issuer:keyring:entry:ES384:kid2", "issuer:keyring:retired:ES384:old", "issuer:kms:state-secret-fingerprint"} {
		if _, ok := got[key]; !ok {
			t.Errorf("%s was not copied", key)
		}
	}
	if sets := exportSets(t, dst.st.Ports); len(sets) != 1 {
		t.Errorf("the destination holds %d Index sets, want the ring's alone", len(sets))
	}
	// A verify with the same choice is clean.
	if _, err = migrate.VerifyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt.PlanOptions); err != nil {
		t.Errorf("VerifyV5 = %v", err)
	}
}

func TestVerifyFindsAMissingSessionAndASkippedIssuerIsLeftAlone(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	opt := copyOptions()
	opt.Skip = []string{migrate.DomainIssuer}
	if _, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt); err != nil {
		t.Fatal(err)
	}
	if got := exportAll(t, dst.st.Ports); len(got) != 0 {
		t.Fatalf("--skip issuer wrote %d issuer records", len(got))
	}
	// The frozen second pass carries them.
	if _, err := migrate.VerifyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions()); err == nil {
		t.Error("a verify of the issuer passed on a destination that has none")
	}
	if report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), copyOptions()); err != nil || report.Totals.Copied == 0 {
		t.Fatalf("second pass = %v\n%s", err, report.JSON())
	}
}

func TestTheMaintenanceFlagIsNeitherCopiedNorRefused(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	// The source is in maintenance, as it is while writers are stopped.
	if _, err := src.st.Ports.State.Put(ctx, "rec.maintenance", []byte(`{"on":true}`), 0); err != nil {
		t.Fatal(err)
	}
	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), copyOptions())
	if err != nil || !report.OK || report.Totals.Refused != 0 {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	if bytes.Contains(report.JSON(), []byte("maintenance\",")) && bytes.Contains(report.JSON(), []byte(`"kind": "maintenance"`)) {
		t.Error("the flag was planned")
	}
	if _, err = dst.st.Ports.State.Get(ctx, "rec.maintenance"); err == nil {
		t.Error("the maintenance flag was carried: the destination must start clear")
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "maintenance flag") {
		t.Error("the report does not say the flag is not carried")
	}
}
