package restorejob_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/backup/restorejob"
)

// forge puts a backup in the archive the way anyone with PutObject on it can:
// a well-formed object and sidecar, encrypted to the public recipient, and no
// record of it in the trail.
func (r *rig) forge(id string) {
	r.t.Helper()
	r.publish(id, func(w *backup.Writer) (*backup.Archive, error) { return w.Close(ctx) })
	delete(r.trail, id)
}

func (r *rig) untouched(t *testing.T) {
	t.Helper()
	if len(r.flagged()) != 0 || len(r.writes) != 0 || len(r.audit.Records()) != 0 {
		t.Errorf("a refused restore changed something: flagged %v, writes %v, audit %v", r.flagged(), r.writes, r.audit.Actions())
	}
	if st, _ := r.job().Status(ctx); st.Latest != nil {
		t.Errorf("a refused restore left a record: %+v", st.Latest)
	}
}

func refused(t *testing.T, res restorejob.Result, err error, reason, wants string) {
	t.Helper()
	if err != nil || res.Outcome != restorejob.OutcomeRefused || res.Reason != reason || !strings.Contains(res.Error, wants) {
		t.Fatalf("%+v, %v; want a refusal %q mentioning %q", res, err, reason, wants)
	}
}

func TestAGoodBackupIsRestoredAndIsNotRecordedAsUnverified(t *testing.T) {
	r := newRig(t)
	res, err := r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
	if err != nil || res.Outcome != restorejob.OutcomeCompleted {
		t.Fatalf("%+v, %v", res, err)
	}
	if res.Run.Unverified || res.Run.Verification != "" || res.Run.ObjectSHA256 == "" || res.Run.SidecarSHA256 == "" {
		t.Errorf("run %+v", res.Run)
	}
	for _, rec := range append(r.audit.Find("roster.restore.started"), r.audit.Find("roster.restore.completed")...) {
		if _, ok := rec.GetData().GetFields()["unverified"]; ok {
			t.Errorf("a verified restore is recorded as unverified: %v", rec.GetData())
		}
	}
}

func TestAForgedBackupWithNoRecordIsRefusedBeforeAnythingIsDecrypted(t *testing.T) {
	r := newRig(t)
	r.forge("evil")
	identityRead := false
	res, err := r.job(func(c *restorejob.Config) {
		c.Identity = func(ctx context.Context) ([]age.Identity, error) { identityRead = true; return identities(ctx) }
	}).Start(ctx, restorejob.Request{BackupID: "evil", Actor: admin})
	refused(t, res, err, restorejob.ReasonUnverified, "no record")
	if identityRead {
		t.Error("the identity was read for a backup nothing vouches for")
	}
	r.untouched(t)
	if _, err := r.job().Preview(ctx, "evil"); !errors.Is(err, backup.ErrNotAttested) {
		t.Errorf("a preview of it: %v", err)
	}
}

func TestARecordThatDisagreesWithTheArchiveRefusesTheBackup(t *testing.T) {
	for name, tamper := range map[string]func(c *backup.Claim){
		"object hash":   func(c *backup.Claim) { c.ObjectSHA256 = strings.Repeat("0", 64) },
		"sidecar hash":  func(c *backup.Claim) { c.SidecarSHA256 = strings.Repeat("1", 64) },
		"object size":   func(c *backup.Claim) { c.ObjectSize++ },
		"object key":    func(c *backup.Claim) { c.ObjectKey = "backup/example/other.tar.gz.age" },
		"installation":  func(c *backup.Claim) { c.Instance = "elsewhere" },
		"recipients":    func(c *backup.Claim) { c.Recipients = backup.Fingerprints([]string{"age1other"}) },
		"no recipients": func(c *backup.Claim) { c.Recipients = nil },
		"an old record": func(c *backup.Claim) { c.ObjectSHA256, c.SidecarSHA256, c.Recipients = "", "", nil },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			c := r.trail["b1"]
			tamper(&c)
			r.trail["b1"] = c
			res, err := r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
			refused(t, res, err, restorejob.ReasonUnverified, "different")
			r.untouched(t)
		})
	}
}

