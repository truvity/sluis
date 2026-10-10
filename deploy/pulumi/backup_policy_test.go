package sluispulumi

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// iamReq is one request as IAM judges it: the action, the resource and the
// condition keys the request carries (each with its values).
type iamReq struct {
	action, resource string
	keys             map[string][]string
}

// globMatch is IAM's wildcard: * is any run of characters, ? one character.
func globMatch(pattern, s string) bool {
	re := strings.ReplaceAll(strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, `.*`), `\?`, `.`)
	ok, _ := regexp.MatchString("^"+re+"$", s)
	return ok
}

func anyGlob(patterns []string, s string) bool {
	for _, p := range patterns {
		if globMatch(p, s) {
			return true
		}
	}
	return false
}

// conditionsHold evaluates the operators the library's statements use.
func conditionsHold(t *testing.T, cond map[string]any, keys map[string][]string) bool {
	t.Helper()
	for op, v := range cond {
		for key, want := range v.(map[string]any) {
			wants, have := strs(want), keys[key]
			switch op {
			case "StringEquals":
				if len(have) != 1 || !slices.Contains(wants, have[0]) {
					return false
				}
			case "StringLike":
				if len(have) == 0 {
					return false
				}
				ok := false
				for _, h := range have {
					ok = ok || anyGlob(wants, h)
				}
				if !ok {
					return false
				}
			case "ForAnyValue:StringEquals":
				ok := false
				for _, h := range have {
					ok = ok || slices.Contains(wants, h)
				}
				if !ok {
					return false
				}
			case "ForAllValues:StringEquals":
				for _, h := range have {
					if !slices.Contains(wants, h) {
						return false
					}
				}
			default:
				t.Fatalf("a condition operator this test does not evaluate: %s", op)
			}
		}
	}
	return true
}

// allowed is IAM's decision for one request: allowed by some statement and
// denied by none.
func allowed(t *testing.T, st []map[string]any, r iamReq) bool {
	t.Helper()
	ok := false
	for _, s := range st {
		if !anyGlob(strs(s["Action"]), r.action) || !anyGlob(strs(s["Resource"]), r.resource) {
			continue
		}
		if c, has := s["Condition"].(map[string]any); has && !conditionsHold(t, c, r.keys) {
			continue
		}
		switch s["Effect"] {
		case "Deny":
			return false
		case "Allow":
			ok = true
		}
	}
	return ok
}

func ddb(action, table, partition string) iamReq {
	return iamReq{action: action, resource: table, keys: map[string][]string{"dynamodb:LeadingKeys": {partition}}}
}

func kmsCtx(action, key string, ctx map[string]string) iamReq {
	keys := map[string][]string{}
	var names []string
	for k, v := range ctx {
		keys["kms:EncryptionContext:"+k] = []string{v}
		names = append(names, k)
	}
	keys["kms:EncryptionContextKeys"] = names
	return iamReq{action: action, resource: key, keys: keys}
}

func testArchive() ArchiveGrant {
	return ArchiveGrant{BucketArn: "arch", Prefix: "pre/", Instance: "i1", KeyArn: "akey", SSEKeyArn: "sse", Versioned: true}
}

func TestBackupRoleReadsEveryModuleAndWritesOnlyItsOwnTable(t *testing.T) {
	env := testModuleEnv()
	st, err := BackupRoleStatements(env, testArchive())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range Modules() {
		table := env.TableArns[m]
		for _, a := range []string{"dynamodb:GetItem", "dynamodb:Query", "dynamodb:Scan", "dynamodb:DescribeTable"} {
			if !allowed(t, st, ddb(a, table, "any")) {
				t.Errorf("the backup role cannot %s on the %s table", a, m)
			}
		}
		wantWrite := m == ModuleBackup
		for _, a := range []string{"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem"} {
			if got := allowed(t, st, ddb(a, table, "lease")); got != wantWrite {
				t.Errorf("the backup role: %s on the %s table = %v, want %v", a, m, got, wantWrite)
			}
		}
		// Every module's SSM, internal and external, is read and not written (the backup
		// module's own aside).
		for _, kind := range []string{"internal", "external"} {
			arn := arnPrefix + "ssm:r1:000000000000:parameter/sluis/i1/" + kind + "/" + string(m) + "/x"
			if !allowed(t, st, iamReq{action: "ssm:GetParameter", resource: arn}) {
				t.Errorf("the backup role cannot read %s/%s", kind, m)
			}
			if got := allowed(t, st, iamReq{action: "ssm:PutParameter", resource: arn}); got != (m == ModuleBackup) {
				t.Errorf("the backup role: ssm:PutParameter on %s/%s = %v", kind, m, got)
			}
		}
		// The blob prefix of each module is read; written only for the backup module.
		if !allowed(t, st, iamReq{action: "s3:GetObject", resource: "bkt/" + string(m) + "/x"}) {
			t.Errorf("the backup role cannot read the %s blobs", m)
		}
		if got := allowed(t, st, iamReq{action: "s3:PutObject", resource: "bkt/" + string(m) + "/x"}); got != (m == ModuleBackup) {
			t.Errorf("the backup role: s3:PutObject on the %s blobs = %v", m, got)
		}
	}
}

