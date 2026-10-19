package sluispulumi_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	sluisconfig "github.com/truvity/sluis/config"
	"github.com/truvity/sluis/srp"
)

// The mock's generated password, with the look-alikes swapped as the library
// does (0 O 1 l I -> x X y Y z).
const mockedPassword = "AbxXcyYzdEfGhJkMnPqRsTuVwXyZaBcDeFgHjKmN"

// The mock's RandomPassword result, before the look-alikes are swapped.
const rawPassword = "Ab0Oc1lIdEfGhJkMnPqRsTuVwXyZaBcDeFgHjKmN"

// The mock's generated salt: 32 bytes ("state-secret-state-secret-state!"), unrelated to the password.
const mockedSalt = "state-secret-state-secret-state!"

func creds(t *testing.T, doc string) srp.Credential {
	t.Helper()
	var wrapper struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(doc), &wrapper); err != nil {
		t.Fatalf("not the secret store's document: %v: %s", err, doc)
	}
	raw, err := base64.StdEncoding.DecodeString(wrapper.Value)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := srp.Default.ParseCredential(raw)
	if err != nil {
		t.Fatalf("not a credential: %v", err)
	}
	return cred
}

// The library derives the recovery account's SRP credential from
// the generated password in the program, stores it where the function reads it,
// and the document names the verifier and not the password
// (docs/decisions/0076).
func TestTheRecoveryVerifierIsDerivedFromTheGeneratedPassword(t *testing.T) {
	rec, out := mustLambda(t, estateWith())
	p := rec.one(t, ssmParameterType, "staging-recovery-verifier-v5")
	if prop(p, "name").StringValue() != "/sluis/staging/internal/oidc/recovery-verifier" || prop(p, "type").StringValue() != "SecureString" {
		t.Errorf("parameter: %v", p.Inputs)
	}
	value := prop(p, "value")
	if !value.IsSecret() {
		t.Error("the verifier's value is not marked secret")
	}
	got := creds(t, value.SecretValue().Element.StringValue())
	if want := srp.Default.NewCredential([]byte(mockedSalt), "recovery", mockedPassword); string(got.Verifier) != string(want.Verifier) ||
		string(got.Salt) != mockedSalt || got.Iterations != srp.Iterations {
		t.Error("the stored credential is not the one derived from the generated password and the generated salt for the account recovery")
	}
	if strings.Contains(value.SecretValue().Element.StringValue(), mockedPassword) {
		t.Error("the verifier's document carries the password")
	}
	if out["recoveryVerifierParameter"] != "/sluis/staging/internal/oidc/recovery-verifier" {
		t.Errorf("output: %q", out["recoveryVerifierParameter"])
	}
	// The password stays for the operator.
	rec.one(t, ssmParameterType, "staging-recovery-password-v5")

	files := layerFiles(t, rec)["sluis/sluis.yaml"]
	if strings.Contains(files, "passwordSecret") {
		t.Errorf("the service document names the password the function may not read: %s", files)
	}
}

// The salt is served to anybody who starts a sign-in, so it must tell nothing of the
// password: a RandomBytes of its own, kept while the password is and replaced with
// it, not a function of the password. And a second run with the same salt and password
// writes the same parameter, so an apply that rotates nothing rewrites nothing.
func TestTheSaltIsRandomAndKeptWithThePasswordAndNotDerivedFromIt(t *testing.T) {
	a, _ := mustLambda(t, estateWith())
	salt := a.one(t, "random:index/randomBytes:RandomBytes", "staging-recovery-salt")
	if prop(salt, "length").NumberValue() != 32 {
		t.Errorf("salt: %v", salt.Inputs)
	}
	// The keeper is the digest of the password, not the password: it is not copied into
	// another resource's state.
	sum := sha256.Sum256([]byte(rawPassword))
	keeper := prop(salt, "keepers")
	if !keeper.HasValue() || !keeper.IsObject() || !keeper.ObjectValue()["password"].HasValue() {
		t.Fatalf("the salt is not kept with the password: %v", salt.Inputs)
	}
	got := keeper.ObjectValue()["password"]
	if got.IsSecret() {
		got = got.SecretValue().Element
	}
	if got.StringValue() != hex.EncodeToString(sum[:]) || strings.Contains(got.StringValue(), rawPassword) {
		t.Errorf("the keeper is %q, want the SHA-256 of the password", got.StringValue())
	}
	b, _ := mustLambda(t, estateWith())
	one := prop(a.one(t, ssmParameterType, "staging-recovery-verifier-v5"), "value").SecretValue().Element.StringValue()
	two := prop(b.one(t, ssmParameterType, "staging-recovery-verifier-v5"), "value").SecretValue().Element.StringValue()
	if one != two {
		t.Error("two runs with the same password and salt wrote two different verifiers: an apply would rewrite the parameter")
	}
	// The salt in the document is the resource's, byte for byte.
	if got := creds(t, one); string(got.Salt) != mockedSalt {
		t.Errorf("the stored salt is %q, the resource's is %q", got.Salt, mockedSalt)
	}
}

