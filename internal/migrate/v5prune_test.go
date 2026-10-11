package migrate_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/truvity/sluis/internal/migrate"
	sluissecrets "github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
)

// pruneOptions never waits, and records what it would have waited.
func pruneOptions(waits *[]time.Duration) migrate.PruneOptions {
	return migrate.PruneOptions{
		PlanOptions: planOptions(),
		Sleep:       func(_ context.Context, d time.Duration) error { *waits = append(*waits, d); return nil },
	}
}

// putRaw puts a parameter below the v4 root the way an operator or Pulumi would.
func putRaw(t *testing.T, src *v4Installation, name string) {
	t.Helper()
	if _, err := src.rec.Put(ctx, name, []byte(`{}`), ""); err != nil {
		t.Fatal(err)
	}
}

func names(t *testing.T, src *v4Installation) []string {
	t.Helper()
	all, err := state.ListAll(ctx, src.rec)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func kept(r *migrate.PruneReport, name string) (migrate.KeptParam, bool) {
	for _, k := range r.Kept {
		if k.Name == name {
			return k, true
		}
	}
	return migrate.KeptParam{}, false
}

func TestPruneDryRunListsWhatItWouldDeleteAndKeepsTheRest(t *testing.T) {
	src, dst := copied(t)
	for _, n := range []string{
		"internal/config/valkey/password", "internal/config/issuer/state-secret", "internal/config/recovery/password",
		"internal/config/unknown/thing",
	} {
		putRaw(t, src, n)
	}
	before := names(t, src)
	var waits []time.Duration
	opt := pruneOptions(&waits)
	opt.DryRun = true
	r, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil || !r.OK || r.Gate != "passed" || r.PlanHash == "" {
		t.Fatalf("PruneV5 --dry-run = %v\n%s", err, r.JSON())
	}
	if got := names(t, src); !slices.Equal(got, before) {
		t.Errorf("a dry run changed the source: %v", got)
	}
	for name, class := range map[string]string{
		"internal/config/valkey/password":     migrate.KeptNotMigrated,
		"internal/config/issuer/state-secret": migrate.KeptPulumi,
		"internal/config/recovery/password":   migrate.KeptPulumi,
		"internal/config/unknown/thing":       migrate.KeptUnmapped,
	} {
		k, ok := kept(r, name)
		if !ok || k.Class != class || k.Reason == "" {
			t.Errorf("%s: kept = %+v, %v; want class %s\n%s", name, k, ok, class, r.JSON())
		}
		if slices.Contains(r.Delete, name) {
			t.Errorf("%s is to be deleted", name)
		}
	}
	// The fixture's organisation names the roster App, whose key is another one:
	// the organisation's own key is not proven to be on v5, so it is kept.
	orgKeys := 0
	for _, k := range r.Kept {
		if strings.HasPrefix(k.Name, "internal/credentials/github-org/") {
			orgKeys++
			if k.Class != migrate.KeptUnverified {
				t.Errorf("%s kept as %s", k.Name, k.Class)
			}
		}
	}
	if orgKeys == 0 {
		t.Errorf("the organisation's credential is not among the kept parameters\n%s", r.JSON())
	}
	for _, want := range []string{
		"external/cloudflare/r2", "external/github/runner-stable-acme", "external/oidc/local-dev", "external/slack/notifier",
		"internal/blobs/r2", "internal/cloudflare-minted/r2", "internal/cloudflare/prod/minter", "internal/credentials/console/session-key",
	} {
		if !slices.Contains(r.Delete, want) {
			t.Errorf("%s is not among the parameters to delete: %v", want, r.Delete)
		}
	}
	for _, d := range r.Delete {
		if strings.HasPrefix(d, "internal/config/") && d != "internal/config/unknown/thing" {
			t.Errorf("a config parameter is to be deleted: %s", d)
		}
	}
	if r.LegacyTableItems == 0 {
		t.Errorf("the legacy table's records were not counted: %+v", r)
	}
	noPlanSecrets(t, r.JSON())
	if len(waits) != 0 {
		t.Errorf("a dry run waited %v", waits)
	}
}

func TestPruneRefusesUnlessTheDestinationHoldsEverythingEqual(t *testing.T) {
	for name, change := range map[string]func(*testing.T, *v4Installation, *v5Installation){
		"different": func(_ *testing.T, src *v4Installation, _ *v5Installation) {
			src.env[sluissecrets.EnvName("recovery/password")] = "CHANGED-PW"
		},
		"new": func(t *testing.T, src *v4Installation, _ *v5Installation) {
			if _, err := src.v4.External.OIDC("late").Put(ctx, secretstore.OIDCv1{ClientID: "late", ClientSecret: "LATE"}, ""); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			src, dst := copied(t)
			change(t, src, dst)
			before := names(t, src)
			var waits []time.Duration
			opt := pruneOptions(&waits)
			opt.Confirm = "0123456789abcdef"
			r, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
			if !errors.Is(err, migrate.ErrPruneGate) || r == nil || r.OK || !strings.HasPrefix(r.Gate, "refused") || len(r.Delete) != 0 {
				t.Fatalf("PruneV5 = %v\n%s", err, r.JSON())
			}
			if !strings.Contains(err.Error(), "the first is") {
				t.Errorf("the refusal does not say why: %v", err)
			}
			if got := names(t, src); !slices.Equal(got, before) {
				t.Errorf("a refused prune deleted: %v", got)
			}
			// A dry run is refused the same way.
			opt.Confirm, opt.DryRun = "", true
			if _, err = migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt); !errors.Is(err, migrate.ErrPruneGate) {
				t.Errorf("dry run = %v, want the gate", err)
			}
		})
	}
}