func TestBackupRoleReadsTheMaintenanceFlagAndNeverWritesIt(t *testing.T) {
	env := testModuleEnv()
	st, err := BackupRoleStatements(env, testArchive())
	if err != nil {
		t.Fatal(err)
	}
	if !allowed(t, st, ddb("dynamodb:GetItem", env.TableArns[ModuleBackup], MaintenancePartition)) {
		t.Error("the backup role cannot read the flag in the backup table")
	}
	for _, m := range Modules() {
		for _, a := range ddbMaintenanceDenied {
			if allowed(t, st, ddb(a, env.TableArns[m], MaintenancePartition)) {
				t.Errorf("the backup role can %s the maintenance flag in the %s table", a, m)
			}
		}
	}
}

func TestBackupRoleUsesTheArchivePathAndNothingElse(t *testing.T) {
	st, err := BackupRoleStatements(testModuleEnv(), testArchive())
	if err != nil {
		t.Fatal(err)
	}
	in, out := "arch/pre/backup/i1/20261010T020000Z-3fa9c1/manifest", "arch/pre/backup/other/x"
	for _, a := range []string{"s3:PutObject", "s3:GetObject", "s3:DeleteObject", "s3:GetObjectVersion"} {
		if !allowed(t, st, iamReq{action: a, resource: in}) {
			t.Errorf("the backup role cannot %s in its archive path", a)
		}
		if allowed(t, st, iamReq{action: a, resource: out}) {
			t.Errorf("the backup role can %s outside its archive path", a)
		}
	}
	list := func(prefix string, a string) bool {
		return allowed(t, st, iamReq{action: a, resource: "arch", keys: map[string][]string{"s3:prefix": {prefix}}})
	}
	for _, a := range []string{"s3:ListBucket", "s3:ListBucketVersions"} {
		if !list("pre/backup/i1/", a) || list("pre/backup/other/", a) || list("", a) {
			t.Errorf("%s is not bound to the archive path", a)
		}
	}
	if allowed(t, st, iamReq{action: "s3:PutBucketPolicy", resource: "arch"}) || allowed(t, st, iamReq{action: "s3:DeleteObjectVersion", resource: in}) ||
		allowed(t, st, iamReq{action: "s3:BypassGovernanceRetention", resource: in}) {
		t.Error("the backup role changes the bucket, deletes a version or bypasses the lock")
	}
}

func TestBackupRoleUsesTheArchiveKeyUnderItsContext(t *testing.T) {
	st, err := BackupRoleStatements(testModuleEnv(), testArchive())
	if err != nil {
		t.Fatal(err)
	}
	ctx := map[string]string{"instance": "i1", "purpose": ArchivePurpose}
	for _, a := range []string{"kms:Decrypt", "kms:GenerateDataKey"} {
		if !allowed(t, st, kmsCtx(a, "akey", ctx)) {
			t.Errorf("the backup role cannot %s with the archive key", a)
		}
		for name, bad := range map[string]map[string]string{
			"other instance": {"instance": "i2", "purpose": ArchivePurpose},
			"other purpose":  {"instance": "i1", "purpose": "sign"},
			"extra key":      {"instance": "i1", "purpose": ArchivePurpose, "alg": "x"},
			"none":           {},
		} {
			if allowed(t, st, kmsCtx(a, "akey", bad)) {
				t.Errorf("the backup role can %s with the archive key under %s", a, name)
			}
		}
	}
	if allowed(t, st, kmsCtx("kms:Encrypt", "akey", ctx)) {
		t.Error("the backup role can kms:Encrypt with the archive key: it seals a data key with GenerateDataKey")
	}
	via := func(a string) bool {
		return allowed(t, st, iamReq{action: a, resource: "sse", keys: map[string][]string{"kms:ViaService": {"s3.r1.amazonaws.com"}}})
	}
	if !via("kms:GenerateDataKey") || !via("kms:Decrypt") {
		t.Error("the bucket's own key is not usable through S3")
	}
	if allowed(t, st, iamReq{action: "kms:Decrypt", resource: "sse"}) {
		t.Error("the bucket's own key is usable outside S3")
	}
}

