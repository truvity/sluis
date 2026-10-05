package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/githubroster/app"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// valid is a v2 document naming a policy document that binds what it enables.
func valid(t *testing.T) string {
	t.Helper()
	policy := write(t, "apiVersion: sluis.truvity.github.io/policy/v2\ngroups: {x:y:z: {}}\ngithub: {a: {members: [x:y:z]}, b: {members: [x:y:z]}}\n"+
		"controllers: {github: {enabledOrgs: [a, b]}}\n")
	return "apiVersion: sluis.truvity.github.io/controller-github/v2\npolicy: {file: " + policy + "}\nconsoleURL: http://console:8080/console/\n"
}

// The controller starts from a valid file, and refuses, before anything is
// built, what it cannot do without: where the policy is, who answers who holds
// a group, a pass interval that is a span of time, and a log level it knows.
func TestTheControllerRefusesWhatItCannotDoWithout(t *testing.T) {
	ok := valid(t)
	if _, err := app.Load(write(t, ok)); err != nil {
		t.Fatalf("a valid file: %v", err)
	}
	for name, body := range map[string]string{
		"no policy document":  "apiVersion: sluis.truvity.github.io/controller-github/v2\nconsoleURL: http://console:8080\n",
		"no console":          strings.Replace(ok, "consoleURL", "release", 1),
		"a zero interval":     ok + "interval: 0s\n",
		"an unknown key":      ok + "intervall: 5m\n",
		"a log level unknown": ok + "log: {level: chatty}\n",
		"an old key":          ok + "ENABLED_ORGS: a\n",
	} {
		if _, err := app.Load(write(t, body)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := app.Load(write(t, ok+"interval: 0s\n")); err == nil || !strings.Contains(err.Error(), "interval") {
		t.Errorf("the refusal does not name the key: %v", err)
	}
}

// The probes listen on :7070 unless the file moves them, and a file that
// gives them no address is refused by the schema rather than read as ":0".
func TestTheProbesAddressIsConfigurable(t *testing.T) {
	ok := valid(t)
	if _, err := app.Load(write(t, ok)); err != nil {
		t.Fatalf("the default: %v", err)
	}
	if _, err := app.Load(write(t, ok+"probes: {address: ':9090'}\n")); err != nil {
		t.Fatalf("a moved listener: %v", err)
	}
	if _, err := app.Load(write(t, ok+"probes: {}\n")); err == nil {
		t.Error("probes with no address was accepted")
	}
}