func TestPruneRefusesAnUnconfirmedRealRun(t *testing.T) {
	src, dst := copied(t)
	before := names(t, src)
	var waits []time.Duration
	opt := pruneOptions(&waits)
	dry := opt
	dry.DryRun = true
	plan, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), dry)
	if err != nil {
		t.Fatal(err)
	}
	for name, confirm := range map[string]string{"none": "", "another plan": "0000000000000000"} {
		opt.Confirm = confirm
		r, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
		if !errors.Is(err, migrate.ErrPruneUnconfirmed) || r.OK || r.Deleted != 0 {
			t.Errorf("%s: PruneV5 = %v", name, err)
		}
		if got := names(t, src); !slices.Equal(got, before) {
			t.Errorf("%s: deleted %v", name, got)
		}
	}
	// A plan that changed since the dry run has another hash.
	putRaw(t, src, "internal/config/valkey/password")
	opt.Confirm = plan.PlanHash
	if _, err = migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt); !errors.Is(err, migrate.ErrPruneUnconfirmed) {
		t.Errorf("a hash of an older plan = %v, want it refused", err)
	}
}

func TestPruneDeletesInBatchesOfTenPacedAndKeepsTheRest(t *testing.T) {
	src, dst := copied(t)
	// More parameters than one batch: the Cloudflare module's minted-token records.
	for i := range 23 {
		name := "cloudflare-minted/p" + string(rune('a'+i))
		if _, err := src.v4.Internal.Store().Put(ctx, name, []byte(`{"tokens":[]}`), ""); err != nil {
			t.Fatal(err)
		}
	}
	putRaw(t, src, "internal/config/valkey/password")

	var waits []time.Duration
	opt := pruneOptions(&waits)
	opt.DryRun = true
	plan, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err == nil {
		// The new minted records are not on the destination: the gate holds them back.
		t.Fatalf("PruneV5 = nil with 23 unmigrated records\n%s", plan.JSON())
	}
	if _, err = migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), copyOptions()); err != nil {
		t.Fatal(err)
	}
	if plan, err = migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt); err != nil {
		t.Fatal(err)
	}

	opt.DryRun, opt.Confirm = false, plan.PlanHash
	src.rec.Reset()
	r, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil || !r.OK || r.Deleted != len(plan.Delete) || r.Deleted < 25 {
		t.Fatalf("PruneV5 = %v\n%s", err, r.JSON())
	}
	batches := src.rec.Batches()
	if len(batches) != r.Batches || len(batches) < 3 {
		t.Fatalf("batches = %v, report says %d", batches, r.Batches)
	}
	total := 0
	for _, b := range batches {
		if b < 1 || b > 10 {
			t.Errorf("a batch of %d, want 1 to 10", b)
		}
		total += b
	}
	if total != r.Deleted {
		t.Errorf("batches hold %d keys, deleted %d", total, r.Deleted)
	}
	if len(waits) != len(batches) {
		t.Errorf("waited %d times for %d batches", len(waits), len(batches))
	}
	left := names(t, src)
	for _, k := range plan.Kept {
		if !slices.Contains(left, k.Name) {
			t.Errorf("the kept parameter %s was deleted", k.Name)
		}
	}
	if len(left) != len(plan.Kept) {
		t.Errorf("left %v, want only the kept: %d", left, len(plan.Kept))
	}
}

type throttle struct{}

func (throttle) Error() string                 { return "Rate exceeded" }
func (throttle) ErrorCode() string             { return "ThrottlingException" }
func (throttle) ErrorMessage() string          { return "Rate exceeded" }
func (throttle) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func TestPruneBacksOffWhenThrottledAndStopsOnAnyOtherError(t *testing.T) {
	src, dst := copied(t)
	var waits []time.Duration
	opt := pruneOptions(&waits)
	opt.Pause, opt.Backoff = time.Second, 2*time.Second
	opt.DryRun = true
	plan, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil {
		t.Fatal(err)
	}
	opt.DryRun, opt.Confirm = false, plan.PlanHash

	src.rec.FailBatches(3, throttle{})
	r, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil || r.Deleted != len(plan.Delete) {
		t.Fatalf("PruneV5 under throttling = %v\n%s", err, r.JSON())
	}
	if len(waits) < 3 || waits[0] != 2*time.Second || waits[1] != 4*time.Second || waits[2] != 8*time.Second {
		t.Errorf("waits = %v, want the backoff to double: 2s 4s 8s", waits)
	}

	// Another error stops the run without trying again.
	src2, dst2 := copied(t)
	waits = nil
	dry := opt
	dry.DryRun, dry.Confirm = true, ""
	plan, _ = migrate.PruneV5(ctx, side("v4.yaml", src2.st), side("v5.yaml", dst2.st), dry)
	opt.Confirm = plan.PlanHash
	src2.rec.FailBatches(1, errors.New("access denied"))
	r, err = migrate.PruneV5(ctx, side("v4.yaml", src2.st), side("v5.yaml", dst2.st), opt)
	if err == nil || r.OK || len(waits) != 0 || r.Deleted != 0 {
		t.Errorf("PruneV5 on an error = %v, waits %v", err, waits)
	}
}

