package sluispulumi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const (
	kpAccount = "000000000000"
	kpRegion  = "region-1"
	kpInst    = "inst1"
)

var (
	kpIssuer  = arnPrefix + "iam::" + kpAccount + ":role/fn-issuer"
	kpCF      = arnPrefix + "iam::" + kpAccount + ":role/fn-cloudflare"
	kpBackup  = arnPrefix + "iam::" + kpAccount + ":role/fn-backup"
	kpRestore = arnPrefix + "iam::" + kpAccount + ":role/fn-restore"
	kpReader1 = arnPrefix + "iam::" + kpAccount + ":role/cluster-b-reader"
	kpReader2 = arnPrefix + "iam::" + kpAccount + ":role/cluster-a-reader"
	kpAdmin   = arnPrefix + "iam::" + kpAccount + ":role/aws-reserved/sso.example/*Admin_*"
	kpGlass   = arnPrefix + "iam::" + kpAccount + ":role/aws-reserved/sso.example/*Glass_*"
	kpRoot    = arnPrefix + "iam::" + kpAccount + ":root"
)

func kpArgs(l Layout) KeyPolicyArgs {
	return KeyPolicyArgs{
		Region: kpRegion, Account: kpAccount, Modules: ModuleSet{Instance: kpInst}, Layout: l,
		Roles: []KeyRole{
			{Role: Role{Name: RoleIssuer, Hosts: []Module{ModuleOIDC, ModuleGitHub, ModuleSlack, ModuleGoogle}}, Arn: kpIssuer,
				MinterRefs: []string{"internal/cloudflare/main/minter"}},
			{Role: Role{Name: RoleCloudflare, Hosts: []Module{ModuleCloudflare}}, Arn: kpCF},
			{Role: Role{Name: RoleBackup, Hosts: []Module{ModuleBackup}}, Arn: kpBackup},
			{Role: Role{Name: RoleRestore, Hosts: []Module{ModuleBackup}}, Arn: kpRestore},
		},
		ExternalReaders: []string{kpReader1, kpReader2},
		Admins:          []string{kpAdmin},
		Breakglass:      []string{kpGlass},
	}
}

func kpStatements(t *testing.T, a KeyPolicyArgs) []map[string]any {
	t.Helper()
	st, err := SluisKeyPolicyStatements(a)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func kpBySid(t *testing.T, st []map[string]any, sid string) map[string]any {
	t.Helper()
	for _, s := range st {
		if s["Sid"] == sid {
			return s
		}
	}
	t.Fatalf("no statement %s", sid)
	return nil
}

func kpSids(st []map[string]any) []string {
	var out []string
	for _, s := range st {
		out = append(out, s["Sid"].(string))
	}
	return out
}

func TestSluisKeyPolicyStatementList(t *testing.T) {
	want := []string{
		"SluisKeyRoleBackup", "SluisKeyReadAllBackup",
		"SluisKeyRoleCloudflare",
		"SluisKeyRoleIssuer", "SluisKeyMinterIssuer",
		"SluisKeyRoleRestore", "SluisKeyWriteAllRestore",
		"SluisKeyExternalReaders", "SluisKeyAdminSeeds", "SluisKeyAdminReads", "SluisKeyBreakglass",
		"SluisKeySign", "SluisKeySignContextReserved", "SluisKeySignDirectPurposeOnly", "SluisKeySignDirectContextKeysOnly",
	}
	got := kpSids(kpStatements(t, kpArgs(LayoutV5)))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("statements\n got %v\nwant %v", got, want)
	}
	a := kpArgs(LayoutV5)
	a.DenyOtherActions = true
	got = kpSids(kpStatements(t, a))
	if got[len(got)-1] != "SluisKeyRolesNothingElse" {
		t.Errorf("DenyOtherActions: last = %s", got[len(got)-1])
	}
}

