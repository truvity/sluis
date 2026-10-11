package sluispulumi_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

// Every document the library renders for a function (issuer, backup, restore),
// for each audit adapter it can render (sqs to a queue it installs or is given,
// sqs the estate named, log) and each blob and state variant, is loaded by the
// runtime of that function. The library cannot import the runtime's internals,
// so the documents are written to a directory and
// TestEveryRenderedDocumentLoadsInItsRuntime of internal/renderedcheck is run over
// it: the loader's strict decoding of the adapters' settings, the stores' and
// the audit trail's assembly. The backup document once carried the sqs adapter's
// settings under a name the runtime does not know, and every invocation of it
// stopped at start; no test ran the runtime's side over that document.
//
// The library renders no other audit adapter (`connect` needs an `audit.writer`
// the library does not write), and no Cloudflare function document.
func TestEveryRenderedDocumentLoadsInItsRuntime(t *testing.T) {
	dir := t.TempDir()
	queue := "https://sqs." + region + ".amazonaws.com/" + account + "/audit-ingest"
	no := false

	write := func(t *testing.T, fn, name, doc string) {
		t.Helper()
		if strings.TrimSpace(doc) == "" {
			t.Fatalf("%s %s: no document was rendered", fn, name)
		}
		if err := os.WriteFile(filepath.Join(dir, fn+"."+strings.ReplaceAll(name, " ", "-")+".yaml"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	external := func() *arp.ExternalBlobs {
		return &arp.ExternalBlobs{Bucket: "r2-blobs", Endpoint: "https://r2.example.test", CredentialsRef: "internal/google/blobs-r2"}
	}

	// backup and restore: the audit queue or none, S3 or external blobs.
	for _, audit := range []struct{ name, queue string }{{"sqs", queue}, {"log", ""}} {
		for _, blobs := range []struct {
			name string
			ext  *arp.ExternalBlobs
		}{{"s3", nil}, {"external", external()}} {
			mutate := func(a *arp.BackupCommon) {
				a.AuditQueueURL = audit.queue
				if blobs.ext != nil {
					a.BlobBucketName, a.Storage = "", &arp.StorageGrant{External: blobs.ext}
				}
			}
			name := "audit-" + audit.name + " blobs-" + blobs.name
			bk, _ := mustBackup(t, func(a *arp.BackupArgs) { mutate(&a.BackupCommon) })
			write(t, "backup", name, archiveFiles(t, bk.one(t, layerType, "staging-backup-config"))["sluis/sluis.yaml"])
			rs, _ := mustRestore(t, func(a *arp.RestoreArgs) { mutate(&a.BackupCommon) })
			write(t, "restore", name, archiveFiles(t, rs.one(t, layerType, "staging-restore-config"))["sluis/sluis.yaml"])
		}
	}

	// the issuer: the audit installed, used, off, or named by the estate; S3 or
	// external blobs; the one table or a table per module.
	audits := map[string]*arp.AuditArgs{
		"install": installAudit(t),
		"use":     {Use: &arp.AuditUse{QueueURL: queue}},
		"off":     {Enabled: &no},
	}
	for aname, au := range audits {
		for _, ext := range []bool{false, true} {
			for _, tables := range []bool{false, true} {
				more := func(a *arp.LambdaArgs) {
					if tables {
						a.Layout = arp.LayoutV5
						tbl := map[arp.Module]pulumi.StringInput{}
						for _, m := range arp.Modules() {
							tbl[m] = pulumi.String(tableArn("sluis-staging-" + string(m)))
						}
						a.State.Tables, a.State.TableArn = tbl, nil
						in := *a.Installation
						aws := *in.AWS
						aws.Table = ""
						in.AWS = &aws
						a.Installation = &in
					}
					if ext {
						e := external()
						onBlobStore(e)(a)
					}
				}
				rec, _ := mustLambda(t, auditEstate(t, au, more))
				write(t, "issuer", "audit-"+aname+" external-"+strings.ToLower(strings.Title(boolWord(ext)))+" tables-"+boolWord(tables), serviceDocument(t, rec))
			}
		}
	}
	// the estate that names the queue in its installation (the older way).
	rec, _ := mustLambda(t, withInstallation(exampleInstallation(t), nil))
	write(t, "issuer", "audit-estate", serviceDocument(t, rec))

	cmd := exec.Command("go", "test", "-count=1", "-run", "^TestEveryRenderedDocumentLoadsInItsRuntime$", "-v", "./internal/renderedcheck")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "SLUIS_RENDERED_DOCS="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(out), "SKIP") {
		t.Fatalf("a runtime refuses a document the library renders: %v\n%s", err, out)
	}
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
