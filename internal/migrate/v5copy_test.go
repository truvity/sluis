package migrate_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/port"
	sluissecrets "github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/secretstore/secretrec"
)

func copyOptions() migrate.V5Options {
	return migrate.V5Options{PlanOptions: planOptions(), WritersStopped: true}
}

// copied returns a seeded v4 source and a v5 destination that a copy filled.
func copied(t *testing.T) (*v4Installation, *v5Installation) {
	t.Helper()
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), copyOptions())
	if err != nil {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	return src, dst
}

func TestCopyThenVerifyOfAFullV4InstallationIsClean(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	src.rec.Reset()

	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), copyOptions())
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 = %v\n%s", err, report.JSON())
	}
	noPlanSecrets(t, report.JSON())
	if c := report.Totals; c.Copied == 0 || c.New != 0 || c.Different != 0 || c.Refused != 0 {
		t.Errorf("totals = %+v, want everything copied", c)
	}
	if v := report.Verify; v == nil || !v.OK || v.Missing != 0 || v.Different != 0 || v.Same == 0 || len(v.Problems) != 0 {
		t.Errorf("verify after the copy = %+v", v)
	}

	verified, err := migrate.VerifyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions())
	if err != nil || !verified.OK {
		t.Fatalf("VerifyV5 = %v\n%s", err, verified.JSON())
	}
	noPlanSecrets(t, verified.JSON())
	if c := verified.Totals; c.Same == 0 || c.Missing+c.Different+c.Refused+c.New != 0 {
		t.Errorf("verify totals = %+v", c)
	}

	// The organisation names its App, and the credential is at its new reference.
	d, err := migrate.OpenDomains(ctx, dst.st, false)
	if err != nil {
		t.Fatal(err)
	}
	orgs, err := d.Orgs.List(ctx)
	if err != nil || len(orgs) == 0 {
		t.Fatalf("organisations = %v, %v", orgs, err)
	}
	for _, o := range orgs {
		if o.AppRef != "roster" {
			t.Errorf("organisation %s names the App %q, want roster", o.Org, o.AppRef)
		}
	}
	s3, err := dst.v5.S3Credentials("internal/google/blobs-r2")
	if err != nil {
		t.Fatal(err)
	}
	if v, _, err := s3.Get(ctx); err != nil || v.AccessKeyID == "" {
		t.Errorf("the S3 credential on the destination = %v, %v", v.AccessKeyID != "", err)
	}

	// The source is never written.
	for _, op := range []string{"put", "delete"} {
		if got := src.rec.Addresses(op); len(got) != 0 {
			t.Errorf("the source saw a %s of %v", op, got)
		}
	}
}

func TestCopyTwiceChangesNothing(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	var writes tally
	from, to := counted(src.st, &writes), counted(dst.st, &writes)
	if _, err := migrate.CopyV5(ctx, side("v4.yaml", from), side("v5.yaml", to), copyOptions()); err != nil {
		t.Fatal(err)
	}
	if writes.total() == 0 {
		t.Fatal("the first copy wrote nothing")
	}
	writes = tally{}
	dst.rec.Reset()

	report, err := migrate.CopyV5(ctx, side("v4.yaml", from), side("v5.yaml", to), copyOptions())
	if err != nil || !report.OK {
		t.Fatalf("second CopyV5 = %v\n%s", err, report.JSON())
	}
	if c := report.Totals; c.Copied != 0 || c.New != 0 || c.Different != 0 || c.Same == 0 {
		t.Errorf("second totals = %+v, want everything the same", c)
	}
	if n := writes.total(); n != 0 {
		t.Errorf("the second copy wrote through the ports: %v", writes.n)
	}
	for _, op := range []string{"put", "delete"} {
		if got := dst.rec.Addresses(op); len(got) != 0 {
			t.Errorf("the second copy's %s %v", op, got)
		}
	}
}

