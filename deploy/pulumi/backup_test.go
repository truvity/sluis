package sluispulumi_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	yaml "go.yaml.in/yaml/v3"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

const hour = time.Hour

const archiveBucket = "acme-sluis-archive"

// backupCommon is an installation on layout v5 with its six tables.
func backupCommon(t *testing.T) arp.BackupCommon {
	t.Helper()
	pkg := zipFile(t, nil)
	tables := map[arp.Module]pulumi.StringInput{}
	for _, m := range arp.Modules() {
		tables[m] = pulumi.String(arnp + "dynamodb:" + region + ":" + account + ":table/sluis-staging-" + string(m))
	}
	return arp.BackupCommon{
		Region: region, AccountID: account, Instance: "staging",
		Package: pkg, PackageSHA256: sha(t, pkg),
		Storage:         &arp.StorageGrant{BucketArn: pulumi.String(arnp + "s3:::" + bucket)},
		BlobBucketName:  bucket,
		State:           &arp.StateGrant{Tables: tables},
		ArchiveKeyAlias: "alias/staging-archive",
		Archive:         arp.ArchiveArgs{Bucket: archiveBucket, Prefix: "vault"},
		AuditQueueURL:   "https://sqs." + region + ".amazonaws.com/" + account + "/audit-ingest",
	}
}

func buildBackup(t *testing.T, mutate func(*arp.BackupArgs)) (*recorder, map[string]string, error) {
	t.Helper()
	a := &arp.BackupArgs{BackupCommon: backupCommon(t)}
	if mutate != nil {
		mutate(a)
	}
	return run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		b, err := arp.NewBackup(ctx, "staging-backup", a)
		if err != nil {
			return err
		}
		collect("functionArn", b.FunctionArn)
		collect("roleArn", b.RoleArn)
		collect("liveAliasArn", b.LiveAliasArn)
		collect("aliases", b.AliasArns.ApplyT(func(m map[string]string) string { raw, _ := json.Marshal(m); return string(raw) }).(pulumi.StringOutput))
		collect("scheduleNames", b.ScheduleNames.ApplyT(func(v []string) string { return strings.Join(v, ",") }).(pulumi.StringOutput))
		collect("dlqArn", b.ScheduleDLQArn)
		collect("bucketName", b.ArchiveBucketName)
		collect("bucketArn", b.ArchiveBucketArn)
		collect("bucketPolicy", b.ArchiveBucketPolicy)
		collect("keyArn", b.ArchiveKeyArn)
		collect("alarms", b.AlarmNames.ApplyT(func(v []string) string { return strings.Join(v, ",") }).(pulumi.StringOutput))
		return nil
	})
}

func mustBackup(t *testing.T, mutate func(*arp.BackupArgs)) (*recorder, map[string]string) {
	t.Helper()
	rec, out, err := buildBackup(t, mutate)
	if err != nil {
		t.Fatal(err)
	}
	return rec, out
}

