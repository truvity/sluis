package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
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

const (
	recipient1 = "age1843v2a5nyfrx6ssyftdyz7n8gncu5r0c3mwx4pgrv00uvca6vgjqa59uwj"
	recipient2 = "age1akzhxjkmgkq06fgkmrwadezd4rlav56g330960ez7f232z2ksf9sq7xk5u"
)

func TestTheFullBackupDocumentLoads(t *testing.T) {
	doc, err := config.LoadBackup(filepath.Join("testdata", "sluis-backup.full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !config.IsBackup(filepath.Join("testdata", "sluis-backup.full.yaml")) || config.IsBackup(filepath.Join("testdata", "sluis.full.yaml")) {
		t.Error("IsBackup does not tell the documents apart")
	}
	if got := doc.Backup.Recipients; len(got) != 2 || got[0] != recipient1 || got[1] != recipient2 {
		t.Errorf("recipients %v", got)
	}
	if doc.IdentityRef() != "restore-identity" || strings.Join(doc.Deprecated(), ",") != "keys.archive" {
		t.Errorf("identity %q, deprecated %v", doc.IdentityRef(), doc.Deprecated())
	}
	if keep, age := doc.Backup.Retention.Rule(); keep != 14 || age != 720*time.Hour {
		t.Errorf("retention %d %s", keep, age)
	}
	if s := doc.Serve(); s.Ports == nil || s.Secrets == nil || s.Instance != "example" {
		t.Errorf("serve view %+v", s)
	}
}

// The KMS archive key of format 1 is accepted for one release, ignored and
// reported as deprecated.
func TestTheFormerArchiveKeyIsAcceptedAndDeprecated(t *testing.T) {
	doc, err := config.LoadBackup(writeBackupDoc(t, backupHead+`
keys: {adapter: kms, archive: alias/a}
backup:
  key: {key: alias/example-archive, context: off}
  region: eu-west-1
  recipients: [`+recipient1+`]
  target: {bucket: b}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(doc.Deprecated(), ","); got != "backup.key,backup.region,keys.archive" {
		t.Errorf("deprecated %q", got)
	}
	if doc.IdentityRef() != config.BackupDefaultIdentity {
		t.Errorf("identity %q", doc.IdentityRef())
	}
	if keep, age := doc.Backup.Retention.Rule(); keep != config.BackupDefaultKeep || age != config.BackupDefaultMaxAge {
		t.Errorf("defaults %d %s", keep, age)
	}
}

// A document that still sets only `backup.region`, as the Pulumi library
// rendered it until the release that stopped, loads and is reported; the
// restore role's verification in it is read as before, and a document without
// the key is not reported.
func TestADocumentThatSetsOnlyTheFormerRegionLoadsAndIsReported(t *testing.T) {
	body := `
backup:
  role: restore
  region: eu-west-1
  identity: dr
  recipients: [` + recipient1 + `]
  verify: {query: "https://audit.example.test", tokenFile: /var/run/audit/token, observer: backup-fn}
  target: {bucket: b, region: eu-west-1}
`
	doc, err := config.LoadBackup(writeBackupDoc(t, backupHead+body))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(doc.Deprecated(), ","); got != "backup.region" {
		t.Errorf("deprecated %q, want backup.region", got)
	}
	if v := doc.Backup.Verify; v == nil || v.Observer != "backup-fn" || doc.IdentityRef() != "dr" || doc.Backup.Target.Region != "eu-west-1" {
		t.Errorf("verify %+v, identity %q, target %+v", doc.Backup.Verify, doc.IdentityRef(), doc.Backup.Target)
	}
	doc, err = config.LoadBackup(writeBackupDoc(t, backupHead+strings.Replace(body, "  region: eu-west-1\n  identity", "  identity", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Deprecated(); len(got) != 0 {
		t.Errorf("deprecated %v, want none", got)
	}
}

// The restore role needs no recipients.
func TestTheRestoreRoleNeedsNoRecipients(t *testing.T) {
	if _, err := config.LoadBackup(writeBackupDoc(t, backupHead+"backup:\n  role: restore\n  identity: dr\n  target: {bucket: b}\n")); err != nil {
		t.Fatal(err)
	}
}

func TestTheBackupDocumentIsRefusedWhenItIsWrong(t *testing.T) {
	r := "  recipients: [" + recipient1 + "]\n"
	for name, c := range map[string]struct{ body, want string }{
		"no recipients":    {"backup:\n  target: {bucket: b}\n", "backup.recipients"},
		"empty recipients": {"backup:\n  recipients: []\n  target: {bucket: b}\n", "recipients"},
		"not age":          {"backup:\n  recipients: [ssh-ed25519 AAAA]\n  target: {bucket: b}\n", "recipients"},
		"bad checksum":     {"backup:\n  recipients: [" + recipient1[:len(recipient1)-1] + "q]\n  target: {bucket: b}\n", "recipients"},
		"no bucket":        {"backup:\n" + r + "  target: {}\n", "bucket"},
		"keep zero":        {"backup:\n" + r + "  target: {bucket: b}\n  retention: {keep: 0}\n", "keep"},
		"unknown key":      {"backup:\n" + r + "  target: {bucket: b}\n  bogus: 1\n", "bogus"},
		"a policy file":    {"policy: {file: x}\nbackup:\n" + r + "  target: {bucket: b}\n", "policy"},
	} {
		_, err := config.LoadBackup(writeBackupDoc(t, backupHead+c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.want)
		}
	}
}

// A restore proves a backup genuine against the audit trail: where it is and
// how to ask are in the document, and a half-written block is refused.
func TestTheRestoreVerificationIsReadAndHeldToItsShape(t *testing.T) {
	doc, err := config.LoadBackup(writeBackupDoc(t, backupHead+`
backup:
  role: restore
  recipients: [`+recipient1+`]
  verify: {query: "https://audit.example.test", tokenFile: /var/run/audit/token, profile: activity, observer: backup-fn}
  target: {bucket: b}
`))
	if err != nil {
		t.Fatal(err)
	}
	if v := doc.Backup.Verify; v == nil || v.Query != "https://audit.example.test" || v.TokenFile != "/var/run/audit/token" ||
		v.Profile != "activity" || v.Observer != "backup-fn" {
		t.Errorf("verify %+v", v)
	}
	// Unset, the restore refuses every backup that is not started with allowUnverified.
	doc, err = config.LoadBackup(writeBackupDoc(t, backupHead+"backup:\n  role: restore\n  target: {bucket: b}\n"))
	if err != nil || doc.Backup.Verify != nil {
		t.Fatalf("%+v, %v", doc.Backup.Verify, err)
	}
	for name, c := range map[string]struct{ body, want string }{
		"no query":     {"verify: {tokenFile: /t, observer: o}", "query"},
		"not a URL":    {"verify: {query: audit, tokenFile: /t, observer: o}", "query"},
		"no token":     {"verify: {query: \"https://a.example.test\", observer: o}", "tokenFile"},
		"no observer":  {"verify: {query: \"https://a.example.test\", tokenFile: /t}", "observer"},
		"both":         {"verify: {query: \"https://a.example.test\", tokenFile: /t, observer: o, anyObserver: true}", "one or the other"},
		"plain http":   {"verify: {query: \"http://audit.example.test\", tokenFile: /t, observer: o}", "https"},
		"unknown key":  {"verify: {query: \"https://a.example.test\", tokenFile: /t, observer: o, bogus: 1}", "bogus"},
		"empty tokens": {"verify: {query: \"https://a.example.test\", tokenFile: \"\", observer: o}", "tokenFile"},
	} {
		_, err := config.LoadBackup(writeBackupDoc(t, backupHead+"backup:\n  role: restore\n  "+c.body+"\n  target: {bucket: b}\n"))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.want)
		}
	}
}

func TestTheRestoreVerificationMayAcceptAnyObserverIfItSaysSoAndLoopbackMayBePlainHTTP(t *testing.T) {
	for _, v := range []string{`{query: "https://audit.example.test", tokenFile: /t, anyObserver: true}`,
		`{query: "http://127.0.0.1:8080", tokenFile: /t, observer: o}`, `{query: "http://localhost:8080", tokenFile: /t, observer: o}`} {
		if _, err := config.LoadBackup(writeBackupDoc(t, backupHead+"backup:\n  role: restore\n  verify: "+v+"\n  target: {bucket: b}\n")); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
}