func TestACopyAndAVerifyShowAChangedSourceAsDifferent(t *testing.T) {
	src, dst := copied(t)
	// A secret and a record change on the source after the copy.
	src.env[sluissecrets.EnvName("recovery/password")] = "CHANGED-PW"
	_, rev, err := src.v4.External.OIDC("local-dev").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = src.v4.External.OIDC("local-dev").Put(ctx, secretstore.OIDCv1{ClientID: "local-dev", ClientSecret: "CHANGED-CLIENT"}, rev); err != nil {
		t.Fatal(err)
	}

	verified, err := migrate.VerifyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions())
	if !errors.Is(err, migrate.ErrMismatch) || verified.OK {
		t.Fatalf("VerifyV5 = %v, want ErrMismatch", err)
	}
	if verified.Totals.Different != 2 {
		t.Errorf("different = %d, want the two changed secrets\n%s", verified.Totals.Different, verified.JSON())
	}
	noPlanSecrets(t, verified.JSON())
	for _, v := range []string{"CHANGED-PW", "CHANGED-CLIENT"} {
		if bytes.Contains(verified.JSON(), []byte(v)) {
			t.Errorf("the report holds %q", v)
		}
	}

	// A copy stops before any write unless it may overwrite.
	var writes tally
	to := counted(dst.st, &writes)
	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", to), copyOptions())
	if !errors.Is(err, migrate.ErrConflict) || report.OK || report.Totals.Different != 2 {
		t.Fatalf("CopyV5 = %v, %+v, want ErrConflict", err, report.Totals)
	}
	if n := writes.total(); n != 0 {
		t.Errorf("a conflicting copy wrote: %v", writes.n)
	}
	opt := copyOptions()
	opt.Overwrite = true
	if report, err = migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt); err != nil || report.Totals.Copied != 2 {
		t.Fatalf("CopyV5 --overwrite = %v, %+v", err, report.Totals)
	}
	if verified, err = migrate.VerifyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions()); err != nil {
		t.Fatalf("VerifyV5 after the overwrite = %v\n%s", err, verified.JSON())
	}
}

func TestVerifyFindsATamperedAndAMissingDestinationItem(t *testing.T) {
	src, dst := copied(t)
	_, rev, err := dst.v5.OIDC().StateSecret().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dst.v5.OIDC().StateSecret().Put(ctx, []byte("TAMPERED"), rev); err != nil {
		t.Fatal(err)
	}
	if err = dst.v5.InternalStore(secretstore.ModuleOIDC).Delete(ctx, "recovery-password"); err != nil {
		t.Fatal(err)
	}
	verified, err := migrate.VerifyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions())
	if !errors.Is(err, migrate.ErrMismatch) {
		t.Fatalf("VerifyV5 = %v, want ErrMismatch\n%s", err, verified.JSON())
	}
	if verified.Totals.Missing != 1 || verified.Totals.Different != 1 {
		t.Errorf("missing %d, different %d: want the recovery password and the state secret", verified.Totals.Missing, verified.Totals.Different)
	}
	if bytes.Contains(verified.JSON(), []byte("TAMPERED")) {
		t.Error("the report holds the tampered value")
	}
	noPlanSecrets(t, verified.JSON())
}

func TestVerifyFindsAnOrganisationThatNamesAnotherApp(t *testing.T) {
	src, dst := copied(t)
	opt := planOptions()
	opt.AppRef = func(string) string { return "renovate" }
	// The same record, now expected to name another App: not the same.
	verified, err := migrate.VerifyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err == nil {
		t.Fatalf("VerifyV5 = nil, want an error\n%s", verified.JSON())
	}
}

func TestCopyDryRunWritesNothing(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	var writes tally
	from, to := counted(src.st, &writes), counted(dst.st, &writes)
	src.rec.Reset()
	dst.rec.Reset()

	opt := copyOptions()
	opt.DryRun, opt.WritersStopped = true, false
	report, err := migrate.CopyV5(ctx, side("v4.yaml", from), side("v5.yaml", to), opt)
	if err != nil || !report.DryRun || report.Totals.New == 0 || report.Totals.Copied != 0 {
		t.Fatalf("dry run = %v, %+v", err, report.Totals)
	}
	if n := writes.total(); n != 0 {
		t.Errorf("the dry run wrote through the ports: %v", writes.n)
	}
	for name, rec := range map[string]*secretrec.Store{"source": src.rec, "destination": dst.rec} {
		for _, op := range []string{"put", "delete"} {
			if got := rec.Addresses(op); len(got) != 0 {
				t.Errorf("the dry run's %s %s %v", name, op, got)
			}
		}
	}
	if len(dst.rec.Addresses("get")) == 0 {
		t.Error("the dry run did not read the destination")
	}
}

