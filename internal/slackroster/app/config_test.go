package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/slackroster/app"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The controller starts from a valid file, and refuses, before anything is
// built, what it cannot do without: where the policy is, who answers who holds
// a group, a pass interval that is a span of time, and a log level it knows.
func TestTheControllerRefusesWhatItCannotDoWithout(t *testing.T) {
	const ok = "policyDir: /p\nconsoleURL: http://console:8080/console/\n"
	if _, err := app.Load(write(t, ok+"enabledWorkspaces: [a, b]\n")); err != nil {
		t.Fatalf("a valid file: %v", err)
	}
	for name, body := range map[string]string{
		"no policy directory": "consoleURL: http://console:8080\n",
		"no console":          "policyDir: /p\n",
		"a zero interval":     ok + "interval: 0s\n",
		"an unknown key":      ok + "intervall: 5m\n",
		"a log level unknown": ok + "log: {level: chatty}\n",
		"an old key":          ok + "ENABLED_WORKSPACES: a\n",
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
	const ok = "policyDir: /p\nconsoleURL: http://console:8080/console/\n"
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
