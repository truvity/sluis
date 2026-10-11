package sluispulumi

import (
	"testing"

	"github.com/truvity/sluis/blobprefix"
)

func listReq(bucket, prefix string) iamReq {
	return iamReq{action: s3ListBucket, resource: bucket, keys: map[string][]string{"s3:prefix": {prefix}}}
}

// The policy covers every blob prefix the code uses. The prefixes come from
// package blobprefix, which the code takes them from too (a test there fails on
// a literal anywhere else), so a prefix added there that no role is granted
// fails here, and a prefix at the root of the bucket, where the reports once
// sat, is one that no module role may touch.
func TestPolicyCoversEveryBlobPrefixTheCodeUses(t *testing.T) {
	env := testModuleEnv()
	roles := map[string]Role{}
	for _, r := range env.Roles {
		roles[r.Name] = r
	}
	// who may do what: the module's host reads and writes; the backup role reads
	// every module's blobs, and the restore role writes them back.
	type need struct {
		role    string
		actions []string
	}
	rw := []string{s3GetObject, s3PutObject, s3DeleteObject}
	for module, prefixes := range blobprefix.Used() {
		var host string
		for _, r := range env.Roles {
			if r.hosts(Module(module)) {
				host = r.Name
			}
		}
		if host == "" {
			t.Fatalf("no role hosts %s, which keeps blobs", module)
		}
		needs := []need{{host, rw}, {RoleBackup, []string{s3GetObject}}, {RoleRestore, rw}}
		for _, prefix := range prefixes {
			for _, n := range needs {
				st, err := ModuleRoleStatements(env, roles[n.role])
				if err != nil {
					t.Fatal(err)
				}
				object := env.BucketArn + "/" + prefix + "some-target"
				for _, a := range n.actions {
					if !allowed(t, st, iamReq{action: a, resource: object}) {
						t.Errorf("role %s is not allowed %s on %s", n.role, a, object)
					}
				}
				if !allowed(t, st, listReq(env.BucketArn, prefix)) {
					t.Errorf("role %s is not allowed to list %s", n.role, prefix)
				}
			}
		}
	}
}

// The grant is the module's prefix and not the whole bucket, and a prefix at
// the root of the bucket is outside every role's grant.
func TestPolicyGrantsNoRootOfTheBucket(t *testing.T) {
	env := testModuleEnv()
	for _, r := range env.Roles {
		st, err := ModuleRoleStatements(env, r)
		if err != nil {
			t.Fatal(err)
		}
		for _, old := range []string{blobprefix.LegacyGitHubReports, blobprefix.LegacySlackReports, blobprefix.LegacyCache} {
			if allowed(t, st, iamReq{action: s3GetObject, resource: env.BucketArn + "/" + old + "x"}) ||
				allowed(t, st, listReq(env.BucketArn, old)) {
				t.Errorf("role %s is granted the root-level prefix %s", r.Name, old)
			}
		}
	}
}