func TestCopyNeedsTheWritersStopped(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	var writes tally
	opt := copyOptions()
	opt.WritersStopped = false
	if _, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", counted(dst.st, &writes)), opt); !errors.Is(err, migrate.ErrWritersRunning) {
		t.Errorf("CopyV5 = %v, want ErrWritersRunning", err)
	}
	if writes.total() != 0 {
		t.Errorf("writes: %v", writes.n)
	}
}

func TestCopyWritesNothingWhenSomethingIsRefused(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	var writes tally
	opt := copyOptions()
	// An organisation whose App slug cannot be an App id, and so has no App.
	putOrg(t, src.st, "initech", 99, "Not An Id", "INITECH-KEY")
	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", counted(dst.st, &writes)), opt)
	if !errors.Is(err, migrate.ErrRefused) || report.OK || report.Totals.Refused == 0 {
		t.Fatalf("CopyV5 = %v, want ErrRefused", err)
	}
	if n := writes.total(); n != 0 {
		t.Errorf("a refused copy wrote: %v", writes.n)
	}
	if len(dst.rec.Addresses("put")) != 0 {
		t.Errorf("a refused copy put %v", dst.rec.Addresses("put"))
	}
}

func TestTwoPassCopy(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	// Pass one runs while the source is live, without the issuer.
	first := copyOptions()
	first.Skip, first.WritersStopped = []string{migrate.DomainIssuer}, false
	r1, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), first)
	if err != nil || r1.Totals.Copied == 0 || !r1.Live {
		t.Fatalf("first pass = %v, %+v", err, r1.Totals)
	}
	// The source changes while people still use it.
	src.env[sluissecrets.EnvName("recovery/password")] = "CHANGED-PW"
	if _, err = src.st.Ports.Blob.Write(ctx, "reports/github/acme", []byte(`{"report":3}`)); err != nil {
		t.Fatal(err)
	}

	// Verify sees what pass one missed.
	if _, err = migrate.VerifyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), planOptions()); !errors.Is(err, migrate.ErrMismatch) {
		t.Fatalf("VerifyV5 between the passes = %v, want ErrMismatch", err)
	}
	// The final pass, with the writers stopped, brings it over and touches only that.
	var writes tally
	final := copyOptions()
	final.Overwrite = true
	r2, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", counted(dst.st, &writes)), final)
	if err != nil || !r2.OK {
		t.Fatalf("final pass = %v\n%s", err, r2.JSON())
	}
	// What changed meanwhile, and the issuer: the frozen pass carries it.
	issuerRows := len(exportAll(t, src.st.Ports)) + len(exportSets(t, src.st.Ports))
	if got := len(exportAll(t, dst.st.Ports)); got != issuerRows-len(exportSets(t, src.st.Ports)) {
		t.Errorf("the destination holds %d issuer records after the final pass, the source %d", got, issuerRows)
	}
	if r2.Totals.Copied != 2+issuerRows {
		t.Errorf("final pass copied %d, first %d: want what changed and the issuer's %d", r2.Totals.Copied, r1.Totals.Copied, issuerRows)
	}
	if v := r2.Verify; v == nil || !v.OK {
		t.Errorf("verify after the final pass = %+v", v)
	}
	noPlanSecrets(t, r1.JSON())
	noPlanSecrets(t, r2.JSON())
}