func TestASidecarRewrittenAfterTheRecordIsRefused(t *testing.T) {
	r := newRig(t)
	// Same object, same hash, a sidecar nudged by someone with PutObject.
	raw, err := r.archive.Read(ctx, backup.SidecarName("example", "b1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.archive.Write(ctx, backup.SidecarName("example", "b1"), append(raw.Body, ' ')); err != nil {
		t.Fatal(err)
	}
	res, err := r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
	refused(t, res, err, restorejob.ReasonUnverified, "sidecar hash")
	r.untouched(t)
}

func TestABackupInAnHourThatIsNotSealedYetWaitsForTheSeal(t *testing.T) {
	r := newRig(t)
	c := r.trail["b1"]
	c.Sealed, c.SealKey = false, ""
	r.trail["b1"] = c
	res, err := r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
	refused(t, res, err, restorejob.ReasonUnverified, "not sealed")
	r.untouched(t)
	// The notary's next run seals the hour; the same call now succeeds.
	c.Sealed, c.SealKey = true, "seals/activity/t/2026/10/10/02.jws"
	r.trail["b1"] = c
	if res, err = r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin}); err != nil || res.Outcome != restorejob.OutcomeCompleted {
		t.Fatalf("after the seal: %+v, %v", res, err)
	}
}

func TestTwoRecordsThatDisagreeRefuseTheBackup(t *testing.T) {
	r := newRig(t)
	good := r.trail["b1"]
	bad := good
	bad.RecordID, bad.ObjectSHA256 = "rec-forged", strings.Repeat("a", 64)
	res, err := r.job(func(c *restorejob.Config) {
		c.Attestor = backup.AttestorFunc(func(context.Context, string, string) ([]backup.Claim, error) {
			return []backup.Claim{good, bad}, nil
		})
	}).Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
	refused(t, res, err, restorejob.ReasonUnverified, "rec-forged")
}

func TestAnUnreachableTrailRefusesUntilTheAdminAllowsIt(t *testing.T) {
	r := newRig(t)
	r.trailDown = true
	res, err := r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
	refused(t, res, err, restorejob.ReasonUnverified, "could not be asked")
	r.untouched(t)

	res, err = r.job().Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin, AllowUnverified: true})
	if err != nil || res.Outcome != restorejob.OutcomeCompleted || !res.Run.Unverified || res.Run.Verification != backup.ReasonUnavailable {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestAnUnverifiedRestoreIsAuditedFromStartToFinish(t *testing.T) {
	r := newRig(t)
	r.forge("evil")
	res, err := r.job().Start(ctx, restorejob.Request{BackupID: "evil", Actor: admin, AllowUnverified: true})
	if err != nil || res.Outcome != restorejob.OutcomeCompleted || !res.Run.Unverified || res.Run.Verification != backup.ReasonUnattested {
		t.Fatalf("%+v, %v", res, err)
	}
	for _, action := range []string{"roster.restore.started", "roster.restore.completed"} {
		got := r.audit.Find(action)
		if len(got) != 1 {
			t.Fatalf("%s: %v", action, r.audit.Actions())
		}
		f := got[0].GetData().GetFields()
		if !f["unverified"].GetBoolValue() || f["verification"].GetStringValue() != backup.ReasonUnattested {
			t.Errorf("%s does not say the restore was unverified: %v", action, got[0].GetData())
		}
	}
}

func TestAllowUnverifiedStillRefusesAnotherSetOfRecipients(t *testing.T) {
	r := newRig(t)
	other, _ := age.GenerateX25519Identity()
	for name, allow := range map[string]bool{"verified": false, "allowing unverified": true} {
		t.Run(name, func(t *testing.T) {
			res, err := r.job(func(c *restorejob.Config) { c.Recipients = []string{other.Recipient().String()} }).
				Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin, AllowUnverified: allow})
			refused(t, res, err, restorejob.ReasonRecipients, "recipient")
			r.untouched(t)
		})
	}
}