func TestAnUnversionedArchiveHasNoVersionActions(t *testing.T) {
	g := testArchive()
	g.Versioned = false
	st, err := BackupRoleStatements(testModuleEnv(), g)
	if err != nil {
		t.Fatal(err)
	}
	if allowed(t, st, iamReq{action: "s3:GetObjectVersion", resource: "arch/pre/backup/i1/x"}) ||
		allowed(t, st, iamReq{action: "s3:ListBucketVersions", resource: "arch", keys: map[string][]string{"s3:prefix": {"pre/backup/i1/"}}}) {
		t.Error("an unversioned bucket is granted version actions")
	}
}

func TestMinterRefsReachTheBackupRoleAsReadOnly(t *testing.T) {
	env := testModuleEnv()
	env.MinterRefs = []string{"internal/cloudflare/acct/minter"}
	st, err := BackupRoleStatements(env, testArchive())
	if err != nil {
		t.Fatal(err)
	}
	// The backup role reads all of cloudflare already: the minter is not a second grant.
	arn := arnPrefix + "ssm:r1:000000000000:parameter/sluis/i1/internal/cloudflare/acct/minter"
	if !allowed(t, st, iamReq{action: "ssm:GetParameter", resource: arn}) || allowed(t, st, iamReq{action: "ssm:PutParameter", resource: arn}) {
		t.Error("the minter parameter is not read-only for the backup role")
	}
}

func TestArchiveGrantRefusesWhatItCannotNameAPathWith(t *testing.T) {
	for name, g := range map[string]ArchiveGrant{
		"no bucket":     {Instance: "i", KeyArn: "k"},
		"no instance":   {BucketArn: "b", KeyArn: "k"},
		"no key":        {BucketArn: "b", Instance: "i"},
		"slash first":   {BucketArn: "b", Instance: "i", KeyArn: "k", Prefix: "/p/"},
		"no slash last": {BucketArn: "b", Instance: "i", KeyArn: "k", Prefix: "p"},
	} {
		if _, err := BackupRoleStatements(testModuleEnv(), g); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheCrossAccountBucketPolicyNamesTheRolesAndTheirPathOnly(t *testing.T) {
	st, err := ArchiveBucketPolicyStatements(testArchive(), "bk-role", "rs-role")
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 4 {
		t.Fatalf("want objects and listing for two roles, got %d statements", len(st))
	}
	seen := map[string]bool{}
	for _, s := range st {
		p := s["Principal"].(map[string]any)["AWS"].(string)
		seen[p] = true
		if s["Effect"] != "Allow" {
			t.Errorf("%v is not an Allow", s["Sid"])
		}
		if p == "rs-role" && (slices.Contains(strs(s["Action"]), "s3:PutObject") || slices.Contains(strs(s["Action"]), "s3:DeleteObject")) {
			t.Errorf("the restore role is granted a write: %v", s["Action"])
		}
	}
	if !seen["bk-role"] || !seen["rs-role"] || len(seen) != 2 {
		t.Errorf("principals: %v", seen)
	}
	if only, _ := ArchiveBucketPolicyStatements(testArchive(), "bk-role", ""); len(only) != 2 {
		t.Errorf("without a restore role: %d statements", len(only))
	}
	if _, err := ArchiveBucketPolicyStatements(testArchive(), "", ""); err == nil {
		t.Error("no backup role accepted")
	}
}
