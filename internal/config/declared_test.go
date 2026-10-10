package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
)

var update = os.Getenv("UPDATE_GOLDEN") != ""

// declaredFixture is the declared set of a fixture, one line per secret:
// kind, subject, name (a ref is marked). Names, never values.
func declaredFixture(t *testing.T, name string) string {
	t.Helper()
	c, err := config.LoadConfig[config.Sluis](filepath.Join("testdata", "declared", name+".sluis.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Policy == nil {
		t.Fatal("the fixture names no policy")
	}
	var b strings.Builder
	for _, s := range config.DeclaredSecrets(&c.Service.Serve, c.Policy) {
		kind := "name"
		if s.Ref {
			kind = "ref"
		}
		b.WriteString(strings.Join([]string{s.Kind, "subject=" + s.Subject, kind + "=" + s.Name}, "\t") + "\n")
	}
	return b.String()
}

// The set a document and its policy declare is held to a golden for a
// Lambda-like and a Kubernetes-like installation: a rule that grows or shrinks it
// is a diff here, reviewed.
func TestTheDeclaredSetMatchesTheGolden(t *testing.T) {
	for _, name := range []string{"lambda", "k8s"} {
		t.Run(name, func(t *testing.T) {
			got := declaredFixture(t, name)
			golden := filepath.Join("testdata", "declared", name+".golden")
			if update {
				if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("the declared set changed (UPDATE_GOLDEN=1 rewrites it):\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestOnlyWhatIsDeclaredIsDeclared(t *testing.T) {
	// A nil document and policy declare nothing.
	if got := config.DeclaredSecrets(nil, nil); len(got) != 0 {
		t.Errorf("nothing declared, got %v", got)
	}
	// A disabled recovery sign-in needs no password.
	off := false
	doc := &config.Serve{Recovery: &config.Recovery{Enabled: &off, LoginSecret: "recovery/password"}}
	if got := config.DeclaredSecrets(doc, nil); len(got) != 0 {
		t.Errorf("a disabled recovery declared %v", got)
	}
	// A file signer has no state secret; a KMS one has.
	doc = &config.Serve{SigningKey: &config.SigningKey{File: "/k"}}
	if got := config.DeclaredSecrets(doc, nil); len(got) != 0 {
		t.Errorf("a file signer declared %v", got)
	}
	doc = &config.Serve{SigningKey: &config.SigningKey{KMS: &config.SigningKeyKMS{StateSecret: "issuer/state-secret"}}}
	if got := config.DeclaredSecrets(doc, nil); len(got) != 1 || got[0].Kind != config.SecretStateSecret {
		t.Errorf("a KMS signer declared %v", got)
	}
}