func TestPruneKeepsASharedAddressWhenBothSidesShareARoot(t *testing.T) {
	src, dst := copied(t)
	var waits []time.Duration
	opt := pruneOptions(&waits)
	opt.DryRun, opt.SameRoot = true, true
	r, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"external/oidc/local-dev", "external/cloudflare/r2", "internal/cloudflare/prod/minter"} {
		if k, ok := kept(r, name); !ok || k.Class != migrate.KeptShared {
			t.Errorf("%s: kept = %+v, %v; want shared-address", name, k, ok)
		}
	}
	if !slices.Contains(r.Delete, "internal/cloudflare-minted/r2") {
		t.Errorf("a v4-only name is not deleted: %v", r.Delete)
	}
}

// An organisation without a source App of its own is made one, with its key:
// that key is on layout v5, found equal by hash, and its v4 parameter goes.
func TestPruneDeletesAnOrganisationsKeyOnceItsSynthesizedAppHoldsIt(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	seed(t, src.st)
	opt := planOptions()
	opt.AppRef = nil
	if _, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st),
		migrate.V5Options{PlanOptions: opt, WritersStopped: true}); err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	po := pruneOptions(&waits)
	po.PlanOptions, po.DryRun = opt, true
	r, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), po)
	if err != nil {
		t.Fatalf("PruneV5 = %v", err)
	}
	var del, keptOrg int
	for _, n := range r.Delete {
		if strings.HasPrefix(n, "internal/credentials/github-org/") {
			del++
		}
	}
	for _, k := range r.Kept {
		if strings.HasPrefix(k.Name, "internal/credentials/github-org/") {
			keptOrg++
		}
	}
	if del != 1 || keptOrg != 0 {
		t.Errorf("organisation keys: %d to delete, %d kept\n%s", del, keptOrg, r.JSON())
	}
	noPlanSecrets(t, r.JSON())
}

// A prune whose intent cannot be recorded deletes nothing; one whose intent is
// recorded sees the names it is about to delete, once, before the first delete.
func TestPruneRecordsItsIntentBeforeTheFirstDelete(t *testing.T) {
	src, dst := copied(t)
	putRaw(t, src, "internal/config/valkey/password")
	var waits []time.Duration
	dry := pruneOptions(&waits)
	dry.DryRun = true
	plan, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), dry)
	if err != nil {
		t.Fatal(err)
	}
	before := names(t, src)

	refused := errors.New("the queue refused the record")
	opt := pruneOptions(&waits)
	opt.Confirm = plan.PlanHash
	opt.Before = func(context.Context, *migrate.PruneReport) error { return refused }
	r, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if !errors.Is(err, refused) || !errors.Is(err, migrate.ErrNotStarted) || r.OK || r.Deleted != 0 {
		t.Fatalf("PruneV5 with a refused record = %v", err)
	}
	if got := names(t, src); !slices.Equal(got, before) {
		t.Errorf("a refused record still deleted: %v", got)
	}

	calls := 0
	opt.Before = func(_ context.Context, r *migrate.PruneReport) error {
		calls++
		if r.Deleted != 0 || !slices.Equal(r.Delete, plan.Delete) || r.PlanHash != plan.PlanHash {
			t.Errorf("the hook saw %d deleted, %d to delete", r.Deleted, len(r.Delete))
		}
		if got := names(t, src); !slices.Equal(got, before) {
			t.Errorf("the hook ran after a delete: %v", got)
		}
		return nil
	}
	if r, err = migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt); err != nil || calls != 1 || r.Deleted != len(plan.Delete) {
		t.Fatalf("PruneV5 = %v after %d calls\n%s", err, calls, r.JSON())
	}
}

func TestPruneDryRunNeverCallsTheIntentHook(t *testing.T) {
	src, dst := copied(t)
	var waits []time.Duration
	opt := pruneOptions(&waits)
	opt.DryRun = true
	opt.Before = func(context.Context, *migrate.PruneReport) error { t.Error("a dry run called the hook"); return nil }
	if _, err := migrate.PruneV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt); err != nil {
		t.Fatal(err)
	}
}
