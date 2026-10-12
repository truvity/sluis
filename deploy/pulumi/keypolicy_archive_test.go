package sluispulumi

import (
	"slices"
	"strings"
	"testing"
)

// keyReq is one KMS call as the key policy judges it: who, what, and the
// encryption context it carries (nil for none).
type keyReq struct {
	principal, action string
	ctx               map[string]string
}

// keyAllowed is the key policy's decision, for statements whose Allows name the
// account root (a principal of the account) and whose Denies name `*`: an Allow
// whose condition holds and no Deny whose condition holds. A condition key the
// request lacks fails a positive operator and satisfies a negated one, as IAM's.
func keyAllowed(t *testing.T, st []map[string]any, r keyReq) bool {
	t.Helper()
	values := func(key string) ([]string, bool) {
		switch {
		case key == "aws:PrincipalArn":
			return []string{r.principal}, true
		case key == "kms:EncryptionContextKeys":
			if len(r.ctx) == 0 {
				return nil, false
			}
			var ks []string
			for k := range r.ctx {
				ks = append(ks, k)
			}
			return ks, true
		case strings.HasPrefix(key, "kms:EncryptionContext:"):
			v, ok := r.ctx[strings.TrimPrefix(key, "kms:EncryptionContext:")]
			return []string{v}, ok
		}
		t.Fatalf("a condition key this test does not evaluate: %s", key)
		return nil, false
	}
	holds := func(cond map[string]any) bool {
		for op, v := range cond {
			for key, want := range v.(map[string]any) {
				wants := strs(want)
				have, present := values(key)
				switch op {
				case "ArnEquals", "StringEquals":
					if !present || !slices.Contains(wants, have[0]) {
						return false
					}
				case "ArnNotEquals", "StringNotEquals":
					if present && slices.Contains(wants, have[0]) {
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
	ok := false
	for _, s := range st {
		if !anyGlob(strs(s["Action"]), r.action) {
			continue
		}
		if c, has := s["Condition"].(map[string]any); has && !holds(c) {
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

// estateArchiveStatements are the statements an estate that shares one key with
// the backup archive writes by hand: the allow of the backup role under the
// archive context, the deny that reserves the purpose, and the deny that holds
// the role to it.
func estateArchiveStatements(instance, backup string) []map[string]any {
	crypto := []string{"kms:Encrypt", "kms:Decrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:CreateGrant"}
	return []map[string]any{
		{
			"Sid": "BackupArchive", "Effect": "Allow", "Principal": map[string]any{"AWS": arnPrefix + "iam::" + kpAccount + ":root"},
			"Action": []string{"kms:Decrypt", "kms:GenerateDataKey"}, "Resource": "*",
			"Condition": map[string]any{
				"ArnEquals": map[string]any{"aws:PrincipalArn": []string{backup}},
				"StringEquals": map[string]any{
					"kms:EncryptionContext:instance": instance, "kms:EncryptionContext:purpose": "archive",
				},
				"ForAllValues:StringEquals": map[string]any{"kms:EncryptionContextKeys": []string{"instance", "purpose"}},
			},
		},
		{
			"Sid": "ArchiveReserved", "Effect": "Deny", "Principal": map[string]any{"AWS": "*"}, "Resource": "*", "Action": crypto,
			"Condition": map[string]any{
				"StringEquals": map[string]any{"kms:EncryptionContext:purpose": "archive"},
				"ArnNotEquals": map[string]any{"aws:PrincipalArn": []string{backup}},
			},
		},
		{
			"Sid": "BackupArchiveOnly", "Effect": "Deny", "Principal": map[string]any{"AWS": "*"}, "Resource": "*", "Action": crypto,
			"Condition": map[string]any{
				"ArnEquals":       map[string]any{"aws:PrincipalArn": []string{backup}},
				"StringNotEquals": map[string]any{"kms:EncryptionContext:purpose": "archive"},
			},
		},
	}
}

func archiveOf(st []map[string]any) []map[string]any {
	var out []map[string]any
	for _, s := range st {
		if strings.HasPrefix(s["Sid"].(string), "SluisKeyArchive") {
			out = append(out, s)
		}
	}
	return out
}

func archiveRequests(principals []string) []keyReq {
	contexts := map[string]map[string]string{
		"none":          nil,
		"archive":       {"instance": kpInst, "purpose": "archive"},
		"other inst":    {"instance": "other", "purpose": "archive"},
		"extra key":     {"instance": kpInst, "purpose": "archive", "alg": "x"},
		"purpose only":  {"purpose": "archive"},
		"sign":          {"instance": kpInst, "purpose": "sign"},
		"other purpose": {"instance": kpInst, "purpose": "backup-x"},
	}
	var out []keyReq
	for _, p := range principals {
		for _, a := range []string{"kms:Decrypt", "kms:GenerateDataKey", "kms:Encrypt", "kms:ReEncrypt*", "kms:GenerateDataKeyWithoutPlaintext", "kms:CreateGrant"} {
			for _, c := range contexts {
				out = append(out, keyReq{principal: p, action: a, ctx: c})
			}
		}
	}
	return out
}

// The archive statements decide as the ones an estate writes by hand for the
// backup role: the same calls allowed, the same refused, for every principal,
// action and context.
func TestArchiveKeyStatementsDecideAsTheEstatesOwn(t *testing.T) {
	a := kpArgs(LayoutV5)
	a.BackupArchive = true
	lib := archiveOf(kpStatements(t, a))
	if got := kpSids(lib); !slices.Equal(got, []string{"SluisKeyArchive", "SluisKeyArchiveContextReserved", "SluisKeyArchiveRolesPurposeOnly"}) {
		t.Fatalf("archive statements %v", got)
	}
	hand := estateArchiveStatements(kpInst, kpBackup)
	allowedAny := 0
	for _, r := range archiveRequests([]string{kpBackup, kpRestore, kpIssuer, kpCF, kpAccountRole("someone")}) {
		got, want := keyAllowed(t, lib, r), keyAllowed(t, hand, r)
		if got != want {
			t.Errorf("%s %s %v: library %v, estate %v", r.principal, r.action, r.ctx, got, want)
		}
		if got {
			allowedAny++
		}
	}
	// Not vacuous: the backup role is let in under the archive context.
	if !keyAllowed(t, lib, keyReq{principal: kpBackup, action: "kms:GenerateDataKey", ctx: map[string]string{"instance": kpInst, "purpose": "archive"}}) ||
		keyAllowed(t, lib, keyReq{principal: kpIssuer, action: "kms:Decrypt", ctx: map[string]string{"instance": kpInst, "purpose": "archive"}}) {
		t.Error("the backup role is not admitted, or another role is")
	}
	if allowedAny == 0 {
		t.Error("nothing is allowed")
	}
}

// With the restore role it opens the archive's data keys and nothing else.
func TestArchiveKeyStatementsAdmitTheRestoreRoleToDecryptOnly(t *testing.T) {
	a := kpArgs(LayoutV5)
	a.BackupArchive, a.RestoreArchive = true, true
	lib := archiveOf(kpStatements(t, a))
	arch := map[string]string{"instance": kpInst, "purpose": "archive"}
	for _, c := range []struct {
		who, action string
		ctx         map[string]string
		want        bool
	}{
		{kpRestore, "kms:Decrypt", arch, true},
		{kpRestore, "kms:GenerateDataKey", arch, false},
		{kpRestore, "kms:Decrypt", map[string]string{"instance": kpInst, "purpose": "sign"}, false},
		{kpRestore, "kms:Decrypt", nil, false},
		{kpBackup, "kms:GenerateDataKey", arch, true},
		{kpBackup, "kms:Decrypt", map[string]string{"instance": kpInst, "purpose": "sign"}, false},
		{kpIssuer, "kms:Decrypt", arch, false},
	} {
		if got := keyAllowed(t, lib, keyReq{principal: c.who, action: c.action, ctx: c.ctx}); got != c.want {
			t.Errorf("%s %s %v: %v, want %v", c.who, c.action, c.ctx, got, c.want)
		}
	}
	// A role kept under a transition name is held to the same.
	a.Roles = slices.Clone(a.Roles)
	for i := range a.Roles {
		if a.Roles[i].Role.Name == RoleBackup {
			a.Roles[i].AlsoArns = []string{kpAccountRole("sluis-old-backup")}
		}
	}
	lib = archiveOf(kpStatements(t, a))
	if !keyAllowed(t, lib, keyReq{principal: kpAccountRole("sluis-old-backup"), action: "kms:Decrypt", ctx: arch}) {
		t.Error("the old backup role is not admitted")
	}
}

func kpAccountRole(name string) string {
	return arnPrefix + "iam::" + kpAccount + ":role/" + name
}

// Off by default, and refused without the role it is about.
func TestArchiveKeyStatementsAreOptIn(t *testing.T) {
	if got := archiveOf(kpStatements(t, kpArgs(LayoutV5))); len(got) != 0 {
		t.Errorf("archive statements without asking: %v", kpSids(got))
	}
	a := kpArgs(LayoutV5)
	a.RestoreArchive = true
	if _, err := SluisKeyPolicyStatements(a); err == nil {
		t.Error("RestoreArchive without BackupArchive")
	}
	a = kpArgs(LayoutV5)
	a.BackupArchive = true
	a.Roles = slices.DeleteFunc(slices.Clone(a.Roles), func(r KeyRole) bool { return r.Role.Name == RoleBackup })
	if _, err := SluisKeyPolicyStatements(a); err == nil {
		t.Error("BackupArchive without a backup role")
	}
}
