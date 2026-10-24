package auditpulumi_test

import (
	"encoding/json"
	"strings"
	"testing"

	auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"
)

// The lock guard of a bucket the library does not manage (audit 2026-10-18, A5):
// the library reads the owner's policy and says what is missing.

const existingBucket = "acme-audit-existing"

var existingBucketArn = "arn:" + "aws:s3:::" + existingBucket

func guardedPolicy(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []any{
		auditpulumi.ArchiveLockGuard(existingBucketArn, []string{arnp + "iam::" + account + ":role/breakglass"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const tlsOnly = `{"Version":"2012-10-17","Statement":[{"Sid":"OnlyOverTLS","Effect":"Deny","Principal":"*","Action":"s3:*",` +
	`"Resource":["arn:aws:s3:::acme-audit-existing","arn:aws:s3:::acme-audit-existing/*"],` +
	`"Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`

func TestHasArchiveLockGuardReadsWhatTheGuardDenies(t *testing.T) {
	stmt := func(s string) string { return `{"Version":"2012-10-17","Statement":[` + s + `]}` }
	res := `"Resource":["arn:aws:s3:::acme-audit-existing","arn:aws:s3:::acme-audit-existing/*"]`
	exempt := `"Condition":{"ArnNotLike":{"aws:PrincipalArn":["arn:aws:iam::111122223333:role/breakglass"]}}`
	for name, tc := range map[string]struct {
		policy string
		want   bool
	}{
		"the library's own statement": {guardedPolicy(t), true},
		"TLS only":                    {tlsOnly, false},
		"not JSON":                    {"", false},
		"a wildcard of the actions":   {stmt(`{"Effect":"Deny","Principal":{"AWS":"*"},"Action":"s3:*",` + res + `,` + exempt + `}`), true},
		"a single statement object": {`{"Statement":{"Effect":"Deny","Principal":"*","Action":["s3:BypassGovernanceRetention",` +
			`"s3:DeleteObjectVersion","s3:PutBucketObjectLockConfiguration"],` + res + `}}`, true},
		"split over two statements": {stmt(`{"Effect":"Deny","Principal":"*","Action":["s3:BypassGovernanceRetention","s3:DeleteObjectVersion"],` +
			`"Resource":"arn:aws:s3:::acme-audit-existing/*"},{"Effect":"Deny","Principal":"*","Action":"s3:PutBucketObjectLock*",` +
			`"Resource":"arn:aws:s3:::acme-audit-existing"}`), true},
		"the objects only": {stmt(`{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":"arn:aws:s3:::acme-audit-existing/*"}`), false},
		"another bucket": {stmt(`{"Effect":"Deny","Principal":"*","Action":"s3:*",` +
			`"Resource":["arn:aws:s3:::acme-other","arn:aws:s3:::acme-other/*"]}`), false},
		"an allow":           {stmt(`{"Effect":"Allow","Principal":"*","Action":"s3:*",` + res + `}`), false},
		"one principal only": {stmt(`{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::111122223333:role/x"},"Action":"s3:*",` + res + `}`), false},
		"NotAction":          {stmt(`{"Effect":"Deny","Principal":"*","NotAction":"s3:GetObject",` + res + `}`), false},
		"another condition": {stmt(`{"Effect":"Deny","Principal":"*","Action":"s3:*",` + res +
			`,"Condition":{"StringEquals":{"aws:PrincipalTag/team":"x"}}}`), false},
		"lower-case actions": {stmt(`{"Effect":"Deny","Principal":"*","Action":["s3:bypassgovernanceretention",` +
			`"s3:deleteobjectversion","s3:putbucketobjectlockconfiguration"],` + res + `}`), true},
	} {
		if got := auditpulumi.HasArchiveLockGuard(tc.policy, existingBucketArn); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// existing puts the standard preset on an existing bucket, and the mock answers
// for its policy and Object Lock.
func existing(t *testing.T, mode, policy string, lock map[string]string) error {
	t.Helper()
	mockSetup = func(r *recorder) {
		r.bucketPolicy = map[string]string{}
		if policy != "" {
			r.bucketPolicy[existingBucket] = policy
		}
		r.objectLock = lock
	}
	t.Cleanup(func() { mockSetup = nil })
	_, _, err := build(t, func(a *auditpulumi.Args) {
		a.Presets = map[string]auditpulumi.PresetStorage{"standard": {Bucket: existingBucket, Prefix: "standard/"}}
		a.Archive.ExistingBucketLockGuard = mode
	})
	return err
}

func TestAnExistingBucketWithoutTheGuardIsReportedByDefault(t *testing.T) {
	for _, policy := range []string{"", tlsOnly} {
		for _, mode := range []string{"", auditpulumi.LockGuardReport, auditpulumi.LockGuardOff} {
			if err := existing(t, mode, policy, nil); err != nil {
				t.Errorf("mode %q, policy %q: %v, want a warning and no refusal", mode, policy, err)
			}
		}
	}
}

func TestRequireRefusesAnExistingBucketWithoutTheGuard(t *testing.T) {
	for name, policy := range map[string]string{"no policy": "", "TLS only": tlsOnly} {
		err := existing(t, auditpulumi.LockGuardRequire, policy, nil)
		if err == nil || !strings.Contains(err.Error(), existingBucket) || !strings.Contains(err.Error(), "ArchiveLockGuard") {
			t.Errorf("%s: %v, want a refusal naming the bucket and ArchiveLockGuard", name, err)
		}
	}
	if err := existing(t, auditpulumi.LockGuardRequire, guardedPolicy(t), nil); err != nil {
		t.Errorf("a guarded bucket: %v", err)
	}
}

// Object Lock cannot be turned on by an apply, so its absence is never a
// refusal, not even with require.
func TestNoObjectLockOnAnExistingAttestedBucketIsNeverARefusal(t *testing.T) {
	mockSetup = func(r *recorder) {
		r.bucketPolicy = map[string]string{existingBucket: guardedPolicy(t)}
		r.objectLock = map[string]string{}
	}
	t.Cleanup(func() { mockSetup = nil })
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Presets = map[string]auditpulumi.PresetStorage{"attested": {Bucket: existingBucket, Prefix: "attested/"}}
		a.Writer.DeploymentYAML = "profiles:\n  security:\n    frameworks: [security]\n    preset: attested\n"
		a.Archive.ExistingBucketLockGuard = auditpulumi.LockGuardRequire
	})
	if err != nil {
		t.Fatalf("no Object Lock refused the deploy: %v", err)
	}
	looked := false
	for _, c := range rec.calls {
		looked = looked || c.Token == "aws:s3/getBucketObjectLockConfiguration:getBucketObjectLockConfiguration"
	}
	if !looked {
		t.Error("the attested preset's existing bucket was not asked for its Object Lock")
	}
}

func TestOffDoesNotLook(t *testing.T) {
	mockSetup = func(r *recorder) { r.bucketPolicy = map[string]string{} }
	t.Cleanup(func() { mockSetup = nil })
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Presets = map[string]auditpulumi.PresetStorage{"standard": {Bucket: existingBucket, Prefix: "standard/"}}
		a.Archive.ExistingBucketLockGuard = auditpulumi.LockGuardOff
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.calls {
		if strings.HasPrefix(c.Token, "aws:s3/getBucket") {
			t.Errorf("off still asked %s", c.Token)
		}
	}
}

// A bucket the library creates or adopts carries the guard in its own policy and
// is not looked up.
func TestAManagedBucketIsNotLookedUp(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.calls {
		if strings.HasPrefix(c.Token, "aws:s3/getBucket") {
			t.Errorf("a created bucket was asked %s", c.Token)
		}
	}
}

func TestAnUnknownLockGuardModeIsRefused(t *testing.T) {
	_, _, err := build(t, func(a *auditpulumi.Args) { a.Archive.ExistingBucketLockGuard = "enforce" })
	if err == nil || !strings.Contains(err.Error(), "ExistingBucketLockGuard") {
		t.Errorf("%v, want a refusal naming ExistingBucketLockGuard", err)
	}
}

// NewArchiveBucket, the estate's own declaration of its bucket, writes the guard
// when it is told the break-glass roles, and the TLS-only text as before when not.
func TestArchiveBucketWritesTheLockGuardForTheBreakglassRoles(t *testing.T) {
	policyOf := func(rec *recorder) string {
		t.Helper()
		return prop(rec.one(t, auditpulumi.ArchivePolicyType, "audit-archive"), "policy").StringValue()
	}
	rec, err := runArchiveBucket(t, archiveBucketArgs())
	if err != nil {
		t.Fatal(err)
	}
	if p := policyOf(rec); auditpulumi.HasArchiveLockGuard(p, "arn:aws:s3:::example-audit-archive") || !strings.Contains(p, "OnlyOverTLS") {
		t.Errorf("without break-glass roles the policy changed: %s", p)
	}

	args := archiveBucketArgs()
	args.BreakglassRoleArns = []string{arnp + "iam::" + account + ":role/breakglass"}
	rec, err = runArchiveBucket(t, args)
	if err != nil {
		t.Fatal(err)
	}
	p := policyOf(rec)
	if !auditpulumi.HasArchiveLockGuard(p, "arn:aws:s3:::example-audit-archive") || !strings.Contains(p, "OnlyOverTLS") ||
		!strings.Contains(p, args.BreakglassRoleArns[0]) {
		t.Errorf("the policy does not carry the guard and the TLS statement: %s", p)
	}

	args.BreakglassRoleArns = []string{"*"}
	if _, err := runArchiveBucket(t, args); err == nil || !strings.Contains(err.Error(), "BreakglassRoleArns") {
		t.Errorf("a wildcard break-glass role: %v", err)
	}
}
