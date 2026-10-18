package auditpulumi_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"
)

const bucketPolicyType = "aws:s3/bucketPolicy:BucketPolicy"

// ---- the archive buckets (audit 2026-10-18, A5)

// The bucket's policy keeps the lock to the break-glass role: nobody else
// bypasses a governance retention, changes the lock's configuration or deletes a
// version, whatever their IAM policies allow.
func TestTheArchiveBucketPolicyKeepsTheLockToTheBreakglassRole(t *testing.T) {
	rec, _, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	pol := policyOf(t, prop(rec.one(t, bucketPolicyType, "audit-archive-standard"), "policy").StringValue())
	var guard map[string]any
	for _, s := range pol {
		if s["Sid"] == "LockBreakglassOnly" {
			guard = s
		}
	}
	if guard == nil || guard["Effect"] != "Deny" || guard["Principal"] != "*" ||
		!reflect.DeepEqual(strs(guard["Action"]), []string{"s3:BypassGovernanceRetention", "s3:DeleteObjectVersion", "s3:PutBucketObjectLockConfiguration"}) {
		t.Fatalf("the lock's guard: %v", guard)
	}
	want := map[string]any{"ArnNotLike": map[string]any{"aws:PrincipalArn": []any{arnp + "iam::" + account + ":role/breakglass"}}}
	if !reflect.DeepEqual(guard["Condition"], want) {
		t.Errorf("guard condition = %v", guard["Condition"])
	}
	if res := strs(guard["Resource"]); len(res) != 2 || !strings.HasSuffix(res[1], "/*") {
		t.Errorf("guard resources = %v", res)
	}
	// The TLS-only deny is still the first statement, unchanged.
	if pol[0]["Sid"] != "OnlyOverTLS" {
		t.Errorf("first statement = %v", pol[0]["Sid"])
	}
}

func TestTheArchiveGuardNeedsBreakglassRoles(t *testing.T) {
	root := arnp + "iam::" + account + ":root"
	for name, c := range map[string]struct {
		roles []string
		want  string
	}{
		"none":            {nil, "Archive.BreakglassRoleArns is required"},
		"the root":        {[]string{root}, "not the ARN of an IAM role"},
		"every role":      {[]string{arnp + "iam::" + account + ":role/*"}, "literal characters"},
		"a bare star":     {[]string{"*"}, "not the ARN of an IAM role"},
		"a wildcard path": {[]string{arnp + "iam::" + account + ":role/*/breakglass"}, "wildcard in the role's path"},
	} {
		_, _, err := build(t, func(a *auditpulumi.Args) { a.Archive.BreakglassRoleArns = c.roles })
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error with %q", name, err, c.want)
		}
	}
	// With nothing the library manages there is nothing to guard.
	_, _, err := build(t, func(a *auditpulumi.Args) {
		a.Presets = map[string]auditpulumi.PresetStorage{"standard": {Bucket: "acme-audit", Endpoint: "https://r2.example.test"}}
		a.Archive.BreakglassRoleArns = []string{arnp + "iam::" + account + ":role/breakglass"}
	})
	if err == nil {
		t.Error("break-glass roles for buckets the library does not manage were accepted")
	}
}

// ---- the writer (A5)