// The conformance account is off unless the configuration turns it on; then the
// library makes its password for the operator and its verifier for the function.
func TestTheConformanceAccountIsMadeOnlyWhenTurnedOn(t *testing.T) {
	off, offOut := mustLambda(t, estateWith())
	for _, name := range []string{"staging-conformance-password-v5", "staging-conformance-verifier-v5"} {
		if off.has(ssmParameterType, name) {
			t.Errorf("%s is made with the account off", name)
		}
	}
	if offOut["conformancePasswordParameter"] != "" {
		t.Errorf("output with the account off: %q", offOut["conformancePasswordParameter"])
	}

	on := estateWith()
	on.config = "issuerURL: https://access.example.test\nconformance: {enabled: true}\ncontrollers:\n" +
		"  github: {consoleURL: \"https://access.example.test/console\"}\n" +
		"  slack: {consoleURL: \"https://access.example.test/console\"}\n"
	rec, out := mustLambda(t, on)
	pw := rec.one(t, ssmParameterType, "staging-conformance-password-v5")
	if prop(pw, "name").StringValue() != "/sluis/staging/internal/oidc/conformance-password" || !prop(pw, "value").IsSecret() {
		t.Errorf("password parameter: %v", pw.Inputs)
	}
	v := rec.one(t, ssmParameterType, "staging-conformance-verifier-v5")
	if prop(v, "name").StringValue() != "/sluis/staging/internal/oidc/conformance-verifier" || !prop(v, "value").IsSecret() {
		t.Errorf("verifier parameter: %v", v.Inputs)
	}
	got := creds(t, prop(v, "value").SecretValue().Element.StringValue())
	want := srp.Default.NewCredential([]byte(mockedSalt), "conformance", mockedPassword)
	if string(got.Verifier) != string(want.Verifier) {
		t.Error("the conformance verifier is not derived from its password for the account conformance")
	}
	recoveryParam := rec.one(t, ssmParameterType, "staging-recovery-verifier-v5")
	if recovery := creds(t, prop(recoveryParam, "value").SecretValue().Element.StringValue()); string(recovery.Verifier) == string(got.Verifier) {
		t.Error("the two accounts share a verifier")
	}
	if out["conformancePasswordParameter"] != "/sluis/staging/internal/oidc/conformance-password" {
		t.Errorf("output: %q", out["conformancePasswordParameter"])
	}
	if !strings.Contains(layerFiles(t, rec)["sluis/sluis.yaml"], "conformance/verifier") {
		t.Errorf("the document does not name the conformance verifier: %s", layerFiles(t, rec)["sluis/sluis.yaml"])
	}
}

// An installation reaches the function's document as the renderer writes it:
// the verifier named, the password not, and the conformance account named when
// it is on.
func TestAnInstallationNamesTheVerifiersAndNotThePassword(t *testing.T) {
	in := exampleInstallation(t)
	in.Secrets = &sluisconfig.Secrets{KMSKeyID: paramKey}
	in.Conformance = &sluisconfig.Conformance{Enabled: true}
	rec, _ := mustLambda(t, withInstallation(in, nil))
	doc := layerFiles(t, rec)["sluis/sluis.yaml"]
	if strings.Contains(doc, "passwordSecret") {
		t.Errorf("the document names the password:\n%s", doc)
	}
	for _, want := range []string{"verifierSecret: recovery/verifier", "verifierSecret: conformance/verifier", "layout: v5"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document lacks %q:\n%s", want, doc)
		}
	}
	for _, name := range []string{"staging-recovery-verifier-v5", "staging-conformance-password-v5", "staging-conformance-verifier-v5"} {
		if !rec.has(ssmParameterType, name) {
			t.Errorf("%s is not made", name)
		}
	}
}

// The administrators read back every SecureString of the SRP credentials the
// stack writes: the provider decrypts each after create and on refresh, so
// under a customer key an apply fails without them. The conformance account's
// are named only when it is on.
func TestTheAdminReadsNameTheSRPParametersTheStackWrites(t *testing.T) {
	for _, on := range []bool{false, true} {
		in := exampleInstallation(t)
		in.Secrets = &sluisconfig.Secrets{KMSKeyID: paramKey}
		if on {
			in.Conformance = &sluisconfig.Conformance{Enabled: true}
		}
		in.AWS.Table = ""
		a := withInstallation(in, func(a *arp.LambdaArgs) {
			a.Layout = arp.LayoutV5
			tbl := map[arp.Module]pulumi.StringInput{}
			for _, m := range arp.Modules() {
				tbl[m] = pulumi.String(tableArn("sluis-staging-" + string(m)))
			}
			a.State.Tables = tbl
		}).args(t)
		st, err := a.KeyPolicyStatements(arp.KeyPolicyOptions{
			Admins: []string{arnp + "iam::" + account + ":role/operator"}, KeyAdmins: []string{kpEstateKeyAdmin},
		})
		if err != nil {
			t.Fatal(err)
		}
		var reads string
		for _, s := range st {
			if s["Sid"] == "SluisKeyAdminReads" {
				raw, _ := json.Marshal(s)
				reads = string(raw)
			}
		}
		if reads == "" {
			t.Fatal("no SluisKeyAdminReads")
		}
		for name, want := range map[string]bool{
			arp.RecoveryVerifierParameterNameV5(in.Instance):    true,
			arp.ConformancePasswordParameterNameV5(in.Instance): on,
			arp.ConformanceVerifierParameterNameV5(in.Instance): on,
		} {
			if got := strings.Contains(reads, ":parameter"+name+`"`); got != want {
				t.Errorf("conformance %v: SluisKeyAdminReads names %s = %v, want %v\n%s", on, name, got, want, reads)
			}
		}
	}
}