func TestAnUnverifiedRestoreNeedsConfiguredRecipients(t *testing.T) {
	r := newRig(t)
	r.forge("evil")
	res, err := r.job(func(c *restorejob.Config) { c.Recipients = nil }).
		Start(ctx, restorejob.Request{BackupID: "evil", Actor: admin, AllowUnverified: true})
	refused(t, res, err, restorejob.ReasonRecipients, "backup.recipients")
	r.untouched(t)
	// A verified one needs none: the record and the sidecar agree.
	if res, err = r.job(func(c *restorejob.Config) { c.Recipients = nil }).Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin}); err != nil ||
		res.Outcome != restorejob.OutcomeCompleted {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestARestoreWithNoVerificationConfiguredRefusesEverything(t *testing.T) {
	r := newRig(t)
	res, err := r.job(func(c *restorejob.Config) { c.Attestor = nil }).Start(ctx, restorejob.Request{BackupID: "b1", Actor: admin})
	refused(t, res, err, restorejob.ReasonUnverified, "backup.verify is not configured")
	r.untouched(t)
}

func TestAPreviewIsVerifiedToo(t *testing.T) {
	r := newRig(t)
	r.forge("evil")
	if _, err := r.job().Preview(ctx, "evil"); !errors.Is(err, backup.ErrUnverified) {
		t.Fatalf("%v", err)
	}
	if rep, err := r.job().PreviewWith(ctx, "evil", true); err != nil || rep == nil {
		t.Fatalf("%v", err)
	}
	if _, err := r.job().Preview(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
}

func TestAPausedRestoreIsContinuedWithoutAskingTheTrailAgain(t *testing.T) {
	r := newRig(t)
	cctx, cancel := context.WithCancel(ctx)
	r.cancel, r.cancelAt = cancel, 5
	if res, _ := r.job().Start(cctx, restorejob.Request{BackupID: "b1", Actor: admin}); res.Outcome != restorejob.OutcomePaused {
		t.Fatalf("%+v", res)
	}
	asked := r.asked
	// The audit pipeline goes down while the restore is paused: the restore
	// does not depend on it any more.
	r.trailDown = true
	res, err := r.job().Start(ctx, restorejob.Request{Actor: admin})
	if err != nil || res.Outcome != restorejob.OutcomeCompleted || res.Run.Unverified {
		t.Fatalf("%+v, %v", res, err)
	}
	if r.asked != asked {
		t.Errorf("the trail was asked %d more times for a restore that had been verified", r.asked-asked)
	}
}

func TestAPausedRestoreWhoseBackupWasSwappedIsRefused(t *testing.T) {
	r := newRig(t)
	cctx, cancel := context.WithCancel(ctx)
	r.cancel, r.cancelAt = cancel, 5
	if res, _ := r.job().Start(cctx, restorejob.Request{BackupID: "b1", Actor: admin}); res.Outcome != restorejob.OutcomePaused {
		t.Fatalf("%+v", res)
	}
	// Someone with PutObject replaces the backup while the restore is paused.
	saved := r.trail["b1"]
	r.publish("b1", func(w *backup.Writer) (*backup.Archive, error) { return w.Close(ctx) })
	r.trail["b1"] = saved
	res, err := r.job().Start(ctx, restorejob.Request{Actor: admin})
	refused(t, res, err, restorejob.ReasonUnverified, "not the one this restore began with")
}

func TestAnUnverifiedRestoreStaysUnverifiedWhenItIsContinued(t *testing.T) {
	r := newRig(t)
	r.trailDown = true
	cctx, cancel := context.WithCancel(ctx)
	r.cancel, r.cancelAt = cancel, 5
	if res, _ := r.job().Start(cctx, restorejob.Request{BackupID: "b1", Actor: admin, AllowUnverified: true}); res.Outcome != restorejob.OutcomePaused ||
		!res.Run.Unverified {
		t.Fatalf("%+v", res)
	}
	// The continuation carries no flag, and needs none.
	res, err := r.job().Start(ctx, restorejob.Request{Actor: admin})
	if err != nil || res.Outcome != restorejob.OutcomeCompleted || !res.Run.Unverified || res.Run.Verification != backup.ReasonUnavailable {
		t.Fatalf("%+v, %v", res, err)
	}
	if got := r.audit.Find("roster.restore.completed"); len(got) != 1 || !got[0].GetData().GetFields()["unverified"].GetBoolValue() {
		t.Errorf("the completion does not say it was unverified: %v", got)
	}
}