func TestSluisKeyPolicySidsAreUnique(t *testing.T) {
	for _, l := range []Layout{LayoutV4, LayoutV5, LayoutV4V5} {
		a := kpArgs(l)
		a.DenyOtherActions = true
		seen := map[string]bool{}
		for _, sid := range kpSids(kpStatements(t, a)) {
			if seen[sid] || !strings.HasPrefix(sid, "SluisKey") {
				t.Errorf("%s: Sid %q repeated or outside the SluisKey prefix", l, sid)
			}
			seen[sid] = true
		}
	}
	// Two roles whose names camel-case to one Sid are refused, not merged.
	a := kpArgs(LayoutV5)
	a.Roles = append(a.Roles, KeyRole{Role: Role{Name: "backup", Hosts: []Module{ModuleBackup}}, Arn: kpBackup})
	if _, err := SluisKeyPolicyStatements(a); err == nil {
		t.Error("a role named twice was accepted")
	}
	a = kpArgs(LayoutV5)
	a.Roles = append(a.Roles, KeyRole{Role: Role{Name: "my-fn", Hosts: []Module{ModuleBackup}}, Arn: kpBackup},
		KeyRole{Role: Role{Name: "my--fn", Hosts: []Module{ModuleBackup}}, Arn: kpBackup})
	if _, err := SluisKeyPolicyStatements(a); err == nil || !strings.Contains(err.Error(), "repeats Sid") {
		t.Errorf("colliding Sids: %v", err)
	}
}

func TestSluisKeyPolicySignContext(t *testing.T) {
	st := kpStatements(t, kpArgs(LayoutV5))
	allow := kpBySid(t, st, "SluisKeySign")
	cond := allow["Condition"].(map[string]any)
	if !reflect.DeepEqual(cond["StringEquals"], map[string]any{
		"kms:EncryptionContext:instance": kpInst, "kms:EncryptionContext:purpose": "sign",
	}) {
		t.Errorf("context = %v", cond["StringEquals"])
	}
	if !reflect.DeepEqual(cond["ForAllValues:StringEquals"], map[string]any{"kms:EncryptionContextKeys": []string{"instance", "purpose"}}) {
		t.Errorf("context keys = %v", cond["ForAllValues:StringEquals"])
	}
	if !reflect.DeepEqual(cond["ArnEquals"], map[string]any{"aws:PrincipalArn": []string{kpIssuer}}) {
		t.Errorf("principal = %v", cond["ArnEquals"])
	}
	if !reflect.DeepEqual(allow["Action"], []string{"kms:Encrypt", "kms:Decrypt", "kms:GenerateDataKey"}) {
		t.Errorf("actions = %v", allow["Action"])
	}
	// Only the roles that host oidc may sign; no ViaService, so the grant is for
	// direct calls only.
	if _, via := cond["StringEquals"].(map[string]any)["kms:ViaService"]; via {
		t.Error("the sign grant names a service")
	}
	// The denies hold the purpose to those roles and their direct calls to it.
	res := kpBySid(t, st, "SluisKeySignContextReserved")["Condition"].(map[string]any)
	if res["StringEquals"].(map[string]any)["kms:EncryptionContext:purpose"] != "sign" ||
		!reflect.DeepEqual(res["ArnNotEquals"], map[string]any{"aws:PrincipalArn": []string{kpIssuer}}) {
		t.Errorf("reserved = %v", res)
	}
	for _, sid := range []string{"SluisKeySignDirectPurposeOnly", "SluisKeySignDirectContextKeysOnly"} {
		c := kpBySid(t, st, sid)["Condition"].(map[string]any)
		if !reflect.DeepEqual(c["Null"], map[string]any{"kms:ViaService": "true"}) {
			t.Errorf("%s does not exempt calls through a service: %v", sid, c)
		}
	}
	// A role through SSM carries PARAMETER_ARN and no purpose: the key policy
	// of its statement names the service and the parameters, not the context.
	role := kpBySid(t, st, "SluisKeyRoleIssuer")["Condition"].(map[string]any)
	if role["StringEquals"].(map[string]any)["kms:ViaService"] != "ssm."+kpRegion+".amazonaws.com" {
		t.Errorf("role not held to SSM: %v", role)
	}
}

