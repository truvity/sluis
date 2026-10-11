package sluispulumi_test

import (
	"slices"
	"testing"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// The re-key operator may list, read and write the instance's parameters and use
// the new key through SSM for them, and nothing else.
func TestRekeyOperatorStatements(t *testing.T) {
	keyArn := arnp + "kms:" + region + ":" + account + ":key/acme-secrets"
	st, err := arp.RekeyOperatorStatements(region, account, "staging", keyArn)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]map[string]any{}
	for _, s := range st {
		by[s["Sid"].(string)] = s
	}
	if len(st) != 3 || by["SluisRekeyList"] == nil || by["SluisRekeyParameters"] == nil || by["SluisRekeyKey"] == nil {
		t.Fatalf("statements = %v", st)
	}
	list := by["SluisRekeyList"]
	if !slices.Equal(listOf(list["Action"]), []string{"ssm:DescribeParameters"}) || !slices.Equal(listOf(list["Resource"]), []string{"*"}) {
		t.Errorf("list: %v", by["SluisRekeyList"])
	}
	params := by["SluisRekeyParameters"]
	if !slices.Equal(listOf(params["Action"]), []string{"ssm:GetParameters", "ssm:PutParameter"}) ||
		!slices.Equal(listOf(params["Resource"]), []string{arnp + "ssm:" + region + ":" + account + ":parameter/sluis/staging/*"}) {
		t.Errorf("parameters: %v", params)
	}
	k := by["SluisRekeyKey"]
	cond := k["Condition"].(map[string]any)
	actions := []string{"kms:Encrypt", "kms:Decrypt", "kms:GenerateDataKey"}
	if !slices.Equal(listOf(k["Action"]), actions) || !slices.Equal(listOf(k["Resource"]), []string{keyArn}) ||
		cond["StringEquals"].(map[string]any)["kms:ViaService"] != "ssm."+region+".amazonaws.com" ||
		cond["StringLike"].(map[string]any)["kms:EncryptionContext:PARAMETER_ARN"] != arnp+"ssm:"+region+":"+account+":parameter/sluis/staging/*" {
		t.Errorf("key: %v", k)
	}
	if _, err = arp.RekeyOperatorStatements(region, account, "staging", ""); err == nil {
		t.Error("a grant on no key was rendered")
	}
}

// The parameters the library creates are encrypted with the key the function's
// own writes use: the alias of Keys.Secrets, which is also the document's
// secrets.kmsKeyId, and not the AWS-managed key.
func TestKeysSecretsEncryptsTheParametersTheLibraryCreates(t *testing.T) {
	rec, _ := mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { noDomain(a); a.Keys = suppliedKeys() }})
	p := rec.one(t, "aws:ssm/parameter:Parameter", "staging-state-secret-internal")
	if got := prop(p, "keyId"); !got.HasValue() || got.StringValue() != "alias/acme-secrets" {
		t.Errorf("keyId = %v, want alias/acme-secrets", got)
	}
	// Without it, and without ParameterKeyArn, nothing changes.
	rec, _ = mustLambda(t, estate{mutate: noDomain})
	if prop(rec.one(t, "aws:ssm/parameter:Parameter", "staging-state-secret-internal"), "keyId").HasValue() {
		t.Error("a key is set although none was given")
	}
	// Sign alone does not key the parameters.
	rec, _ = mustLambda(t, estate{mutate: func(a *arp.LambdaArgs) { noDomain(a); a.Keys = &arp.KeysArgs{Sign: "alias/acme-sign"} }})
	if prop(rec.one(t, "aws:ssm/parameter:Parameter", "staging-state-secret-internal"), "keyId").HasValue() {
		t.Error("a key is set from Keys.Sign")
	}
}

// listOf reads a statement field that is a string, []string or []any.
func listOf(v any) []string {
	if x, ok := v.([]string); ok {
		return x
	}
	return strs(v)
}
