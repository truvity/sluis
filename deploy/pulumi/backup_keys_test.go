package sluispulumi

import (
	"strings"
	"testing"
)

const identityArn = "arn:aws:ssm:r1:111122223333:parameter/sluis/i1/internal/backup/restore-identity"

func ssmViaKey(action, key, param string) iamReq {
	return iamReq{action: action, resource: key, keys: map[string][]string{
		"kms:ViaService":                      {"ssm.r1.amazonaws.com"},
		"kms:EncryptionContext:PARAMETER_ARN": {param},
	}}
}

func TestTheRestoreIdentityLivesInTheBackupNamespace(t *testing.T) {
	if got := RestoreIdentityParameterName("i1"); got != "/sluis/i1/internal/backup/restore-identity" {
		t.Errorf("the parameter is %s", got)
	}
	if got := restoreIdentityArn("r1", "111122223333", ModuleSet{Instance: "i1"}); got != identityArn {
		t.Errorf("the ARN is %s", got)
	}
}

func TestTheBackupRoleCannotReadTheRestoreIdentity(t *testing.T) {
	st, err := BackupRoleStatements(testModuleEnv(), testArchive())
	if err != nil {
		t.Fatal(err)
	}
	// Not vacuous: the role does read its own namespace, and decrypts those.
	other := "arn:aws:ssm:r1:111122223333:parameter/sluis/i1/internal/backup/other"
	if !allowed(t, st, iamReq{action: "ssm:GetParameter", resource: other}) {
		t.Fatal("the backup role cannot read its own namespace: the test proves nothing")
	}
	if !allowed(t, st, ssmViaKey("kms:Decrypt", "pkey", other)) {
		t.Fatal("the backup role cannot decrypt its own parameters: the test proves nothing")
	}
	for _, a := range []string{"ssm:GetParameter", "ssm:GetParameters", "ssm:GetParameterHistory", "ssm:PutParameter", "ssm:DeleteParameter"} {
		if allowed(t, st, iamReq{action: a, resource: identityArn}) {
			t.Errorf("the backup role can %s the restore identity", a)
		}
	}
	// GetParametersByPath is authorised on the path, so the value is held back at
	// the key: the parameter's own encryption context.
	if allowed(t, st, ssmViaKey("kms:Decrypt", "pkey", identityArn)) {
		t.Error("the backup role can decrypt the restore identity: GetParametersByPath on its namespace would return it")
	}
}

func TestTheRestoreRoleReadsTheRestoreIdentity(t *testing.T) {
	st, err := RestoreRoleStatements(testModuleEnv(), testArchive(), []string{"alias:admin"})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed(t, st, iamReq{action: "ssm:GetParameter", resource: identityArn}) {
		t.Error("the restore role cannot read the restore identity")
	}
	if !allowed(t, st, ssmViaKey("kms:Decrypt", "pkey", identityArn)) {
		t.Error("the restore role cannot decrypt the restore identity through SSM")
	}
	if allowed(t, st, iamReq{action: "kms:Decrypt", resource: "pkey"}) {
		t.Error("the restore role decrypts the parameter key outside SSM")
	}
	var sids []string
	for _, s := range st {
		sids = append(sids, s["Sid"].(string))
	}
	if !strings.Contains(strings.Join(sids, " "), "SluisRestoreIdentity") {
		t.Errorf("no statement names the identity: %v", sids)
	}
}

func TestNoOtherRoleHostsTheBackupNamespace(t *testing.T) {
	// The issuer, and every module function, are given no statement that reaches
	// the backup module's parameters.
	env := testModuleEnv()
	for _, r := range env.Roles {
		if r.Name == RoleBackup || r.Name == RoleRestore {
			continue
		}
		st, err := ModuleRoleStatements(env, r)
		if err != nil {
			t.Fatal(err)
		}
		if allowed(t, st, iamReq{action: "ssm:GetParameter", resource: identityArn}) {
			t.Errorf("the %s role can read the restore identity", r.Name)
		}
	}
}

func TestExtraRecipientsAreValidated(t *testing.T) {
	_, good, err := AgeKeypairFromSeed(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseBackupRecipients([]string{good, good})
	if err != nil || len(got) != 1 || got[0] != good {
		t.Fatalf("%v, %v", got, err)
	}
	if got, err := ParseBackupRecipients(nil); err != nil || len(got) != 0 {
		t.Errorf("none: %v, %v", got, err)
	}
	for name, bad := range map[string]string{
		"empty":     "",
		"garbage":   "not-a-key",
		"identity":  "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ",
		"checksum":  good[:len(good)-1] + "q",
		"ssh":       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA",
		"uppercase": strings.ToUpper(good) + "x",
	} {
		if _, err := ParseBackupRecipients([]string{good, bad}); err == nil {
			t.Errorf("%s: %q accepted", name, bad)
		}
	}
}

func TestTheStandInRecipientIsValid(t *testing.T) {
	if _, err := ParseBackupRecipients([]string{standInRecipient()}); err != nil {
		t.Fatal(err)
	}
}