func TestSluisKeyPolicyParameterScopes(t *testing.T) {
	root := arnPrefix + "ssm:" + kpRegion + ":" + kpAccount + ":parameter/sluis/" + kpInst
	arns := func(sid string, st []map[string]any) []string {
		c := kpBySid(t, st, sid)["Condition"].(map[string]any)["StringLike"].(map[string]any)["kms:EncryptionContext:PARAMETER_ARN"]
		return c.([]string)
	}
	st := kpStatements(t, kpArgs(LayoutV5))
	cf := arns("SluisKeyRoleCloudflare", st)
	if !reflect.DeepEqual(cf, []string{root + "/internal/cloudflare/*", root + "/external/cloudflare/*"}) {
		t.Errorf("cloudflare role: %v", cf)
	}
	if len(arns("SluisKeyRoleIssuer", st)) != 8 {
		t.Errorf("issuer role: %v", arns("SluisKeyRoleIssuer", st))
	}
	for _, sid := range []string{"SluisKeyReadAllBackup", "SluisKeyWriteAllRestore"} {
		if got := arns(sid, st); !reflect.DeepEqual(got, []string{root + "/internal/*", root + "/external/*"}) {
			t.Errorf("%s: %v", sid, got)
		}
	}
	if got := kpBySid(t, st, "SluisKeyReadAllBackup")["Action"]; !reflect.DeepEqual(got, []string{"kms:Decrypt", "kms:DescribeKey"}) {
		t.Errorf("backup actions: %v", got)
	}
	if got := kpBySid(t, st, "SluisKeyWriteAllRestore")["Action"].([]string); len(got) != 4 {
		t.Errorf("restore actions: %v", got)
	}
	if got := arns("SluisKeyExternalReaders", st); !reflect.DeepEqual(got, []string{root + "/external/*"}) {
		t.Errorf("readers: %v", got)
	}
	if got := kpBySid(t, st, "SluisKeyExternalReaders")["Action"]; !reflect.DeepEqual(got, []string{"kms:Decrypt"}) {
		t.Errorf("readers actions: %v", got)
	}
	if got := arns("SluisKeyMinterIssuer", st); !reflect.DeepEqual(got, []string{root + "/internal/cloudflare/main/minter"}) {
		t.Errorf("minter: %v", got)
	}
	if got := arns("SluisKeyAdminReads", st); !reflect.DeepEqual(got, []string{root + "/internal/oidc/state-secret", root + "/internal/oidc/recovery-password"}) {
		t.Errorf("admin reads v5: %v", got)
	}
	// v4 adds the v4 names and gives the issuer the whole of internal/ and external/.
	v4 := kpStatements(t, kpArgs(LayoutV4))
	if got := arns("SluisKeyAdminReads", v4); len(got) != 4 {
		t.Errorf("admin reads v4: %v", got)
	}
	if got := arns("SluisKeyRoleIssuer", v4); got[0] != root+"/internal/*" || got[1] != root+"/external/*" {
		t.Errorf("issuer v4: %v", got)
	}
	// The admin and breakglass statements are about the principals they name.
	c := kpBySid(t, st, "SluisKeyAdminSeeds")["Condition"].(map[string]any)
	if !reflect.DeepEqual(c["ArnLike"], map[string]any{"aws:PrincipalArn": []string{kpAdmin}}) {
		t.Errorf("admin: %v", c)
	}
	if got := kpBySid(t, st, "SluisKeyAdminSeeds")["Action"]; !reflect.DeepEqual(got, []string{"kms:Encrypt", "kms:GenerateDataKey", "kms:DescribeKey"}) {
		t.Errorf("admin seeds: %v", got)
	}
}

