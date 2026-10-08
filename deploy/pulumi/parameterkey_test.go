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

// The function writes its credentials and exports itself, so the key is in the
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

// Unset, the document is what it was: no key, and the same bytes as with a key
// but for the key's own line.
func TestWithoutParameterKeyArnTheServiceDocumentIsUnchanged(t *testing.T) {
	for name, mk := range map[string]func(more func(*arp.LambdaArgs)) estate{
		"config": func(more func(*arp.LambdaArgs)) estate { return estate{mutate: more} },
		"installation": func(more func(*arp.LambdaArgs)) estate {
			return withInstallation(exampleInstallation(t), more)
		},
	} {
		t.Run(name, func(t *testing.T) {
			recOff, _ := mustLambda(t, mk(nil))
			off := layerFiles(t, recOff)["sluis/sluis.yaml"]
			if strings.Contains(off, "kmsKeyId") {
				t.Errorf("no ParameterKeyArn, and the document names a key:\n%s", off)
			}
			recOn, _ := mustLambda(t, mk(func(a *arp.LambdaArgs) { a.ParameterKeyArn = paramKey }))
			on := layerFiles(t, recOn)["sluis/sluis.yaml"]
			var kept []string
			for _, l := range strings.Split(on, "\n") {
				if !strings.Contains(l, "kmsKeyId: "+paramKey) {
					kept = append(kept, l)
				}
			}
			if strings.Join(kept, "\n") != off {
				t.Errorf("the key changed more than its own line:\n%s\n--- without ---\n%s", on, off)
			}
		})
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