// layerDocument is the `sluis-backup/v1` document the function's layer holds.
func layerDocument(t *testing.T, rec *recorder, layer string) map[string]any {
	t.Helper()
	code := prop(rec.one(t, "aws:lambda/layerVersion:LayerVersion", layer), "code")
	if !code.IsArchive() {
		t.Fatalf("the layer's code is not an archive: %v", code)
	}
	assets := code.ArchiveValue().Assets
	if len(assets) != 1 {
		t.Fatalf("the layer holds %d files, want the one document", len(assets))
	}
	a, ok := assets["sluis/sluis.yaml"].(*resource.Asset)
	if !ok {
		t.Fatalf("the layer has no sluis/sluis.yaml: %v", assets)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(a.Text), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func at(t *testing.T, m any, path ...string) any {
	t.Helper()
	for _, k := range path {
		mm, ok := m.(map[string]any)
		if !ok {
			t.Fatalf("%v: not an object at %s", path, k)
		}
		m = mm[k]
	}
	return m
}

func TestTheBackupFunctionIsTheReleaseZipWithTheBackupDocument(t *testing.T) {
	rec, out := mustBackup(t, nil)
	fn := rec.one(t, fnType, "staging-backup-fn")
	if got := prop(fn, "name").StringValue(); got != "sluis-staging-backup" {
		t.Errorf("function name = %q", got)
	}
	if got := prop(fn, "timeout").NumberValue(); got != 900 {
		t.Errorf("timeout = %v, want 900", got)
	}
	if got := prop(fn, "memorySize").NumberValue(); got != 512 {
		t.Errorf("memory = %v", got)
	}
	if !prop(fn, "publish").BoolValue() {
		t.Error("the function does not publish a version")
	}
	if prop(fn, "reservedConcurrentExecutions").HasValue() && !prop(fn, "reservedConcurrentExecutions").IsNull() {
		t.Error("the backup function reserves concurrency: the lease is the guard")
	}
	if got := packagePath(t, fn); got == "" {
		t.Error("no package")
	}
	if got := prop(fn, "environment").ObjectValue()["variables"].ObjectValue()["SLUIS_CONFIG"].StringValue(); got != "/opt/sluis/sluis.yaml" {
		t.Errorf("SLUIS_CONFIG = %q", got)
	}
	if got := len(prop(fn, "layers").ArrayValue()); got != 1 {
		t.Errorf("layers = %d, want the configuration layer only", got)
	}

	doc := layerDocument(t, rec, "staging-backup-config")
	if got := doc["apiVersion"]; got != "sluis.truvity.github.io/sluis-backup/v1" {
		t.Errorf("apiVersion = %v", got)
	}
	if at(t, doc, "secrets", "layout") != "v5" || at(t, doc, "secrets", "source") != "ssm" || at(t, doc, "secrets", "root") != "/sluis/staging" {
		t.Errorf("secrets = %v", doc["secrets"])
	}
	tables := at(t, doc, "ports", "dynamodb", "tables").(map[string]any)
	if len(tables) != 6 {
		t.Errorf("ports.dynamodb.tables = %v, want all six modules", tables)
	}
	for _, m := range arp.Modules() {
		if tables[string(m)] != "sluis-staging-"+string(m) {
			t.Errorf("table of %s = %v", m, tables[string(m)])
		}
	}
	if at(t, doc, "ports", "blob", "s3", "bucket") != bucket {
		t.Errorf("ports.blob = %v", at(t, doc, "ports", "blob"))
	}
	bk := doc["backup"].(map[string]any)
	if bk["key"] != "alias/staging-archive" {
		t.Errorf("backup.key = %v", bk["key"])
	}
	if _, set := bk["role"]; set {
		t.Errorf("backup.role = %v: the default role is the backup's", bk["role"])
	}
	tg := bk["target"].(map[string]any)
	if tg["bucket"] != archiveBucket || tg["prefix"] != "vault" || tg["region"] != region {
		t.Errorf("backup.target = %v", tg)
	}
	if at(t, bk, "retention", "maxAge") != "720h" || at(t, bk, "retention", "keep") != 7 {
		t.Errorf("backup.retention = %v", bk["retention"])
	}
	if at(t, doc, "adapters", "audit", "adapter") != "sqs" {
		t.Errorf("adapters.audit = %v", at(t, doc, "adapters", "audit"))
	}
	if !strings.HasSuffix(out["functionArn"], ":function:sluis-staging-backup") {
		t.Errorf("functionArn = %s", out["functionArn"])
	}
}

func TestTheBackupDocumentWithoutAuditLogsTheRecords(t *testing.T) {
	rec, _ := mustBackup(t, func(a *arp.BackupArgs) { a.AuditQueueURL = "" })
	doc := layerDocument(t, rec, "staging-backup-config")
	if at(t, doc, "adapters", "audit", "adapter") != "log" {
		t.Errorf("adapters.audit = %v", at(t, doc, "adapters", "audit"))
	}
}

func TestTheBackupFunctionHasOneAliasPerCallerClassAndLive(t *testing.T) {
	rec, out := mustBackup(t, nil)
	var got map[string]string
	if err := json.Unmarshal([]byte(out["aliases"]), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"live", "live-console", "live-admin", "live-breakglass"}
	if len(got) != len(want) {
		t.Errorf("aliases = %v", got)
	}
	for _, name := range want {
		if !strings.HasSuffix(got[name], ":function:sluis-staging-backup:"+name) {
			t.Errorf("alias %s = %q", name, got[name])
		}
		a := rec.one(t, "aws:lambda/alias:Alias", "staging-backup-alias-"+name)
		if prop(a, "name").StringValue() != name || prop(a, "functionVersion").StringValue() != "7" {
			t.Errorf("alias %s: %v", name, a.Inputs)
		}
	}
	if got["live"] != out["liveAliasArn"] {
		t.Error("LiveAliasArn is not the live alias")
	}
	if arp.CallerAlias(arp.ClassAdmin) != "live-admin" {
		t.Error("CallerAlias")
	}
}

func TestTheBackupSchedulesSendTheEventsAndHaveADeadLetterQueue(t *testing.T) {
	rec, out := mustBackup(t, nil)
	if out["scheduleNames"] != "sluis-staging-backup-daily,sluis-staging-backup-resume" {
		t.Errorf("schedules = %s", out["scheduleNames"])
	}
	for res, want := range map[string]struct{ expr, input string }{
		"staging-backup-daily":  {"cron(0 2 * * ? *)", `{"kind":"backup"}`},
		"staging-backup-resume": {"rate(5 minutes)", `{"kind":"backup","resume":true}`},
	} {
		s := rec.one(t, "aws:scheduler/schedule:Schedule", res)
		if prop(s, "scheduleExpression").StringValue() != want.expr {
			t.Errorf("%s: expression %v", res, prop(s, "scheduleExpression"))
		}
		tg := prop(s, "target").ObjectValue()
		if tg["input"].StringValue() != want.input {
			t.Errorf("%s: input %v", res, tg["input"])
		}
		if tg["arn"].StringValue() != out["liveAliasArn"] {
			t.Errorf("%s: target %v, want the live alias", res, tg["arn"])
		}
		if tg["deadLetterConfig"].ObjectValue()["arn"].StringValue() != out["dlqArn"] {
			t.Errorf("%s: no dead-letter queue", res)
		}
		if prop(s, "state").HasValue() && prop(s, "state").IsString() {
			t.Errorf("%s: state %v", res, prop(s, "state"))
		}
	}
	dlq := rec.one(t, "aws:sqs/queue:Queue", "staging-backup-schedule-dlq")
	if !prop(dlq, "sqsManagedSseEnabled").BoolValue() {
		t.Error("the dead-letter queue is not encrypted")
	}
	// The scheduler role may invoke the live alias and send to the queue, and nothing else.
	pol := grants(statements(t, prop(rec.one(t, policyType, "staging-backup-scheduler-policy"), "policy").StringValue()))
	if len(pol) != 2 || pol["lambda:InvokeFunction"][0] != out["liveAliasArn"] || pol["sqs:SendMessage"][0] != out["dlqArn"] {
		t.Errorf("scheduler grants = %v", pol)
	}
}

func TestPausedBackupSchedulesAreDeclaredDisabled(t *testing.T) {
	rec, _ := mustBackup(t, func(a *arp.BackupArgs) {
		a.Schedule = arp.BackupScheduleArgs{Daily: "cron(30 3 * * ? *)", Paused: true}
	})
	for _, res := range []string{"staging-backup-daily", "staging-backup-resume"} {
		if got := prop(rec.one(t, "aws:scheduler/schedule:Schedule", res), "state").StringValue(); got != "DISABLED" {
			t.Errorf("%s state = %q", res, got)
		}
	}
	if got := prop(rec.one(t, "aws:scheduler/schedule:Schedule", "staging-backup-daily"), "scheduleExpression").StringValue(); got != "cron(30 3 * * ? *)" {
		t.Errorf("daily = %q", got)
	}
}

func TestTheBackupAlarms(t *testing.T) {
	rec, out := mustBackup(t, func(a *arp.BackupArgs) {
		a.AlarmActionArns = []string{arnp + "sns:" + region + ":" + account + ":oncall"}
	})
	if out["alarms"] != "sluis-staging-backup-no-backup-36h,sluis-staging-backup-failed-run,sluis-staging-backup-schedule-dlq" {
		t.Errorf("alarms = %s", out["alarms"])
	}
	filter := rec.one(t, "aws:cloudwatch/logMetricFilter:LogMetricFilter", "staging-backup-completed-filter")
	if prop(filter, "pattern").StringValue() != `"a backup was written"` {
		t.Errorf("filter pattern = %v", prop(filter, "pattern"))
	}
	none := rec.one(t, "aws:cloudwatch/metricAlarm:MetricAlarm", "staging-backup-alarm-no-backup")
	if prop(none, "period").NumberValue()*prop(none, "evaluationPeriods").NumberValue() != 36*3600 ||
		prop(none, "datapointsToAlarm").NumberValue() != 36 || prop(none, "comparisonOperator").StringValue() != "LessThanThreshold" ||
		prop(none, "treatMissingData").StringValue() != "breaching" || prop(none, "threshold").NumberValue() != 1 ||
		prop(none, "namespace").StringValue() != "Sluis/Backup/staging" || prop(none, "metricName").StringValue() != "Completed" {
		t.Errorf("no-backup alarm: %v", none.Inputs)
	}
	failed := rec.one(t, "aws:cloudwatch/metricAlarm:MetricAlarm", "staging-backup-alarm-failed")
	if prop(failed, "namespace").StringValue() != "AWS/Lambda" || prop(failed, "metricName").StringValue() != "Errors" ||
		prop(failed, "comparisonOperator").StringValue() != "GreaterThanOrEqualToThreshold" || prop(failed, "treatMissingData").StringValue() != "notBreaching" {
		t.Errorf("failed-run alarm: %v", failed.Inputs)
	}
	for _, a := range rec.ofType("aws:cloudwatch/metricAlarm:MetricAlarm") {
		acts := prop(a, "alarmActions").ArrayValue()
		if len(acts) != 1 || acts[0].StringValue() != arnp+"sns:"+region+":"+account+":oncall" {
			t.Errorf("%s: alarm actions %v", a.Name, acts)
		}
	}
	if n := len(rec.ofType("aws:cloudwatch/metricAlarm:MetricAlarm")); n != 3 {
		t.Errorf("%d alarms, want 3", n)
	}
}

func TestTheBackupRolePolicyIsTheOneBackupRoleStatementsRenders(t *testing.T) {
	rec, out := mustBackup(t, nil)
	doc := prop(rec.one(t, policyType, "staging-backup-policy"), "policy").StringValue()
	st := statements(t, doc)
	sids := map[string]bool{}
	for _, s := range st {
		sids[s["Sid"].(string)] = true
	}
	for _, want := range []string{
		"SluisTableBackup", "SluisCrossOidcTable", "SluisCrossGoogleParameters", "SluisMaintenanceDeny",
		"SluisArchiveObjects", "SluisArchiveList", "SluisArchiveKey",
	} {
		if !sids[want] {
			t.Errorf("no %s in %v", want, sids)
		}
	}
	g := grants(st)
	if got := g["s3:PutObject"]; !contains(got, arnp+"s3:::"+archiveBucket+"/vault/backup/staging/*") {
		t.Errorf("put grants = %v", got)
	}
	if got := g["kms:GenerateDataKey"]; !contains(got, out["keyArn"]) || !strings.HasSuffix(out["keyArn"], "/staging-archive") {
		t.Errorf("archive key grants = %v (key %s)", got, out["keyArn"])
	}
	if got := g["sqs:SendMessage"]; len(got) != 1 || !strings.HasSuffix(got[0], ":audit-ingest") {
		t.Errorf("audit grants = %v", got)
	}
	if !strings.Contains(doc, "kms:EncryptionContext:purpose") || !strings.Contains(doc, `"archive"`) {
		t.Error("the archive key has no encryption-context condition")
	}
	// The log group is its own.
	if got := g["logs:PutLogEvents"]; len(got) != 1 || !strings.HasSuffix(got[0], "log-group:/aws/lambda/sluis-staging-backup:*") {
		t.Errorf("log grants = %v", got)
	}
}

func TestAnExistingArchiveBucketIsNotCreated(t *testing.T) {
	rec, out := mustBackup(t, nil)
	if len(rec.ofType("aws:s3/bucket:Bucket")) != 0 || len(rec.ofType("aws:s3/bucketObjectLockConfiguration:BucketObjectLockConfiguration")) != 0 {
		t.Errorf("a bucket was declared: %v", rec.names())
	}
	if out["bucketName"] != archiveBucket || out["bucketArn"] != arnp+"s3:::"+archiveBucket {
		t.Errorf("bucket outputs = %s %s", out["bucketName"], out["bucketArn"])
	}
}

func TestTheCreatedArchiveBucketIsVersionedLockedAndNoLongerThanMaxAge(t *testing.T) {
	rec, out := mustBackup(t, func(a *arp.BackupArgs) { a.Archive.Create = &arp.ArchiveBucketArgs{} })
	if !rec.isProtected("aws:s3/bucket:Bucket", "staging-backup-archive") {
		t.Error("the archive bucket is not protected")
	}
	v := prop(rec.one(t, "aws:s3/bucketVersioning:BucketVersioning", "staging-backup-archive-versioning"), "versioningConfiguration").ObjectValue()
	if v["status"].StringValue() != "Enabled" {
		t.Errorf("versioning = %v", v)
	}
	lock := rec.one(t, "aws:s3/bucketObjectLockConfiguration:BucketObjectLockConfiguration", "staging-backup-archive-lock")
	dr := prop(lock, "rule").ObjectValue()["defaultRetention"].ObjectValue()
	if prop(lock, "objectLockEnabled").StringValue() != "Enabled" || dr["mode"].StringValue() != "GOVERNANCE" || dr["days"].NumberValue() != 30 {
		t.Errorf("lock = %v", lock.Inputs)
	}
	pab := rec.one(t, "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock", "staging-backup-archive-public-access")
	for _, k := range []string{"blockPublicAcls", "blockPublicPolicy", "ignorePublicAcls", "restrictPublicBuckets"} {
		if !prop(pab, k).BoolValue() {
			t.Errorf("%s is off", k)
		}
	}
	pol := prop(rec.one(t, "aws:s3/bucketPolicy:BucketPolicy", "staging-backup-archive-policy"), "policy").StringValue()
	if !strings.Contains(pol, "aws:SecureTransport") {
		t.Errorf("no TLS-only deny: %s", pol)
	}
	if len(rec.ofType("aws:s3/bucketServerSideEncryptionConfiguration:BucketServerSideEncryptionConfiguration")) != 1 {
		t.Error("the bucket is not encrypted")
	}
	lc := rec.one(t, "aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration", "staging-backup-archive-lifecycle")
	if rules := prop(lc, "rules").ArrayValue(); len(rules) != 2 ||
		rules[0].ObjectValue()["noncurrentVersionExpiration"].ObjectValue()["noncurrentDays"].NumberValue() != 31 {
		t.Errorf("lifecycle = %v", prop(lc, "rules"))
	}
	if out["bucketName"] != archiveBucket {
		t.Errorf("bucket = %s", out["bucketName"])
	}
}

func TestTheArchiveLockFollowsMaxAgeAndNeverExceedsIt(t *testing.T) {
	rec, _ := mustBackup(t, func(a *arp.BackupArgs) {
		a.Archive.Create = &arp.ArchiveBucketArgs{}
		a.Retention = &arp.BackupRetentionArgs{Keep: 3, MaxAge: 240 * hour}
	})
	lock := rec.one(t, "aws:s3/bucketObjectLockConfiguration:BucketObjectLockConfiguration", "staging-backup-archive-lock")
	if d := prop(lock, "rule").ObjectValue()["defaultRetention"].ObjectValue()["days"].NumberValue(); d != 10 {
		t.Errorf("default retention = %v days, want 10", d)
	}
	doc := layerDocument(t, rec, "staging-backup-config")
	if at(t, doc, "backup", "retention", "maxAge") != "240h" || at(t, doc, "backup", "retention", "keep") != 3 {
		t.Errorf("retention = %v", at(t, doc, "backup", "retention"))
	}
	for name, mut := range map[string]func(*arp.BackupArgs){
		"retention beyond maxAge": func(a *arp.BackupArgs) { a.Archive.Create = &arp.ArchiveBucketArgs{RetentionDays: 31} },
		"maxAge under a day": func(a *arp.BackupArgs) {
			a.Archive.Create = &arp.ArchiveBucketArgs{}
			a.Retention = &arp.BackupRetentionArgs{MaxAge: 12 * hour}
		},
		"compliance unacknowledged": func(a *arp.BackupArgs) { a.Archive.Create = &arp.ArchiveBucketArgs{Mode: "COMPLIANCE"} },
		"unknown mode":              func(a *arp.BackupArgs) { a.Archive.Create = &arp.ArchiveBucketArgs{Mode: "LOOSE"} },
		"unversioned lock": func(a *arp.BackupArgs) {
			f := false
			a.Archive.Create = &arp.ArchiveBucketArgs{}
			a.Archive.Versioned = &f
		},
		"another account's bucket": func(a *arp.BackupArgs) {
			a.Archive.Create = &arp.ArchiveBucketArgs{}
			a.Archive.AccountID = strings.Repeat("2", 12)
		},
	} {
		if _, _, err := buildBackup(t, mut); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := buildBackup(t, func(a *arp.BackupArgs) {
		a.Archive.Create = &arp.ArchiveBucketArgs{Mode: "COMPLIANCE", AcknowledgeCompliance: true, RetentionDays: 30}
	}); err != nil {
		t.Errorf("acknowledged COMPLIANCE: %v", err)
	}
}

func TestTheCrossAccountArchiveGetsABucketPolicyToHandOver(t *testing.T) {
	_, out := mustBackup(t, func(a *arp.BackupArgs) {
		a.Archive.AccountID = strings.Repeat("2", 12)
		a.Archive.SSEKeyArn = arnp + "kms:" + region + ":" + strings.Repeat("2", 12) + ":key/archive-sse"
	})
	var pol struct{ Statement []map[string]any }
	if err := json.Unmarshal([]byte(out["bucketPolicy"]), &pol); err != nil {
		t.Fatal(err)
	}
	roles := map[string]bool{}
	for _, s := range pol.Statement {
		roles[s["Principal"].(map[string]any)["AWS"].(string)] = true
	}
	want := map[string]bool{out["roleArn"]: true, arnp + "iam::" + account + ":role/sluis-staging-restore": true}
	if len(roles) != 2 || !roles[out["roleArn"]] || !roles[arnp+"iam::"+account+":role/sluis-staging-restore"] {
		t.Errorf("principals = %v, want %v", roles, want)
	}
	if strings.Contains(out["bucketPolicy"], `"*"`) && strings.Contains(out["bucketPolicy"], `"Principal":"*"`) {
		t.Error("the bucket policy names every principal")
	}
}

func TestBackupArgumentsAreHeldToWhatTheFunctionNeeds(t *testing.T) {
	for name, mut := range map[string]func(*arp.BackupArgs){
		"no region":            func(a *arp.BackupArgs) { a.Region = "" },
		"no archive bucket":    func(a *arp.BackupArgs) { a.Archive.Bucket = "" },
		"key by arn":           func(a *arp.BackupArgs) { a.ArchiveKeyAlias = arnp + "kms:" + region + ":" + account + ":key/x" },
		"aws alias":            func(a *arp.BackupArgs) { a.ArchiveKeyAlias = "alias/aws/s3" },
		"a table missing":      func(a *arp.BackupArgs) { delete(a.State.Tables, arp.ModuleGoogle) },
		"no state":             func(a *arp.BackupArgs) { a.State = nil },
		"no blob bucket":       func(a *arp.BackupArgs) { a.BlobBucketName = "" },
		"bad instance":         func(a *arp.BackupArgs) { a.Instance = "Private_X" },
		"timeout over 900":     func(a *arp.BackupArgs) { a.Function.TimeoutSeconds = 901 },
		"bad schedule":         func(a *arp.BackupArgs) { a.Schedule.Daily = "daily" },
		"not an SQS url":       func(a *arp.BackupArgs) { a.AuditQueueURL = "https://example.test/q" },
		"package without hash": func(a *arp.BackupArgs) { a.PackageSHA256 = "" },
		"unknown table module": func(a *arp.BackupArgs) { a.TableNames = map[arp.Module]string{"nope": "x"} },
	} {
		if _, _, err := buildBackup(t, mut); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		_, err := arp.NewBackup(ctx, "x", nil)
		return err
	}); err == nil {
		t.Error("nil args accepted")
	}
}

func TestBackupDocumentOnExternalBlobsNamesTheStoreAndItsCredentials(t *testing.T) {
	rec, _ := mustBackup(t, func(a *arp.BackupArgs) {
		a.BlobBucketName = ""
		a.Storage = &arp.StorageGrant{External: &arp.ExternalBlobs{
			Bucket: "r2-blobs", Endpoint: "https://r2.example.test", CredentialsRef: "internal/google/blobs-r2",
		}}
	})
	doc := layerDocument(t, rec, "staging-backup-config")
	s3 := at(t, doc, "ports", "blob", "s3").(map[string]any)
	if s3["bucket"] != "r2-blobs" || s3["credentialsRef"] != "internal/google/blobs-r2" {
		t.Errorf("ports.blob.s3 = %v", s3)
	}
	// The credential lives under a module the role reads whole.
	st := statements(t, prop(rec.one(t, policyType, "staging-backup-policy"), "policy").StringValue())
	if g := grants(st)["ssm:GetParameter"]; !contains(g, arnp+"ssm:"+region+":"+account+":parameter/sluis/staging/internal/google/*") {
		t.Errorf("ssm grants = %v", g)
	}
}

func TestTheBackupFunctionFromTheArtifactsBucket(t *testing.T) {
	rec, _ := mustBackup(t, func(a *arp.BackupArgs) { a.Artifacts = &arp.ArtifactsArgs{Bucket: "acme-artifacts"} })
	fn := rec.one(t, fnType, "staging-backup-fn")
	if prop(fn, "s3Bucket").StringValue() != "acme-artifacts" || !strings.HasPrefix(prop(fn, "s3ObjectVersion").StringValue(), "ver-") {
		t.Errorf("function code: %v", fn.Inputs)
	}
	layer := rec.one(t, "aws:lambda/layerVersion:LayerVersion", "staging-backup-config")
	if prop(layer, "s3Bucket").StringValue() != "acme-artifacts" {
		t.Errorf("layer code: %v", layer.Inputs)
	}
}
