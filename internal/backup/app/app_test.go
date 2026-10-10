package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/backup/app"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backup.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const head = "apiVersion: sluis.truvity.github.io/sluis-backup/v1\n"
const target = "backup:\n  key: alias/archive\n  target: {bucket: b}\n"

func TestTheFullDocumentBuildsTheSettings(t *testing.T) {
	cfg, err := app.Load(filepath.Join("..", "..", "config", "testdata", "sluis-backup.full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel().String() != "INFO" {
		t.Errorf("level %s", cfg.LogLevel())
	}
}

// The module reads the layout the backup archive is of.
func TestADocumentOnLayoutV4OrWithoutTablesIsRefused(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"no storage":   {target, "layout v5"},
		"v4 secrets":   {"secrets: {source: ssm, root: /sluis/x}\nports: {adapter: dynamodb, dynamodb: {tables: {backup: t}}}\n" + target, "layout v5"},
		"bad instance": {"instance: a/b\n" + target, "instance"},
	} {
		_, err := app.Load(write(t, head+c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

func TestTheCreatorNamesTheBuild(t *testing.T) {
	if got := app.Creator(); !strings.HasPrefix(got, "sluis-backup ") {
		t.Errorf("creator %q", got)
	}
}
