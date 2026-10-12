package sluispulumi

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func archiveOf(st []map[string]any) []map[string]any {
	var out []map[string]any
	for _, s := range st {
		if strings.HasPrefix(s["Sid"].(string), "SluisKeyArchive") {
			out = append(out, s)
		}
	}
	return out
}

// BackupArchive and RestoreArchive are deprecated: an archive is age-encrypted
// and uses no KMS key, so they are accepted, in any combination, and add no
// statement.
func TestTheDeprecatedArchiveKeyOptionsAreAcceptedAndAddNothing(t *testing.T) {
	base := kpSids(kpStatements(t, kpArgs(LayoutV5)))
	for _, c := range []struct{ backup, restore bool }{{true, false}, {true, true}, {false, true}} {
		a := kpArgs(LayoutV5)
		a.BackupArchive, a.RestoreArchive = c.backup, c.restore
		st, err := SluisKeyPolicyStatements(a)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		if got := archiveOf(st); len(got) != 0 {
			t.Errorf("%+v: archive statements %v", c, kpSids(got))
		}
		if got := kpSids(st); !slices.Equal(got, base) {
			t.Errorf("%+v changed the statements:\n got %v\nwant %v", c, got, base)
		}
	}
	a := kpArgs(LayoutV5)
	a.BackupArchive = true
	a.Roles = slices.DeleteFunc(slices.Clone(a.Roles), func(r KeyRole) bool { return r.Role.Name == RoleBackup })
	if _, err := SluisKeyPolicyStatements(a); err != nil {
		t.Errorf("BackupArchive without a backup role is refused: %v", err)
	}
}

// The key's own policy refuses the backup role the decryption of the restore
// identity through SSM, and no other role.
func TestTheKeyPolicyDeniesTheBackupRoleTheRestoreIdentity(t *testing.T) {
	var deny map[string]any
	for _, s := range kpStatements(t, kpArgs(LayoutV5)) {
		if s["Sid"] == "SluisKeyDenyRestoreIdentity" {
			deny = s
		}
	}
	if deny == nil {
		t.Fatal("no deny of the restore identity")
	}
	cond := deny["Condition"].(map[string]any)
	who := cond["ArnEquals"].(map[string]any)["aws:PrincipalArn"]
	if !reflect.DeepEqual(who, []string{kpBackup}) {
		t.Errorf("the deny is about %v, want the backup role %s", who, kpBackup)
	}
	eq := cond["StringEquals"].(map[string]any)
	want := arnPrefix + "ssm:" + kpRegion + ":" + kpAccount + ":parameter/sluis/" + kpInst + "/internal/backup/restore-identity"
	if eq["kms:EncryptionContext:PARAMETER_ARN"] != want || deny["Effect"] != "Deny" || !reflect.DeepEqual(deny["Action"], []string{"kms:Decrypt"}) {
		t.Errorf("the deny is %v", deny)
	}
}