// No statement is a catch-all: every Allow names the account root with a
// principal condition, every Deny is about a sluis role or the sign purpose,
// none uses NotPrincipal, and nothing allows an action on a key outside SSM
// except the sign roles' direct calls.
func TestSluisKeyPolicyHasNoCatchAll(t *testing.T) {
	a := kpArgs(LayoutV4V5)
	a.DenyOtherActions = true
	for _, s := range kpStatements(t, a) {
		raw, _ := json.Marshal(s)
		sid := s["Sid"].(string)
		if _, ok := s["NotPrincipal"]; ok {
			t.Errorf("%s: NotPrincipal", sid)
		}
		cond, _ := s["Condition"].(map[string]any)
		switch s["Effect"] {
		case "Allow":
			if s["Principal"].(map[string]any)["AWS"] != kpRoot || !strings.Contains(string(raw), "aws:PrincipalArn") {
				t.Errorf("%s: an Allow not narrowed to principals: %s", sid, raw)
			}
			if !strings.Contains(string(raw), "kms:ViaService") && sid != "SluisKeySign" {
				t.Errorf("%s: not held to SSM", sid)
			}
		case "Deny":
			bySign := cond["StringEquals"] != nil && cond["StringEquals"].(map[string]any)["kms:EncryptionContext:purpose"] == "sign"
			byRole := cond["ArnEquals"] != nil && reflect.DeepEqual(cond["ArnEquals"], map[string]any{"aws:PrincipalArn": []string{kpIssuer}})
			if !bySign && !byRole {
				t.Errorf("%s: a Deny about neither the sign purpose nor a sluis role: %s", sid, raw)
			}
		}
	}
}

// Shapes equivalent to the sign statements the two estates write by hand today,
// with neutral names. The helper must carry every one of them (same Effect and
// actions, every condition the estate's statement has, with the same value) and
// the test lists what it adds: a new check on the sluis role, or a wider list.
type legacy struct {
	sid    string
	st     map[string]any
	helper string
	extras []string
}

