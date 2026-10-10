package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/storage/keys"
)

func writeBackupDoc(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backup.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const backupHead = "apiVersion: sluis.truvity.github.io/sluis-backup/v1\ninstance: example\n"

func TestTheFullBackupDocumentLoadsAndResolvesTheArchiveKey(t *testing.T) {
	doc, err := config.LoadBackup(filepath.Join("testdata", "sluis-backup.full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !config.IsBackup(filepath.Join("testdata", "sluis-backup.full.yaml")) || config.IsBackup(filepath.Join("testdata", "sluis.full.yaml")) {
		t.Error("IsBackup does not tell the documents apart")
	}
	kc, err := doc.ArchiveKeys()
	if err != nil {
		t.Fatal(err)
	}
	if kc.Adapter != "kms" || len(kc.Keys) != 1 || kc.Keys[keys.Archive].Key != "alias/example-archive" {
		t.Errorf("archive keys %+v", kc)
	}
	if keep, age := doc.Backup.Retention.Rule(); keep != 14 || age != 720*time.Hour {
		t.Errorf("retention %d %s", keep, age)
	}
	if s := doc.Serve(); s.Ports == nil || s.Secrets == nil || s.Instance != "example" {
		t.Errorf("serve view %+v", s)
	}
}

// backup.key is the short way to set keys.archive, and resolves to it.
func TestBackupKeyResolvesToTheArchivePurpose(t *testing.T) {
	doc, err := config.LoadBackup(writeBackupDoc(t, backupHead+`
backup:
  key: {key: alias/example-archive, context: off}
  target: {bucket: b}
`))
	if err != nil {
		t.Fatal(err)
	}
	kc, _ := doc.ArchiveKeys()
	e := kc.Keys[keys.Archive]
	if kc.Adapter != "kms" || e.Key != "alias/example-archive" || e.Context.Mode != keys.ContextOff {
		t.Errorf("%+v", kc)
	}
	if keep, age := doc.Backup.Retention.Rule(); keep != config.BackupDefaultKeep || age != config.BackupDefaultMaxAge {
		t.Errorf("defaults %d %s", keep, age)
	}
}

func TestTheBackupDocumentIsRefusedWhenItIsWrong(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"no key":        {"backup:\n  target: {bucket: b}\n", "backup.key: required"},
		"key twice":     {"keys: {adapter: kms, archive: alias/a}\nbackup:\n  key: alias/b\n  target: {bucket: b}\n", "give one"},
		"an ARN":        {"backup:\n  key: arn:aws:kms:eu-west-1:1:key/x\n  target: {bucket: b}\n", "backup.key"},
		"no bucket":     {"backup:\n  key: alias/a\n  target: {}\n", "bucket"},
		"keep zero":     {"backup:\n  key: alias/a\n  target: {bucket: b}\n  retention: {keep: 0}\n", "keep"},
		"unknown key":   {"backup:\n  key: alias/a\n  target: {bucket: b}\n  bogus: 1\n", "bogus"},
		"a policy file": {"policy: {file: x}\nbackup:\n  key: alias/a\n  target: {bucket: b}\n", "policy"},
	} {
		_, err := config.LoadBackup(writeBackupDoc(t, backupHead+c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.want)
		}
	}
}
