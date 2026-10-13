package config

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// the temp directory a check writes its documents to is named in the error; it
// is the only thing that differs between two runs.
var tempDir = regexp.MustCompile(`sluis-[a-z]+-[0-9]+`)

func text(err error) string {
	if err == nil {
		return "<nil>"
	}
	return tempDir.ReplaceAllString(err.Error(), "sluis-tmp")
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Remembering the outcome of a check changes neither the result nor the words
// of the error: the same bytes give the same answer, cold and warm, and a
// refused document is still refused with the same message.
func TestRememberedValidationAnswersAsTheUncachedOne(t *testing.T) {
	issuer := readFixture(t, "modules-full.issuer.yaml")
	policy := readFixture(t, "modules-full.policy.yaml")
	good := readFixture(t, "modules-full.github.yaml")
	bad := append(slices.Clone(good), []byte("\nnotAKey: true\n")...)

	for _, c := range []struct {
		name        string
		module      string
		doc, policy []byte
	}{
		{"valid", ModuleGitHub, good, policy},
		{"unknown key", ModuleGitHub, bad, policy},
		{"not a module", "nope", good, policy},
		{"not YAML", ModuleSlack, []byte("a: [b\n"), nil},
	} {
		want := text(validateModule(c.module, c.doc, c.policy))
		for i := 0; i < 3; i++ {
			if got := text(ValidateModule(c.module, c.doc, c.policy)); got != want {
				t.Errorf("%s, call %d: got %q, uncached %q", c.name, i, got, want)
			}
		}
	}

	wantErr := text(verifyUncached(issuer, []byte("not: [a policy")))
	for i := 0; i < 3; i++ {
		if got := text(verify(issuer, []byte("not: [a policy"))); got != wantErr {
			t.Errorf("verify, call %d: got %q, uncached %q", i, got, wantErr)
		}
	}
	if want, got := text(verifyUncached(issuer, policy)), text(verify(issuer, policy)); got != want {
		t.Errorf("verify: got %q, uncached %q", got, want)
	}
}

func TestModuleSecretsAreCopiedOut(t *testing.T) {
	issuer := readFixture(t, "modules-full.issuer.yaml")
	policy := readFixture(t, "modules-full.policy.yaml")
	a, err := ModuleSecrets(ModuleIssuer, issuer, policy)
	if err != nil || len(a) == 0 {
		t.Fatalf("%v, %v", a, err)
	}
	want := slices.Clone(a)
	a[0].Name = "changed by the caller"
	b, err := ModuleSecrets(ModuleIssuer, issuer, policy)
	if err != nil || !slices.Equal(b, want) {
		t.Fatalf("a caller's change reached the next one: %v, %v", b, err)
	}
}