func kpLegacyKernel(function string) []legacy {
	actions := []string{"kms:Encrypt", "kms:Decrypt", "kms:GenerateDataKey"}
	keys := []string{"instance", "purpose"}
	anyone := map[string]any{"AWS": "*"}
	direct := map[string]any{"kms:ViaService": "true"}
	return []legacy{
		{sid: "SluisSign", helper: "SluisKeySign", st: map[string]any{
			"Effect": "Allow", "Principal": map[string]any{"AWS": kpRoot}, "Action": actions,
			"Condition": map[string]any{
				"ArnEquals":                 map[string]any{"aws:PrincipalArn": function},
				"StringEquals":              map[string]any{"kms:EncryptionContext:instance": kpInst, "kms:EncryptionContext:purpose": "sign"},
				"ForAllValues:StringEquals": map[string]any{"kms:EncryptionContextKeys": keys},
			}}},
		{sid: "SluisSignContextReserved", helper: "SluisKeySignContextReserved", st: map[string]any{
			"Effect": "Deny", "Principal": anyone,
			"Action": []string{"kms:Decrypt", "kms:Encrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:CreateGrant"},
			"Condition": map[string]any{
				"StringEquals": map[string]any{"kms:EncryptionContext:purpose": "sign"},
				"ArnNotEquals": map[string]any{"aws:PrincipalArn": []string{function}},
			}}},
		{sid: "SluisSignRoleDirectPurposeOnly", helper: "SluisKeySignDirectPurposeOnly", st: map[string]any{
			"Effect": "Deny", "Principal": anyone, "Action": actions,
			"Condition": map[string]any{
				"ArnEquals":       map[string]any{"aws:PrincipalArn": []string{function}},
				"Null":            direct,
				"StringNotEquals": map[string]any{"kms:EncryptionContext:purpose": "sign"},
			}}},
		{sid: "SluisSignRoleDirectContextKeysOnly", helper: "SluisKeySignDirectContextKeysOnly", st: map[string]any{
			"Effect": "Deny", "Principal": anyone, "Action": actions,
			"Condition": map[string]any{
				"ArnEquals":                   map[string]any{"aws:PrincipalArn": []string{function}},
				"Null":                        direct,
				"ForAnyValue:StringNotEquals": map[string]any{"kms:EncryptionContextKeys": keys},
			}}},
		{sid: "SluisRoleNothingElse", helper: "SluisKeyRolesNothingElse", extras: []string{"NotAction:+kms:DescribeKey"}, st: map[string]any{
			"Effect": "Deny", "Principal": anyone,
			"NotAction": append(append([]string(nil), actions...), "kms:DescribeKey"),
			"Condition": map[string]any{"ArnEquals": map[string]any{"aws:PrincipalArn": []string{function}}},
		}},
		{sid: "FunctionRoleUsesTheKeyThroughSSM", helper: "SluisKeyRoleIssuer", st: map[string]any{
			"Effect": "Allow", "Principal": map[string]any{"AWS": kpRoot},
			"Action": []string{"kms:Encrypt", "kms:Decrypt", "kms:GenerateDataKey", "kms:DescribeKey"},
			"Condition": map[string]any{
				"ArnEquals":    map[string]any{"aws:PrincipalArn": function},
				"StringEquals": map[string]any{"kms:ViaService": "ssm." + kpRegion + ".amazonaws.com"},
			}},
			extras: []string{"StringLike/kms:EncryptionContext:PARAMETER_ARN (narrowing: internal/* and external/* only; v3 private/* is gone)"}},
		{sid: "ExternalSecretsReadersDecryptExternalOnly", helper: "SluisKeyExternalReaders", st: map[string]any{
			"Effect": "Allow", "Principal": map[string]any{"AWS": kpRoot}, "Action": []string{"kms:Decrypt"},
			"Condition": map[string]any{
				"ArnEquals":    map[string]any{"aws:PrincipalArn": []string{kpReader2, kpReader1}},
				"StringEquals": map[string]any{"kms:ViaService": "ssm." + kpRegion + ".amazonaws.com"},
				"StringLike": map[string]any{"kms:EncryptionContext:PARAMETER_ARN": []string{
					arnPrefix + "ssm:" + kpRegion + ":" + kpAccount + ":parameter/sluis/" + kpInst + "/external/*"}},
			}}},
		{sid: "OperatorSeedsTheParameters", helper: "SluisKeyAdminSeeds", st: map[string]any{
			"Effect": "Allow", "Principal": map[string]any{"AWS": kpRoot},
			"Action": []string{"kms:Encrypt", "kms:GenerateDataKey", "kms:DescribeKey"},
			"Condition": map[string]any{
				"ArnLike":      map[string]any{"aws:PrincipalArn": []string{kpAdmin}},
				"StringEquals": map[string]any{"kms:ViaService": "ssm." + kpRegion + ".amazonaws.com"},
			}}},
		{sid: "OperatorReadsTheStacksOwnSecrets", helper: "SluisKeyAdminReads", st: map[string]any{
			"Effect": "Allow", "Principal": map[string]any{"AWS": kpRoot}, "Action": []string{"kms:Decrypt"},
			"Condition": map[string]any{
				"ArnLike":      map[string]any{"aws:PrincipalArn": []string{kpAdmin}},
				"StringEquals": map[string]any{"kms:ViaService": "ssm." + kpRegion + ".amazonaws.com"},
				"StringLike": map[string]any{"kms:EncryptionContext:PARAMETER_ARN": []string{
					arnPrefix + "ssm:" + kpRegion + ":" + kpAccount + ":parameter/sluis/" + kpInst + "/internal/config/issuer/state-secret",
					arnPrefix + "ssm:" + kpRegion + ":" + kpAccount + ":parameter/sluis/" + kpInst + "/internal/config/recovery/password",
				}},
			}},
			extras: []string{"StringLike/kms:EncryptionContext:PARAMETER_ARN (adds the v5 names)"}},
		{sid: "BreakglassUsesTheKeyThroughSSM", helper: "SluisKeyBreakglass", st: map[string]any{
			"Effect": "Allow", "Principal": map[string]any{"AWS": kpRoot},
			"Action": []string{"kms:Encrypt", "kms:Decrypt", "kms:GenerateDataKey", "kms:DescribeKey"},
			"Condition": map[string]any{
				"ArnLike":      map[string]any{"aws:PrincipalArn": []string{kpGlass}},
				"StringEquals": map[string]any{"kms:ViaService": "ssm." + kpRegion + ".amazonaws.com"},
			}}},
	}
}

