package sluispulumi

import (
	"encoding/json"
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
	base := kpSids(kpStatements(t, kpArgs()))
	for _, c := range []struct{ backup, restore bool }{{true, false}, {true, true}, {false, true}} {
		a := kpArgs()
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
	a := kpArgs()
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
	for _, s := range kpStatements(t, kpArgs()) {
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

// The deployer reads the restore identity back (the provider decrypts a
// SecureString after create and on refresh): the admins are granted it, the
// backup role stays denied, the restore role keeps its own grant, and nobody
// else gains the parameter.
func TestAdminsReadTheRestoreIdentityAndNoOneElseGainsIt(t *testing.T) {
	ident := arnPrefix + "ssm:" + kpRegion + ":" + kpAccount + ":parameter/sluis/" + kpInst + "/internal/backup/restore-identity"
	holds := func(s map[string]any) bool {
		raw, _ := json.Marshal(s["Condition"])
		return strings.Contains(string(raw), ident)
	}
	var granted []string
	for _, s := range kpStatements(t, kpArgs()) {
		if s["Effect"] == "Allow" && holds(s) {
			granted = append(granted, s["Sid"].(string))
		}
	}
	if !reflect.DeepEqual(granted, []string{"SluisKeyAdminReads"}) {
		t.Fatalf("statements that name the restore identity explicitly: %v", granted)
	}
	reads := kpBySid(t, kpStatements(t, kpArgs()), "SluisKeyAdminReads")
	if !reflect.DeepEqual(reads["Action"], []string{"kms:Decrypt"}) {
		t.Errorf("admin reads actions: %v", reads["Action"])
	}
	c := reads["Condition"].(map[string]any)
	if !reflect.DeepEqual(c["ArnLike"], map[string]any{"aws:PrincipalArn": []string{kpAdmin}}) ||
		c["StringEquals"].(map[string]any)["kms:ViaService"] != "ssm."+kpRegion+".amazonaws.com" {
		t.Errorf("admin reads condition: %v", c)
	}
	// The deny is still about the backup role alone, so it never reaches an admin.
	for _, s := range kpStatements(t, kpArgs()) {
		if s["Sid"] == "SluisKeyDenyRestoreIdentity" {
			who := s["Condition"].(map[string]any)["ArnEquals"].(map[string]any)["aws:PrincipalArn"]
			if !reflect.DeepEqual(who, []string{kpBackup}) {
				t.Errorf("deny principals: %v", who)
			}
		}
	}
	// Without a backup role there is no identity parameter, so no grant for it.
	a := kpArgs()
	a.Roles = slices.DeleteFunc(slices.Clone(a.Roles), func(r KeyRole) bool { return r.Role.Name == RoleBackup })
	for _, s := range kpStatements(t, a) {
		if holds(s) {
			t.Errorf("%v names the restore identity without a backup role", s["Sid"])
		}
	}
}
