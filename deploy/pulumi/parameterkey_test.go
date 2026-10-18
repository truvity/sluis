package sluispulumi_test

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	sluisconfig "github.com/truvity/sluis/config"
	arp "github.com/truvity/sluis/deploy/pulumi"
)

var paramKey = arnp + "kms:" + region + ":" + account + ":key/params"

// secretsOf is the `secrets` block of a rendered service document.
func secretsOf(t *testing.T, doc string) map[string]any {
	t.Helper()
	var m struct {
		Secrets map[string]any `yaml:"secrets"`
	}
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	return m.Secrets
}

// The function writes its credentials itself, so the key is in the
// document it reads (secrets.kmsKeyId), from a Config and from an Installation.
func TestParameterKeyArnIsWrittenIntoTheServiceDocument(t *testing.T) {
	withKey := func(a *arp.LambdaArgs) { a.ParameterKeyArn = paramKey }
	for name, e := range map[string]estate{
		"config":       {mutate: withKey},
		"installation": withInstallation(exampleInstallation(t), withKey),
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := mustLambda(t, e)
			s := secretsOf(t, layerFiles(t, rec)["sluis/sluis.yaml"])
			if s["kmsKeyId"] != paramKey || s["source"] != "ssm" {
				t.Errorf("secrets = %v, want ssm with kmsKeyId %s", s, paramKey)
			}
		})
	}
}

// Unset, the library refuses to render: the parameters the services write would
// be encrypted with the AWS-managed key, which any role with ssm:GetParameter can
// decrypt. A Config, an Installation and an Installation that names the key
// itself (secrets.kmsKeyId) without ParameterKeyArn are all refused: the key is
// also what the roles are granted on.
func TestWithoutAParameterKeyTheLibraryRefuses(t *testing.T) {
	in := exampleInstallation(t)
	in.Secrets = &sluisconfig.Secrets{KMSKeyID: paramKey}
	for name, e := range map[string]estate{
		"config":                {noParameterKey: true},
		"installation":          withInstallation(exampleInstallation(t), func(a *arp.LambdaArgs) { a.ParameterKeyArn = "" }),
		"installation, own key": withInstallation(in, func(a *arp.LambdaArgs) { a.ParameterKeyArn = "" }),
	} {
		t.Run(name, func(t *testing.T) {
			e.noParameterKey = true
			if _, _, err := buildLambda(t, e); err == nil || !strings.Contains(err.Error(), "no customer-managed key for the secrets") {
				t.Errorf("got %v", err)
			}
		})
	}
}

// A module function's document written by the estate names the same key, or the
// parameters that function writes (GitHub and Slack apps, Google keys) are
// encrypted with the AWS-managed key.
func TestAModuleDocumentOfTheEstateNamesTheParameterKey(t *testing.T) {
	doc := func(key string) string {
		d := "apiVersion: sluis.truvity.github.io/controller-github/v2\n"
		if key != "" {
			d += "secrets: {kmsKeyId: " + key + "}\n"
		}
		return d
	}
	for name, c := range map[string]struct {
		doc     string
		refused bool
	}{"names no key": {doc(""), true}, "names another": {doc("alias/other"), true}, "names the key": {doc(paramKey), false},
		// An alias and the ARN of the key behind it are different ids: compared as text.
		"names the key behind the ARN as an alias": {doc("alias/params"), true}} {
		_, _, err := buildLambda(t, v5Estate(func(a *arp.LambdaArgs) {
			noDomain(a)
			a.Modules = map[string]arp.ModuleArgs{"github": {Document: c.doc}}
		}))
		switch {
		case c.refused && (err == nil || !strings.Contains(err.Error(), "Modules[github].Document")):
			t.Errorf("%s: %v, want a refusal naming the document", name, err)
		case !c.refused && err != nil:
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The key is the library's: another one in the document is refused, naming the
// field; the same one is accepted.
func TestADocumentThatNamesAnotherParameterKeyIsRefused(t *testing.T) {
	other := arnp + "kms:" + region + ":" + account + ":key/other"
	base := "issuerURL: https://access.example.test\n"
	withKey := func(a *arp.LambdaArgs) { a.ParameterKeyArn = paramKey }
	for name, tc := range map[string]struct {
		config string
		want   string
	}{
		"secrets.kmsKeyId": {base + "secrets: {kmsKeyId: " + other + "}\n", "kmsKeyId"},
		"adapters.secrets.settings.kmsKeyId": {
			base + "adapters: {secrets: {adapter: ssm, settings: {kmsKeyId: " + other + "}}}\n", "adapters.secrets.settings.kmsKeyId"},
	} {
		if _, _, err := buildLambda(t, estate{config: tc.config, mutate: withKey}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error naming %s", name, err, tc.want)
		}
	}
	for name, config := range map[string]string{
		"secrets.kmsKeyId":                   base + "secrets: {kmsKeyId: " + paramKey + "}\n",
		"adapters.secrets.settings.kmsKeyId": base + "adapters: {secrets: {adapter: ssm, settings: {kmsKeyId: " + paramKey + "}}}\n",
	} {
		if _, _, err := buildLambda(t, estate{config: config, mutate: withKey}); err != nil {
			t.Errorf("%s: the same key was refused: %v", name, err)
		}
	}

	in := exampleInstallation(t)
	in.Secrets = &sluisconfig.Secrets{KMSKeyID: other}
	if _, _, err := buildLambda(t, withInstallation(in, withKey)); err == nil || !strings.Contains(err.Error(), "secrets.kmsKeyId") {
		t.Errorf("an installation that names another key: %v", err)
	}
	in.Secrets = &sluisconfig.Secrets{KMSKeyID: paramKey}
	if _, _, err := buildLambda(t, withInstallation(in, withKey)); err != nil {
		t.Errorf("an installation that names the same key: %v", err)
	}
}