// kpLegacyHive is the shape of the single-function estate: the Allow names the
// role as the Principal, and the two role denies carry no Null on ViaService.
func kpLegacyHive(function string) []legacy {
	actions := []string{"kms:Encrypt", "kms:Decrypt", "kms:GenerateDataKey"}
	keys := []string{"instance", "purpose"}
	anyone := map[string]any{"AWS": "*"}
	nullNote := "Null/kms:ViaService (calls through SSM are not held to the sign context)"
	return []legacy{
		{sid: "SluisSign", helper: "SluisKeySign", st: map[string]any{
			"Effect": "Allow", "Principal": map[string]any{"AWS": function}, "Action": actions,
			"Condition": map[string]any{
				"StringEquals":              map[string]any{"kms:EncryptionContext:instance": kpInst, "kms:EncryptionContext:purpose": "sign"},
				"ForAllValues:StringEquals": map[string]any{"kms:EncryptionContextKeys": keys},
			}},
			extras: []string{"ArnEquals/aws:PrincipalArn (the role moves from Principal to a condition on the account root)"}},
		{sid: "SluisSignContextReserved", helper: "SluisKeySignContextReserved", st: map[string]any{
			"Effect": "Deny", "Principal": anyone,
			"Action": []string{"kms:Decrypt", "kms:Encrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:CreateGrant"},
			"Condition": map[string]any{
				"StringEquals": map[string]any{"kms:EncryptionContext:purpose": "sign"},
				"ArnNotEquals": map[string]any{"aws:PrincipalArn": []string{function}},
			}}},
		{sid: "SluisSignRolePurposeOnly", helper: "SluisKeySignDirectPurposeOnly", extras: []string{nullNote}, st: map[string]any{
			"Effect": "Deny", "Principal": anyone, "Action": actions,
			"Condition": map[string]any{
				"ArnEquals":       map[string]any{"aws:PrincipalArn": []string{function}},
				"StringNotEquals": map[string]any{"kms:EncryptionContext:purpose": "sign"},
			}}},
		{sid: "SluisSignRoleContextKeysOnly", helper: "SluisKeySignDirectContextKeysOnly", extras: []string{nullNote}, st: map[string]any{
			"Effect": "Deny", "Principal": anyone, "Action": actions,
			"Condition": map[string]any{
				"ArnEquals":                   map[string]any{"aws:PrincipalArn": []string{function}},
				"ForAnyValue:StringNotEquals": map[string]any{"kms:EncryptionContextKeys": keys},
			}}},
		{sid: "SluisSignRoleNothingElse", helper: "SluisKeyRolesNothingElse", extras: []string{"NotAction:+kms:DescribeKey"}, st: map[string]any{
			"Effect": "Deny", "Principal": anyone, "NotAction": actions,
			"Condition": map[string]any{"ArnEquals": map[string]any{"aws:PrincipalArn": []string{function}}},
		}},
	}
}

// norm makes a statement comparable: the role in a Principal becomes a condition
// on the root, and values are sorted and stringified.
func kpNorm(s map[string]any) (head map[string]any, cond map[string]string) {
	cond = map[string]string{}
	head = map[string]any{"Effect": s["Effect"]}
	for _, k := range []string{"Action", "NotAction"} {
		if v, ok := s[k]; ok {
			head[k] = v
		}
	}
	if c, ok := s["Condition"].(map[string]any); ok {
		for op, m := range c {
			for k, v := range m.(map[string]any) {
				cond[op+"/"+k] = kpVal(v)
			}
		}
	}
	if p := s["Principal"].(map[string]any)["AWS"].(string); p != "*" && p != kpRoot {
		cond["ArnEquals/aws:PrincipalArn"] = kpVal(p)
	}
	return head, cond
}

func kpVal(v any) string {
	var vs []string
	switch x := v.(type) {
	case string:
		vs = []string{x}
	case []string:
		vs = append(vs, x...)
	}
	sort.Strings(vs)
	return strings.Join(vs, ",")
}

