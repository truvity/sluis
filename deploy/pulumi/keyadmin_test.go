package sluispulumi_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// A key the library creates is not left to the default key policy, which hands
// the whole of kms:* to the account root and so to every IAM principal with an
// allow: the remote signing keys carry the control plane's allow and deny too.
func TestTheRemoteSigningKeysCarryAPolicyThatHoldsTheControlPlane(t *testing.T) {
	rec, _ := mustLambda(t, estate{mutate: noDomain})
	keys := rec.ofType("aws:kms/key:Key")
	if len(keys) != 2 {
		t.Fatalf("%d keys, want the two the library creates", len(keys))
	}
	for _, k := range keys {
		pol := prop(k, "policy")
		if !pol.HasValue() {
			t.Errorf("%s: the key is created with the default key policy (kms:* to the root)", k.Name)
			continue
		}
		byID := map[string]map[string]any{}
		for _, st := range statements(t, pol.StringValue()) {
			byID[st["Sid"].(string)] = st
		}
		root := byID["EnableIAMPolicies"]
		if root == nil {
			t.Fatalf("%s: no root statement: %v", k.Name, byID)
		}
		acts := strs(root["Action"])
		for _, a := range []string{"kms:Sign", "kms:Verify", "kms:DescribeKey"} {
			if !contains(acts, a) {
				t.Errorf("%s: the root does not enable %s", k.Name, a)
			}
		}
		for _, a := range append([]string{"kms:*"}, arp.KeyControlPlaneActions...) {
			if contains(acts, a) {
				t.Errorf("%s: the root delegates %s to IAM", k.Name, a)
			}
		}
		deny := byID["SluisKeyControlPlaneAdminsOnly"]
		if deny == nil || deny["Effect"] != "Deny" || !reflect.DeepEqual(strs(deny["Action"]), arp.KeyControlPlaneActions) {
			t.Errorf("%s: the control plane's deny: %v", k.Name, deny)
		}
		if got := deny["Condition"].(map[string]any)["ArnNotLike"]; !reflect.DeepEqual(got, map[string]any{"aws:PrincipalArn": []any{testKeyAdmin}}) {
			t.Errorf("%s: deny but for %v", k.Name, got)
		}
	}
}

// A key made with no administrator could not be changed again, and a key made
// where a role may hold it without a wildcard is not what a pattern says.
func TestTheLibraryCreatesNoKeyWithoutAdministrators(t *testing.T) {
	for name, e := range map[string]estate{
		"remote signing": {noKeyAdmins: true, mutate: noDomain},
		"wrapped signing": {noKeyAdmins: true, mutate: func(a *arp.LambdaArgs) {
			noDomain(a)
			a.WrappedSigning = &arp.WrappedSigningArgs{}
		}},
	} {
		if _, _, err := buildLambda(t, e); err == nil || !strings.Contains(err.Error(), "KeyAdmins") {
			t.Errorf("%s: %v, want a refusal naming KeyAdmins", name, err)
		}
	}
	// A wildcard or the account's root is no administrator.
	for _, bad := range []string{arnp + "iam::" + account + ":root", "*", arnp + "iam::" + account + ":role/*"} {
		_, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { noDomain(a); a.KeyAdmins = []string{bad} }})
		if err == nil {
			t.Errorf("%q administers a key", bad)
		}
	}
	// Where the estate supplies the keys, the keys' policies are its own, and KeyAdmins
	// would do nothing: it is refused, so that nobody believes it does.
	_, _, err := buildLambda(t, estate{noKeyAdmins: true, mutate: func(a *arp.LambdaArgs) {
		noDomain(a)
		a.Keys = suppliedKeys()
		a.KeyAdmins = []string{testKeyAdmin}
	}})
	if err == nil || !strings.Contains(err.Error(), "KeyAdmins") {
		t.Errorf("KeyAdmins with Keys: %v", err)
	}
	if _, _, err := buildLambda(t, estate{noKeyAdmins: true, mutate: func(a *arp.LambdaArgs) { noDomain(a); a.Keys = suppliedKeys() }}); err != nil {
		t.Errorf("supplied keys need no administrators of the library's: %v", err)
	}
	// An existing wrapped key's policy is the estate's too.
	_, _, err = buildLambda(t, estate{noKeyAdmins: true, mutate: func(a *arp.LambdaArgs) {
		noDomain(a)
		a.WrappedSigning = &arp.WrappedSigningArgs{KeyArn: pulumi.String(arnp + "kms:" + region + ":" + account + ":key/application")}
	}})
	if err != nil {
		t.Errorf("a supplied wrapped key needs no administrators: %v", err)
	}
}

// The restore identity is the age key that opens every backup: a SecureString of
// the installation's key, never the AWS-managed one, and the backup and restore
// functions' plans are refused without it.
func TestBackupAndRestoreNeedTheInstallationsKey(t *testing.T) {
	if _, _, err := buildBackup(t, func(a *arp.BackupArgs) { a.ParameterKeyArn = "" }); err == nil || !strings.Contains(err.Error(), "ParameterKeyArn") {
		t.Errorf("backup: %v", err)
	}
	if _, _, err := buildRestore(t, func(a *arp.RestoreArgs) { a.ParameterKeyArn = "" }); err == nil || !strings.Contains(err.Error(), "ParameterKeyArn") {
		t.Errorf("restore: %v", err)
	}
	rec, _ := mustBackup(t, nil)
	p := rec.one(t, "aws:ssm/parameter:Parameter", "staging-backup-restore-identity")
	if got := prop(p, "keyId"); !got.HasValue() || got.StringValue() != paramKey {
		t.Errorf("the restore identity is encrypted with %v, want %s", got, paramKey)
	}
}

// ---- the optional code signing configuration (audit 2026-10-18, B4)

const csc = "arn:aws:lambda:eu-central-1:111122223333:code-signing-config:csc-0123456789abcdef0"

func TestEveryFunctionIsPutUnderTheCodeSigningConfigWhenOneIsGiven(t *testing.T) {
	rec, _ := mustLambda(t, estateWith(func(a *arp.LambdaArgs) { noDomain(a); a.CodeSigningConfigArn = pulumi.String(csc) }))
	fns := rec.ofType(fnType)
	if len(fns) < 2 {
		t.Fatalf("%d functions", len(fns))
	}
	for _, f := range fns {
		if got := prop(f, "codeSigningConfigArn"); !got.HasValue() || got.StringValue() != csc {
			t.Errorf("%s: codeSigningConfigArn = %v", f.Name, got)
		}
	}
	bk, _ := mustBackup(t, func(a *arp.BackupArgs) { a.CodeSigningConfigArn = pulumi.String(csc) })
	for _, f := range bk.ofType(fnType) {
		if got := prop(f, "codeSigningConfigArn"); !got.HasValue() || got.StringValue() != csc {
			t.Errorf("backup %s: codeSigningConfigArn = %v", f.Name, got)
		}
	}
	// Left out, nothing changes.
	rec, _ = mustLambda(t, estateWith(noDomain))
	for _, f := range rec.ofType(fnType) {
		if prop(f, "codeSigningConfigArn").HasValue() {
			t.Errorf("%s: a code signing configuration nobody asked for", f.Name)
		}
	}
}
