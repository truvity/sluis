package sluispulumi_test

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"
	sluisconfig "github.com/truvity/sluis/config"
	arp "github.com/truvity/sluis/deploy/pulumi"
)

// The audit writer's release zip as the release makes one: `bootstrap` at the
// root, a static linux/arm64 Go binary built from a program of the audit
// module's path and command name, in a file named as the release names it. The
// audit library reads the architecture and the command from the binary.
var (
	writerMu  sync.Mutex
	writerBin []byte
)

func auditWriterZip(t *testing.T) (path, digest string) {
	t.Helper()
	writerMu.Lock()
	defer writerMu.Unlock()
	if writerBin == nil {
		dir := t.TempDir()
		write := func(name, body string) {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("go.mod", "module github.com/truvity/sluis/audit\n\ngo 1.27\n")
		write("cmd/audit-writer-lambda/main.go", "package main\n\nfunc main() { println(\"writer\") }\n")
		out := filepath.Join(dir, "bootstrap")
		build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", out, "./cmd/audit-writer-lambda")
		build.Dir = dir
		build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
		if msg, err := build.CombinedOutput(); err != nil {
			t.Fatalf("building the fixture binary: %v\n%s", err, msg)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		writerBin = b
	}
	p := filepath.Join(t.TempDir(), "audit-writer-lambda_0.11.0_linux_arm64.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(writerBin); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p, sha(t, p)
}

// catalogueDir is the directory of sluis's real audit catalogue (the release's
// sluis-audit-catalogue bundle holds the same files).
func catalogueDir() string { return filepath.Join("..", "..", "internal", "audit", "catalogue") }

const queueName = "audit-example-ingest"

var (
	queueURL = "https://sqs." + region + ".amazonaws.com/" + account + "/" + queueName
	queueArn = arnp + "sqs:" + region + ":" + account + ":" + queueName
)

// auditEstate is an installation-shaped estate that leaves the queue to the
// library, with audit set as given.
func auditEstate(t *testing.T, audit *arp.AuditArgs, more func(*arp.LambdaArgs)) estate {
	t.Helper()
	in := exampleInstallation(t)
	in.AWS.AuditQueueURL = ""
	return withInstallation(in, func(a *arp.LambdaArgs) {
		a.AuditQueueArn = nil
		a.Audit = audit
		if more != nil {
			more(a)
		}
	})
}

func installAudit(t *testing.T) *arp.AuditArgs {
	t.Helper()
	zip, digest := auditWriterZip(t)
	return &arp.AuditArgs{WriterPackage: zip, WriterPackageSHA256: digest, CatalogueDir: catalogueDir()}
}

// auditLayer is what the audit writer's configuration layer holds, by path under
// /opt/audit.
func auditLayer(t *testing.T, rec *recorder) map[string]string {
	t.Helper()
	return archiveFiles(t, rec.one(t, layerType, "audit-example-writer-config"))
}

// onBlobStore puts the blobs on an S3-compatible store: no bucket in the
// installation, the store in the grant.
func onBlobStore(blobs *arp.ExternalBlobs) func(*arp.LambdaArgs) {
	return func(a *arp.LambdaArgs) {
		in := *a.Installation
		aws := *in.AWS
		aws.Bucket = ""
		in.AWS = &aws
		a.Installation, a.Storage = &in, &arp.StorageGrant{External: blobs}
	}
}

func serviceDocument(t *testing.T, rec *recorder) string {
	t.Helper()
	return layerFiles(t, rec)["sluis/sluis.yaml"]
}

func hasSend(g map[string][]string, arn string) bool {
	for _, r := range g["sqs:SendMessage"] {
		if r == arn {
			return true
		}
	}
	return false
}

// Audit unset installs it: the operational preset derived from the profiles, an
// AWS archive of its own, the function's role the one sender of the queue, the
// service publishing to that queue, and the roster catalogue in the writer's
// package.
func TestAuditIsInstalledByDefaultAsOperationalOnS3(t *testing.T) {
	rec, out := mustLambda(t, auditEstate(t, installAudit(t), nil))

	// The installation: a queue and its writer, no notary, no alarm.
	rec.one(t, "aws:sqs/queue:Queue", queueName)
	rec.one(t, fnType, "audit-example-writer")
	for _, d := range rec.ofType(fnType) {
		if strings.Contains(d.Name, "notary") {
			t.Errorf("the operational preset has no notary, and %s is declared", d.Name)
		}
	}
	if n := len(rec.ofType("aws:sns/topic:Topic")); n != 0 {
		t.Errorf("the operational preset has no alarms, and %d topics are declared", n)
	}

	// The archive is a bucket of its own, SSE-S3 and named for the installation.
	b := rec.one(t, "aws:s3/bucket:Bucket", "audit-example-archive-operational")
	if got, want := prop(b, "bucket").StringValue(), "audit-example-"+account+"-"+region+"-operational"; got != want {
		t.Errorf("archive bucket %q, want %q", got, want)
	}
	sse := rec.one(t, "aws:s3/bucketServerSideEncryptionConfiguration:BucketServerSideEncryptionConfiguration", "audit-example-archive-operational")
	rule := sse.Inputs.Mappable()["rules"].([]any)[0].(map[string]any)["applyServerSideEncryptionByDefault"].(map[string]any)
	if rule["sseAlgorithm"] != "AES256" {
		t.Errorf("archive encryption: %v", sse.Inputs)
	}

	// The function may send to that queue, and the queue accepts it alone.
	if !hasSend(rolePolicy(t, rec), queueArn) {
		t.Errorf("the function's role may not send to %s: %v", queueArn, rolePolicy(t, rec))
	}

	// The service publishes there.
	doc := serviceDocument(t, rec)
	for _, want := range []string{"audit:", "adapter: sqs", "queueURL: " + queueURL} {
		if !strings.Contains(doc, want) {
			t.Errorf("the service document lacks %q:\n%s", want, doc)
		}
	}

	// The roster catalogue travels in the writer's package, and the deployment
	// says the sluis profile, composed from a framework profile that is operational.
	layer := auditLayer(t, rec)
	if _, ok := layer["audit/catalogues/catalogue-roster/catalogue-roster.yaml"]; !ok {
		t.Errorf("the writer's layer lacks the roster catalogue: %v", keysOf(layer))
	}
	if dep := layer["audit/deployment.yaml"]; !strings.Contains(dep, "security:") || !strings.Contains(dep, "history") {
		t.Errorf("deployment document:\n%s", dep)
	}
	if out["auditPresets"] != "operational" {
		t.Errorf("the presets are %q, the default profiles are kept under operational", out["auditPresets"])
	}
	if out["auditQueueUrl"] != queueURL || out["auditQueueArn"] != queueArn {
		t.Errorf("outputs: %v", out)
	}
}

// The senders of the queue are the function's role: it is the identity the
// records are attributed to.
func TestTheFunctionsRoleIsTheQueuesOnlySender(t *testing.T) {
	rec, _ := mustLambda(t, auditEstate(t, installAudit(t), nil))
	qp := prop(rec.one(t, "aws:sqs/queuePolicy:QueuePolicy", "audit-example-ingest"), "policy")
	if !strings.Contains(qp.StringValue(), arnp+"iam::"+account+":role/sluis-http") {
		t.Errorf("the queue policy does not name the function's role:\n%s", qp.StringValue())
	}
}

// The audit archive on an S3-compatible store: no bucket, the endpoint in the
// writer's configuration, the credentials at the installation's own address.
func TestAuditArchiveOnAnS3CompatibleStore(t *testing.T) {
	au := installAudit(t)
	au.Presets = map[string]arp.AuditPreset{"operational": {PresetStorage: auditpulumi.PresetStorage{Bucket: "acme-audit", Endpoint: "https://acct.r2.example.test"}}} //nolint:lll // a table row
	rec, _ := mustLambda(t, auditEstate(t, au, nil))
	if rec.has("aws:s3/bucket:Bucket", "audit-example-archive-operational") {
		t.Error("the archive is the store's, and an AWS bucket is declared for it")
	}
	layer := auditLayer(t, rec)
	for file, wants := range map[string][]string{
		"audit/deployment.yaml": {"endpoint: https://acct.r2.example.test", "bucket: acme-audit"},
		"audit/audit.yaml":      {"stateRoot:"},
	} {
		for _, want := range wants {
			if !strings.Contains(layer[file], want) {
				t.Errorf("%s lacks %q:\n%s", file, want, layer[file])
			}
		}
	}
	if strings.Contains(layer["audit/audit.yaml"], "lockMode") {
		t.Errorf("the lock is the preset's, not the function's:\n%s", layer["audit/audit.yaml"])
	}
}

// ReuseBlobStore takes the store from the blobs and keeps the archive a bucket
// of its own.
func TestAuditArchiveCanReuseTheBlobStore(t *testing.T) {
	au := installAudit(t)
	au.Presets = map[string]arp.AuditPreset{"operational": {PresetStorage: auditpulumi.PresetStorage{Bucket: "acme-audit"}, ReuseBlobStore: true}}
	blobs := r2()
	blobs.PathStyle = true
	rec, _ := mustLambda(t, auditEstate(t, au, onBlobStore(blobs)))
	conf := auditLayer(t, rec)["audit/deployment.yaml"]
	for _, want := range []string{"endpoint: " + blobs.Endpoint, "bucket: acme-audit", "path_style: true"} {
		if !strings.Contains(conf, want) {
			t.Errorf("the writer's configuration lacks %q:\n%s", want, conf)
		}
	}
	// Not the blob bucket.
	au = installAudit(t)
	au.Presets = map[string]arp.AuditPreset{"operational": {PresetStorage: auditpulumi.PresetStorage{Bucket: blobs.Bucket}, ReuseBlobStore: true}}
	_, _, err := buildLambda(t, auditEstate(t, au, onBlobStore(r2())))
	if err == nil || !strings.Contains(err.Error(), "blob bucket") {
		t.Errorf("the blob bucket as the archive: %v", err)
	}
}

// Use sends to an installation that exists and installs nothing.
func TestAuditUseInstallsNothingAndSendsToTheQueue(t *testing.T) {
	other := strings.Repeat("2", 12)
	url := "https://sqs." + region + ".amazonaws.com/" + other + "/their-ingest"
	rec, out := mustLambda(t, auditEstate(t, &arp.AuditArgs{Use: &arp.AuditUse{QueueURL: url}}, nil))
	if n := len(rec.ofType("aws:sqs/queue:Queue")); n != 0 {
		t.Errorf("Use installs nothing, and %d queues are declared", n)
	}
	wantArn := arnp + "sqs:" + region + ":" + other + ":their-ingest"
	if !hasSend(rolePolicy(t, rec), wantArn) {
		t.Errorf("the role may not send to %s: %v", wantArn, rolePolicy(t, rec))
	}
	if doc := serviceDocument(t, rec); !strings.Contains(doc, "queueURL: "+url) {
		t.Errorf("the service document lacks the queue:\n%s", doc)
	}
	if out["auditQueueUrl"] != url || out["auditQueueArn"] != wantArn {
		t.Errorf("outputs: %v", out)
	}
}

// Enabled false: nothing installed, no grant, and the runtime logs its records.
func TestAuditDisabledSendsNothingAndTheServiceLogs(t *testing.T) {
	no := false
	rec, out := mustLambda(t, auditEstate(t, &arp.AuditArgs{Enabled: &no}, nil))
	if n := len(rec.ofType("aws:sqs/queue:Queue")); n != 0 {
		t.Errorf("audit is off, and %d queues are declared", n)
	}
	if g := rolePolicy(t, rec); len(g["sqs:SendMessage"]) != 0 {
		t.Errorf("audit is off and the role may send: %v", g["sqs:SendMessage"])
	}
	doc := serviceDocument(t, rec)
	if !strings.Contains(doc, "adapter: log") || strings.Contains(doc, "queueURL") {
		t.Errorf("the service document does not log its audit records:\n%s", doc)
	}
	if out["auditQueueUrl"] != "" || out["auditQueueArn"] != "" {
		t.Errorf("outputs: %v", out)
	}
}

// The older way still works: the estate installs audit and names the queue; the
// library adds nothing to the document and installs nothing.
func TestAuditQueueArnKeepsTheOlderWay(t *testing.T) {
	in := exampleInstallation(t)
	rec, _ := mustLambda(t, withInstallation(in, nil))
	if n := len(rec.ofType("aws:sqs/queue:Queue")); n != 0 {
		t.Errorf("AuditQueueArn installs nothing, and %d queues are declared", n)
	}
	if !strings.Contains(serviceDocument(t, rec), "queueURL: "+in.AWS.AuditQueueURL) {
		t.Errorf("the estate's queue URL is not in the document:\n%s", serviceDocument(t, rec))
	}
}

func TestAuditArgumentsAreRefusedWhenTheyDisagree(t *testing.T) {
	no := false
	for name, c := range map[string]struct {
		audit  func(*testing.T) *arp.AuditArgs
		more   func(*arp.LambdaArgs)
		change func(*sluisconfig.Installation)
		want   string
	}{
		"both ways": {
			audit: installAudit,
			more:  func(a *arp.LambdaArgs) { a.AuditQueueArn = pulumi.String(queueArn) },
			want:  "both set",
		},
		"nothing to install with": {
			audit: func(*testing.T) *arp.AuditArgs { return &arp.AuditArgs{} },
			want:  "WriterPackage",
		},
		"use and disabled": {
			audit: func(*testing.T) *arp.AuditArgs {
				return &arp.AuditArgs{Enabled: &no, Use: &arp.AuditUse{QueueURL: queueURL}}
			},
			want: "Enabled is false and Use is set",
		},
		"use in another region": {
			audit: func(*testing.T) *arp.AuditArgs {
				return &arp.AuditArgs{Use: &arp.AuditUse{QueueURL: "https://sqs.eu-west-1.amazonaws.com/" + account + "/q"}}
			},
			want: "eu-west-1",
		},
		"use a queue that is no queue": {
			audit: func(*testing.T) *arp.AuditArgs {
				return &arp.AuditArgs{Use: &arp.AuditUse{QueueURL: "http://queue.example.test/q"}}
			},
			want: "QueueURL",
		},
		"profiles and the document": {
			audit: func(t *testing.T) *arp.AuditArgs {
				au := installAudit(t)
				au.Profiles = map[string][]string{"security": {"history"}}
				au.DeploymentYAML = "profiles: {}\n"
				return au
			},
			want: "Profiles and DeploymentYAML",
		},
		"reuse the blob store with no external blobs": {
			audit: func(t *testing.T) *arp.AuditArgs {
				au := installAudit(t)
				au.Presets = map[string]arp.AuditPreset{"operational": {PresetStorage: auditpulumi.PresetStorage{Bucket: "acme-audit"}, ReuseBlobStore: true}}
				return au
			},
			want: "ReuseBlobStore",
		},
		"an endpoint and reuse": {
			audit: func(t *testing.T) *arp.AuditArgs {
				au := installAudit(t)
				au.Presets = map[string]arp.AuditPreset{"operational": {PresetStorage: auditpulumi.PresetStorage{Bucket: "b", Endpoint: "https://x.example.test"}, ReuseBlobStore: true}} //nolint:lll // a table row
				return au
			},
			want: "say the store once",
		},
		"another queue in the installation": {
			audit: installAudit,
			change: func(in *sluisconfig.Installation) {
				in.AWS.AuditQueueURL = "https://sqs." + region + ".amazonaws.com/" + account + "/mine"
			},
			want: "say it once",
		},
		"another adapter in the installation": {
			audit: installAudit,
			change: func(in *sluisconfig.Installation) {
				in.Adapters = map[string]sluisconfig.AdapterChoice{"audit": {Adapter: "log"}}
			},
			want: "adapters.audit",
		},
		"a name that is no name": {
			audit: func(t *testing.T) *arp.AuditArgs { au := installAudit(t); au.Name = "Audit_1"; return au },
			want:  "Audit.Name",
		},
		"a stronger preset than the archive can keep": {
			audit: func(t *testing.T) *arp.AuditArgs {
				au := installAudit(t)
				au.DeploymentYAML = "profiles:\n  security: {frameworks: [pci-dss]}\n"
				au.Presets = map[string]arp.AuditPreset{"attested": {PresetStorage: auditpulumi.PresetStorage{Bucket: "acme-audit", Endpoint: "https://x.example.test"}}}
				return au
			},
			want: "Object Lock",
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := auditEstate(t, c.audit(t), c.more)
			inner := e.mutate
			e.mutate = func(a *arp.LambdaArgs) {
				inner(a)
				if c.change != nil {
					in := *a.Installation
					aws := *in.AWS
					in.AWS = &aws
					c.change(&in)
					a.Installation = &in
				}
			}
			_, _, err := buildLambda(t, e)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want one naming %q", err, c.want)
			}
		})
	}
	t.Run("audit with the deprecated documents", func(t *testing.T) {
		_, _, err := buildLambda(t, estate{mutate: func(a *arp.LambdaArgs) { a.AuditQueueArn = nil; a.Audit = installAudit(t) }})
		if err == nil || !strings.Contains(err.Error(), "needs LambdaArgs.Installation") {
			t.Errorf("error %v", err)
		}
	})
}

// The installation may keep two presets in two buckets: standard (a security
// profile) unlocked, attested (a payments profile) under compliance Object Lock.
func TestAuditMixedPresetsAreTwoBuckets(t *testing.T) {
	au := installAudit(t)
	au.DeploymentYAML = "profiles:\n  security: {frameworks: [security]}\n  payments: {frameworks: [pci-dss]}\n"
	au.Presets = map[string]arp.AuditPreset{
		"standard": {PresetStorage: auditpulumi.PresetStorage{Prefix: "standard/", Create: true}},
		"attested": {PresetStorage: auditpulumi.PresetStorage{Prefix: "attested/", Create: true}},
	}
	au.Archive = arp.AuditArchiveArgs{AcknowledgeCompliance: true, DefaultRetentionDays: 30}
	au.Notary.Disabled = true
	rec, _ := mustLambda(t, auditEstate(t, au, nil))
	rec.one(t, "aws:s3/bucket:Bucket", "audit-example-archive-standard")
	rec.one(t, "aws:s3/bucket:Bucket", "audit-example-archive-attested")
	dep := auditLayer(t, rec)["audit/deployment.yaml"]
	for _, want := range []string{"standard:", "attested:", "prefix: standard/", "prefix: attested/"} {
		if !strings.Contains(dep, want) {
			t.Errorf("deployment document lacks %q:\n%s", want, dep)
		}
	}
}

// Profiles that are not the default need their presets named.
func TestAuditProfilesNeedPresets(t *testing.T) {
	au := installAudit(t)
	au.Profiles = map[string][]string{"security": {"security"}}
	_, _, err := buildLambda(t, auditEstate(t, au, nil))
	if err == nil || !strings.Contains(err.Error(), "Presets") {
		t.Errorf("profiles with no presets: %v", err)
	}
}