func kpCompare(t *testing.T, estate string, legacies []legacy, got []map[string]any) {
	t.Helper()
	for _, l := range legacies {
		h := kpBySid(t, got, l.helper)
		lh, lc := kpNorm(l.st)
		gh, gc := kpNorm(h)
		var added []string
		for k, v := range gc {
			old, had := lc[k]
			switch {
			case !had:
				added = append(added, k)
			case old != v && !strings.Contains(strings.Join(l.extras, ";"), k):
				t.Errorf("%s %s -> %s: %s is %q, was %q", estate, l.sid, l.helper, k, v, old)
			}
		}
		for k, v := range lc {
			if gc[k] != v && !strings.Contains(strings.Join(l.extras, ";"), k) {
				t.Errorf("%s %s -> %s: condition %s = %q is not carried (got %q)", estate, l.sid, l.helper, k, v, gc[k])
			}
		}
		if lh["Effect"] != gh["Effect"] || fmt.Sprint(lh["Action"]) != fmt.Sprint(gh["Action"]) {
			t.Errorf("%s %s -> %s: effect/actions %v differ from %v", estate, l.sid, l.helper, gh, lh)
		}
		if lh["NotAction"] != nil {
			wantNA := fmt.Sprint(lh["NotAction"])
			gotNA := fmt.Sprint(gh["NotAction"])
			if wantNA != gotNA && !strings.Contains(strings.Join(l.extras, ";"), "NotAction:+kms:DescribeKey") {
				t.Errorf("%s %s: NotAction %s vs %s", estate, l.sid, gotNA, wantNA)
			}
		}
		sort.Strings(added)
		// Every added condition is one the test names.
		for _, k := range added {
			if !strings.Contains(strings.Join(l.extras, ";"), k) {
				t.Errorf("%s %s -> %s: unlisted addition %s", estate, l.sid, l.helper, k)
			}
		}
		// Every listed addition happens.
		for _, e := range l.extras {
			key, _, _ := strings.Cut(e, " ")
			if strings.HasPrefix(key, "NotAction:") {
				continue
			}
			if _, ok := gc[key]; !ok {
				t.Errorf("%s %s: listed addition %s is not in the helper", estate, l.sid, key)
			}
		}
	}
}

func TestSluisKeyPolicyCoversTheEstatesStatements(t *testing.T) {
	a := kpArgs(LayoutV4V5)
	a.DenyOtherActions = true
	got := kpStatements(t, a)
	// Kernel shape: the function's role is the issuer's; the single-function estate.
	kpCompare(t, "kernel", kpLegacyKernel(kpIssuer), got)
	kpCompare(t, "single-function", kpLegacyHive(kpIssuer), got)
}