// A destination whose issuer recorded a fingerprint of its own state secret,
// beside a source that never recorded one: the destination's guard is to match
// the source's, absence included.
func TestOverwriteDeletesADestinationFingerprintTheSourceLacks(t *testing.T) {
	src, dst := copied(t)
	const key = "issuer:kms:state-secret-fingerprint"
	if err := src.st.Ports.State.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	from, to := side("v4.yaml", src.st), side("v5.yaml", dst.st)

	verified, err := migrate.VerifyV5(ctx, from, to, planOptions())
	if !errors.Is(err, migrate.ErrMismatch) || verified.Totals.Different != 1 {
		t.Fatalf("VerifyV5 = %v, %+v, want one different item (the stray fingerprint)", err, verified.Totals)
	}
	report, err := migrate.CopyV5(ctx, from, to, copyOptions())
	if !errors.Is(err, migrate.ErrConflict) || report.Totals.Different != 1 {
		t.Fatalf("CopyV5 = %v, %+v, want ErrConflict", err, report.Totals)
	}
	if _, err = dst.st.Ports.State.Get(ctx, key); err != nil {
		t.Fatalf("a refused copy removed the fingerprint: %v", err)
	}

	opt := copyOptions()
	opt.Overwrite = true
	if report, err = migrate.CopyV5(ctx, from, to, opt); err != nil || report.Totals.Copied != 1 {
		t.Fatalf("CopyV5 --overwrite = %v, %+v\n%s", err, report.Totals, report.JSON())
	}
	if _, err = dst.st.Ports.State.Get(ctx, key); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("the destination's fingerprint after --overwrite: %v, want ErrNotFound", err)
	}
	if verified, err = migrate.VerifyV5(ctx, from, to, planOptions()); err != nil {
		t.Fatalf("VerifyV5 after --overwrite = %v\n%s", err, verified.JSON())
	}
}

func TestLiveFirstPassSkippingTheIssuerNeedsNoFlag(t *testing.T) {
	src, dst := newV4Installation(t), newV5Installation(t)
	src.seedFull(t)
	opt := copyOptions()
	opt.WritersStopped = false
	opt.Skip = []string{"issuer"}
	report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), opt)
	if err != nil || !report.OK {
		t.Fatalf("CopyV5 = %v", err)
	}
	if !report.Live || !strings.Contains(strings.Join(report.Notes, "\n"), "final pass") {
		t.Errorf("the report does not say it ran live and a final pass is required: live=%v notes=%v", report.Live, report.Notes)
	}
	final := copyOptions()
	final.Overwrite = true
	if report, err = migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", dst.st), final); err != nil || report.Live {
		t.Fatalf("the final pass = %v, live=%v", err, report.Live)
	}
}

// changingBlob changes the source once, after the first write to the destination.
type changingBlob struct {
	port.Blob
	change func()
	done   bool
}

func (c *changingBlob) Write(ctx context.Context, n string, b []byte) (string, error) {
	v, err := c.Blob.Write(ctx, n, b)
	if !c.done {
		c.done = true
		c.change()
	}
	return v, err
}

func TestLivePassReportsWhatChangedInsteadOfFailing(t *testing.T) {
	for _, live := range []bool{true, false} {
		src, dst := newV4Installation(t), newV5Installation(t)
		src.seedFull(t)
		to := *dst.st
		to.Ports.Blob = &changingBlob{Blob: dst.st.Ports.Blob, change: func() {
			src.env[sluissecrets.EnvName("recovery/password")] = "CHANGED-WHILE-LIVE"
		}}
		opt := copyOptions()
		if live {
			opt.Skip, opt.WritersStopped = []string{migrate.DomainIssuer}, false
		} else {
			opt.Skip = []string{migrate.DomainIssuer}
		}
		report, err := migrate.CopyV5(ctx, side("v4.yaml", src.st), side("v5.yaml", &to), opt)
		if !live {
			if !errors.Is(err, migrate.ErrMismatch) {
				t.Errorf("a non-live copy whose source changed = %v, want ErrMismatch", err)
			}
			continue
		}
		if err != nil || !report.OK || !report.Live {
			t.Fatalf("a live pass = %v\n%s", err, report.JSON())
		}
		if v := report.Verify; v == nil || v.OK || v.Missing+v.Different == 0 {
			t.Errorf("the verify section does not show the change: %+v", v)
		}
		if !strings.Contains(strings.Join(report.Notes, "\n"), "changed on the live source") {
			t.Errorf("no note about the change: %v", report.Notes)
		}
	}
}
