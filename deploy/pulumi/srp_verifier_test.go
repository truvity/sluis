package sluispulumi_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluisconfig "github.com/truvity/sluis/config"
	arp "github.com/truvity/sluis/deploy/pulumi"
	"github.com/truvity/sluis/srp"
)

// The mock's generated password, with the look-alikes swapped as the library
// does (0 O 1 l I -> x X y Y z).
const mockedPassword = "AbxXcyYzdEfGhJkMnPqRsTuVwXyZaBcDeFgHjKmN"

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

// On layout v5 the library derives the recovery account's SRP credential from
// the generated password in the program, stores it where the function reads it,
// and the document names the verifier and not the password
// (docs/decisions/0076).
func TestTheRecoveryVerifierIsDerivedFromTheGeneratedPasswordOnLayoutV5(t *testing.T) {
	rec, out := mustLambda(t, withLayout(arp.LayoutV5))
	p := rec.one(t, ssmParameterType, "staging-recovery-verifier-v5")
	if prop(p, "name").StringValue() != "/sluis/staging/internal/oidc/recovery-verifier" || prop(p, "type").StringValue() != "SecureString" {
		t.Errorf("parameter: %v", p.Inputs)
	}
	value := prop(p, "value")
	if !value.IsSecret() {
		t.Error("the verifier's value is not marked secret")
	}
	got := creds(t, value.SecretValue().Element.StringValue())
	if want := srp.Default.NewCredential("recovery", mockedPassword); string(got.Verifier) != string(want.Verifier) || string(got.Salt) != string(want.Salt) {
		t.Error("the stored credential is not the one derived from the generated password for the account recovery")
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

func TestTheVerifierIsAPureFunctionOfThePassword(t *testing.T) {
	a, _ := mustLambda(t, withLayout(arp.LayoutV5))
	b, _ := mustLambda(t, withLayout(arp.LayoutV5))
	one := prop(a.one(t, ssmParameterType, "staging-recovery-verifier-v5"), "value").SecretValue().Element.StringValue()
	two := prop(b.one(t, ssmParameterType, "staging-recovery-verifier-v5"), "value").SecretValue().Element.StringValue()
	if one != two {
		t.Error("two runs with the same password wrote two different verifiers: an apply would rewrite the parameter")
	}
}

// On layout v4 the service holds the password and derives the verifier itself;
// the library writes no verifier and the document keeps naming the password.
func TestLayoutV4WritesNoVerifierAndKeepsNamingThePassword(t *testing.T) {
	rec, out := mustLambda(t, withLayout(arp.LayoutV4))
	if rec.has(ssmParameterType, "staging-recovery-verifier-v5") {
		t.Error("layout v4 wrote a verifier")
	}
	if out["recoveryVerifierParameter"] != "" {
		t.Errorf("output: %q", out["recoveryVerifierParameter"])
	}
	if !strings.Contains(layerFiles(t, rec)["sluis/sluis.yaml"], "passwordSecret: recovery/password") {
		t.Error("the document no longer names the password on layout v4")
	}
}

// The conformance account is off unless the configuration turns it on; then the
// library makes its password for the operator and its verifier for the function.
func TestTheConformanceAccountIsMadeOnlyWhenTurnedOn(t *testing.T) {
	off, offOut := mustLambda(t, withLayout(arp.LayoutV5))
	for _, name := range []string{"staging-conformance-password-v5", "staging-conformance-verifier-v5"} {
		if off.has(ssmParameterType, name) {
			t.Errorf("%s is made with the account off", name)
		}
	}
	if offOut["conformancePasswordParameter"] != "" {
		t.Errorf("output with the account off: %q", offOut["conformancePasswordParameter"])
	}

	on := withLayout(arp.LayoutV5)
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
	want := srp.Default.NewCredential("conformance", mockedPassword)
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

func TestTheConformanceAccountNeedsLayoutV5(t *testing.T) {
	e := withLayout(arp.LayoutV4)
	e.config = "issuerURL: https://access.example.test\nconformance: {enabled: true}\n"
	if _, _, err := buildLambda(t, e); err == nil || !strings.Contains(err.Error(), "Layout v5") {
		t.Errorf("err = %v, want a refusal naming Layout v5", err)
	}
}

// An installation on layout v5 reaches the function's document as the renderer
// writes it for v5: the verifier named, the password not, and the conformance
// account named when it is on.
func TestAnInstallationOnLayoutV5NamesTheVerifiersAndNotThePassword(t *testing.T) {
	in := exampleInstallation(t)
	in.Secrets = &sluisconfig.Secrets{KMSKeyID: paramKey}
	in.Conformance = &sluisconfig.Conformance{Enabled: true}
	in.AWS.Table = "" // the layout v4 table is not a v5 estate's
	rec, _ := mustLambda(t, withInstallation(in, func(a *arp.LambdaArgs) {
		a.Layout = arp.LayoutV5
		tbl := map[arp.Module]pulumi.StringInput{}
		for _, m := range arp.Modules() {
			tbl[m] = pulumi.String(tableArn("sluis-staging-" + string(m)))
		}
		a.State.Tables = tbl
	}))
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