// A record is written once: the policy requires If-None-Match on a put into
// records/, and not on the other prefixes. The legal hold stays: the put of a
// record whose tenant is held carries it.
func TestTheWriterMayOnlyAddRecords(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Presets["attested"] = auditpulumi.PresetStorage{Bucket: "acme-audit-attested", Create: true}
		a.Archive.AcknowledgeCompliance, a.Archive.DefaultRetentionDays = true, 30
	})
	if err != nil {
		t.Fatal(err)
	}
	var onRecords, elsewhere []map[string]any
	for _, s := range policy(t, rec, "audit-writer") {
		if s["Effect"] != "Allow" || !contains(strs(s["Action"]), "s3:PutObject") {
			continue
		}
		if strings.Contains(strings.Join(strs(s["Resource"]), ","), "/records/*") {
			onRecords = append(onRecords, s)
		} else {
			elsewhere = append(elsewhere, s)
		}
	}
	// One pair per bucket: the standard preset's, and the attested preset's (locked).
	if len(onRecords) != 2 || len(elsewhere) != 2 {
		t.Fatalf("%d put statements on records/ and %d elsewhere, want 2 and 2", len(onRecords), len(elsewhere))
	}
	locked := 0
	for _, s := range onRecords {
		if got := s["Condition"]; !reflect.DeepEqual(got, map[string]any{"StringEquals": map[string]any{"s3:if-none-match": "*"}}) {
			t.Errorf("records/ put condition = %v", got)
		}
		if len(strs(s["Resource"])) != 1 {
			t.Errorf("the conditional put covers more than records/: %v", s["Resource"])
		}
		// The legal hold the writer places on a held tenant's record, and retention, are still its.
		acts := strings.Join(strs(s["Action"]), ",")
		if strings.Contains(acts, "s3:PutObjectLegalHold") && strings.Contains(acts, "s3:PutObjectRetention") {
			locked++
		}
	}
	if locked != 1 {
		t.Errorf("%d records/ puts carry the retention and the legal hold, want the locked bucket's one", locked)
	}
	for _, s := range elsewhere {
		if s["Condition"] != nil {
			t.Errorf("a put outside records/ is conditional: %v", s)
		}
		for _, r := range strs(s["Resource"]) {
			if strings.Contains(r, "/records/") {
				t.Errorf("records/ is also put without the condition: %v", s)
			}
		}
	}
	// Nothing that deletes, bypasses or reshapes the lock is granted to any role.
	for _, role := range []string{"audit-writer", "audit-notary"} {
		for a := range grants(policy(t, rec, role)) {
			switch a {
			case "s3:DeleteObject", "s3:DeleteObjectVersion", "s3:BypassGovernanceRetention", "s3:PutBucketObjectLockConfiguration":
				t.Errorf("%s holds %s", role, a)
			}
		}
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---- the observe reader's trust (C)

func TestTheObserveReaderIsAssumedByOneRoleOnly(t *testing.T) {
	other := func(p string) string { return arnp + "iam::" + otherAccount + ":" + p }
	same := func(p string) string { return arnp + "iam::" + account + ":" + p }
	for name, c := range map[string]struct {
		principal, external string
		want                string // "" is accepted
	}{
		"another account's role, with the external id": {other("role/kernel/audit-observe"), "x1", ""},
		"another account's role, without it":           {other("role/kernel/audit-observe"), "", "ExternalID is required"},
		"another account's root":                       {other("root"), "x1", "account's root"},
		"a wildcard":                                   {other("role/*"), "x1", "wildcard"},
		"every principal":                              {"*", "x1", "wildcard"},
		"a session ARN":                                {arnp + "sts::" + otherAccount + ":assumed-role/r/s", "x1", "not the ARN of one IAM role or user"},
		"this account's role, no external id":          {same("role/kernel/audit-observe"), "", ""},
		"this account's root":                          {same("root"), "", "account's root"},
		"another account's user, with the external id": {other("user/ops"), "x1", ""},
	} {
		_, _, err := build(t, func(a *auditpulumi.Args) {
			a.AccountID = account
			a.Observe = &auditpulumi.ObserveArgs{TrustedPrincipalArn: pulumi.String(c.principal), ExternalID: c.external}
		})
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want an error with %q", name, err, c.want)
		}
	}
	// With no AccountID the account is looked up, and a principal from another one still needs the id.
	_, _, err := build(t, func(a *auditpulumi.Args) {
		a.Observe = &auditpulumi.ObserveArgs{TrustedPrincipalArn: pulumi.String(other("role/kernel/audit-observe"))}
	})
	if err == nil || !strings.Contains(err.Error(), "ExternalID is required") {
		t.Errorf("a looked-up account: %v", err)
	}
}

// ---- the scheduler's trust (C)

func TestTheNotarysSchedulerTrustNamesItsSchedule(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.AccountID, a.Region = account, "eu-west-1" })
	if err != nil {
		t.Fatal(err)
	}
	trust := prop(rec.one(t, "aws:iam/role:Role", "audit-scheduler"), "assumeRolePolicy").StringValue()
	sts := policyOf(t, trust)
	if len(sts) != 1 {
		t.Fatalf("trust: %s", trust)
	}
	cond := sts[0]["Condition"].(map[string]any)
	if cond["StringEquals"].(map[string]any)["aws:SourceAccount"] != account {
		t.Errorf("source account: %v", cond)
	}
	if got, want := cond["ArnEquals"].(map[string]any)["aws:SourceArn"], arnp+"scheduler:eu-west-1:"+account+":schedule/default/audit-notary"; got != want {
		t.Errorf("source ARN = %v, want %s", got, want)
	}
	sched := rec.one(t, "aws:scheduler/schedule:Schedule", "audit-notary")
	if prop(sched, "name").StringValue() != "audit-notary" {
		t.Errorf("the schedule is %v, the trust names audit-notary", prop(sched, "name"))
	}
}

// ---- the optional code signing configuration (B4)

func TestTheAuditFunctionsAreUnderTheCodeSigningConfigWhenOneIsGiven(t *testing.T) {
	const csc = "arn:aws:lambda:eu-west-1:111122223333:code-signing-config:csc-0123456789abcdef0"
	rec, _, err := build(t, func(a *auditpulumi.Args) { a.CodeSigningConfigArn = pulumi.String(csc) })
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audit-writer", "audit-notary"} {
		if got := prop(rec.one(t, "aws:lambda/function:Function", name), "codeSigningConfigArn"); !got.HasValue() || got.StringValue() != csc {
			t.Errorf("%s: codeSigningConfigArn = %v", name, got)
		}
	}
}