// The helper's statements merge with an estate's own (a seal, two operator
// purposes, a state bucket and a "nothing else" deny over the estate's
// principals) without a repeated Sid, and the document stays within KMS's limit.
func TestSluisKeyPolicyComposesWithEstateStatements(t *testing.T) {
	sluis := kpStatements(t, kpArgs(LayoutV5))
	sealRole := arnPrefix + "iam::" + kpAccount + ":role/seal"
	opRole := arnPrefix + "iam::" + kpAccount + ":role/operator"
	estate := []map[string]any{
		{"Sid": "AdministrationViaIAM", "Effect": "Allow", "Principal": map[string]any{"AWS": kpRoot}, "Action": []string{"kms:PutKeyPolicy"}, "Resource": "*"},
		{"Sid": "Seal", "Effect": "Allow", "Principal": map[string]any{"AWS": sealRole}, "Action": []string{"kms:Decrypt"}, "Resource": "*",
			"Condition": map[string]any{"Null": map[string]any{"kms:EncryptionContextKeys": "true"}}},
		{"Sid": "OperatorSecrets", "Effect": "Allow", "Principal": map[string]any{"AWS": opRole}, "Action": []string{"kms:Decrypt"}, "Resource": "*",
			"Condition": map[string]any{"StringEquals": map[string]any{"kms:EncryptionContext:purpose": "ops"}}},
		{"Sid": "CryptoOnlyNamedRoles", "Effect": "Deny", "Principal": map[string]any{"AWS": "*"}, "Action": []string{"kms:Decrypt"}, "Resource": "*",
			"Condition": map[string]any{"ArnNotEquals": map[string]any{
				"aws:PrincipalArn": []string{sealRole, opRole, kpIssuer, kpCF, kpBackup, kpRestore, kpReader1, kpReader2}}}},
	}
	doc, err := KeyPolicyDocument("shared-key", estate, sluis)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ID        string `json:"Id"`
		Version   string
		Statement []map[string]any
	}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Version != "2012-10-17" || parsed.ID != "shared-key" || len(parsed.Statement) != len(estate)+len(sluis) {
		t.Errorf("document: %s", doc[:200])
	}
	// The same Sid in both lists is refused.
	clash := []map[string]any{{"Sid": "SluisKeySign", "Effect": "Allow"}}
	if _, err := KeyPolicyDocument("x", sluis, clash); err == nil || !strings.Contains(err.Error(), "repeats Sid") {
		t.Errorf("clash: %v", err)
	}
	if _, err := KeyPolicyDocument("x", []map[string]any{{"Effect": "Allow"}}); err == nil {
		t.Error("a statement without a Sid was accepted")
	}
}

func TestSluisKeyPolicySize(t *testing.T) {
	a := kpArgs(LayoutV4V5)
	a.DenyOtherActions = true
	// The largest realistic input: every role hosts every module and the readers
	// are a dozen clusters.
	for i := 0; i < 12; i++ {
		a.ExternalReaders = append(a.ExternalReaders, fmt.Sprintf("%srole/cluster-%02d-reader", arnPrefix+"iam::"+kpAccount+":", i))
	}
	for i := range a.Roles {
		a.Roles[i].Role.Hosts = Modules()
	}
	doc, err := KeyPolicyDocument("sluis", kpStatements(t, a))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc) > maxKeyPolicyBytes/2 {
		t.Errorf("the sluis statements alone take %d bytes of KMS's %d", len(doc), maxKeyPolicyBytes)
	}
	// Past the limit the document is refused with the sizes.
	big := []map[string]any{{"Sid": "Big", "Effect": "Allow", "Resource": strings.Repeat("x", maxKeyPolicyBytes)}}
	if _, err := KeyPolicyDocument("x", big); err == nil || !strings.Contains(err.Error(), "32768") {
		t.Errorf("oversize: %v", err)
	}
	// Rendering is deterministic.
	a2, _ := KeyPolicyDocument("sluis", kpStatements(t, a))
	if a2 != doc {
		t.Error("the same input rendered two documents")
	}
}

func TestSluisKeyPolicyRefusesBadInput(t *testing.T) {
	for name, mut := range map[string]func(*KeyPolicyArgs){
		"no instance":    func(a *KeyPolicyArgs) { a.Modules.Instance = "" },
		"no account":     func(a *KeyPolicyArgs) { a.Account = "" },
		"no roles":       func(a *KeyPolicyArgs) { a.Roles = nil },
		"wildcard role":  func(a *KeyPolicyArgs) { a.Roles[0].Arn = arnPrefix + "iam::" + kpAccount + ":role/*" },
		"bad layout":     func(a *KeyPolicyArgs) { a.Layout = "v6" },
		"reader pattern": func(a *KeyPolicyArgs) { a.ExternalReaders = []string{arnPrefix + "iam::" + kpAccount + ":role/*"} },
		"admin not arn":  func(a *KeyPolicyArgs) { a.Admins = []string{"admin"} },
		"unknown module": func(a *KeyPolicyArgs) { a.Roles[1].Role.Hosts = []Module{"nope"} },
		"bad role name":  func(a *KeyPolicyArgs) { a.Roles[1].Role.Name = "Bad Name" },
	} {
		a := kpArgs(LayoutV5)
		mut(&a)
		if _, err := SluisKeyPolicyStatements(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
