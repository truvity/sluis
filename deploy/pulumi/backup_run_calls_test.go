package sluispulumi

import (
	"encoding/json"
	"testing"
	"unicode"

	"github.com/truvity/sluis/deploy/pulumi/internal/iameval"
)

// The ARNs of a realistic installation: a real region, a twelve-digit account,
// a short instance name and key ARNs with their UUIDs, so that a size measured
// on them is what IAM measures on a real stack.
const (
	rcRegion   = "eu-central-1"
	rcAccount  = "123456789012"
	rcInst     = "prod"
	rcKey      = arnPrefix + "kms:eu-central-1:123456789012:key/0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b"
	rcTableKey = arnPrefix + "kms:eu-central-1:123456789012:key/1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
	rcSSEKey   = arnPrefix + "kms:eu-central-1:123456789012:key/2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e"
	rcBucket   = arnPrefix + "s3:::sluis-prod-blobs-123456789012"
	rcArchive  = arnPrefix + "s3:::sluis-prod-archive-123456789012"
	rcQueue    = arnPrefix + "sqs:eu-central-1:123456789012:sluis-prod-audit"
)

func rcEnv() ModuleEnv {
	tables := map[Module]string{}
	for _, m := range Modules() {
		tables[m] = arnPrefix + "dynamodb:" + rcRegion + ":" + rcAccount + ":table/sluis-" + rcInst + "-" + string(m)
	}
	logs, fns := map[string]string{}, map[string]string{}
	for _, r := range []string{RoleBackup, RoleRestore} {
		logs[r] = arnPrefix + "logs:" + rcRegion + ":" + rcAccount + ":log-group:/aws/lambda/sluis-" + rcInst + "-" + r
		fns[r] = arnPrefix + "lambda:" + rcRegion + ":" + rcAccount + ":function:sluis-" + rcInst + "-" + r
	}
	return ModuleEnv{
		Region: rcRegion, Account: rcAccount, Modules: ModuleSet{Instance: rcInst},
		BucketArn: rcBucket, TableArns: tables, TableKeyArn: rcTableKey, ParameterKeyArn: rcKey,
		QueueArn: rcQueue, LogGroupArns: logs, FunctionArns: fns,
		MinterRefs:         []string{"internal/cloudflare/*/minter"},
		TelemetryAudiences: []string{"https://otlp.collector.example.com"},
	}
}

func rcArchiveGrant() ArchiveGrant {
	return ArchiveGrant{BucketArn: rcArchive, Prefix: "sluis/", Instance: rcInst, SSEKeyArn: rcSSEKey, Versioned: true}
}

// iamInlineRoleLimit is IAM's limit on the inline policies of one role, in
// characters, whitespace not counted.
const iamInlineRoleLimit = 10240

func iamSize(doc string) int {
	n := 0
	for _, r := range doc {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// The backup and restore roles carry the largest inline policies (a grant per
// module): on a realistic installation each must stay within IAM's limit, or
// the apply that adds a statement is the one that fails. The margin is thin
// (the log says how thin): a grant added to these roles is added compactly.
func TestBackupAndRestoreRolePoliciesFitTheInlineLimit(t *testing.T) {
	env, g := rcEnv(), rcArchiveGrant()
	bst, err := BackupRoleStatements(env, g)
	if err != nil {
		t.Fatal(err)
	}
	rst, err := RestoreRoleStatements(env, g, []string{env.FunctionArns[RoleRestore] + ":live-console"})
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string][]map[string]any{RoleBackup: bst, RoleRestore: rst} {
		raw, err := json.Marshal(map[string]any{"Version": polVersion, "Statement": st})
		if err != nil {
			t.Fatal(err)
		}
		n := iamSize(string(raw))
		t.Logf("%s: %d of IAM's %d characters", name, n, iamInlineRoleLimit)
		if n > iamInlineRoleLimit {
			t.Errorf("%s: the role policy is %d characters, over IAM's inline limit of %d", name, n, iamInlineRoleLimit)
		}
	}
}

// rcCall is one AWS call of a backup run, as IAM sees it.
type rcCall struct {
	step, action, resource string
	ctx                    map[string][]string
	ec                     map[string]string // a KMS encryption context
	key                    bool              // judged by the shared key's policy too
}

// backupRunCalls is every AWS call the backup function makes in one run
// (internal/backup/job, export, writer, prune; the ports under them), in
// order: the maintenance gate, the lease and the run records in the backup
// table, the export of every module (State by Query and Scan of the module's
// table, Index by Scan of the oidc table, the secrets listed then read by name,
// the reports listed then read), the archive object and its sidecar, the audit
// record, and retention (list, read the sidecars, delete). The backup module's
// own secrets are not exported, so internal/backup is never listed by the run.
func backupRunCalls() []rcCall {
	bare := func(ns, m string) string {
		return arnPrefix + "ssm:" + mxRegion + ":" + mxAccount + ":parameter/sluis/" + mxInst + "/" + ns + "/" + m
	}
	lk := func(p string) map[string][]string { return map[string][]string{"dynamodb:LeadingKeys": {p}} }
	pre := func(p string) map[string][]string { return map[string][]string{"s3:prefix": {p}} }
	obj := mxArchive + "/pre/backup/" + mxInst + "/20261013T020000Z-3fa9c1"
	calls := []rcCall{
		{step: "B1 maintenance gate", action: "dynamodb:GetItem", resource: mxTable(ModuleOIDC), ctx: lk(MaintenancePartition)},
		{step: "B1 maintenance gate, own table", action: "dynamodb:GetItem", resource: mxTable(ModuleBackup), ctx: lk(MaintenancePartition)},
		{step: "B2 lease acquire", action: "dynamodb:PutItem", resource: mxTable(ModuleBackup), ctx: lk("lease")},
		{step: "B3 lease read on conflict", action: "dynamodb:GetItem", resource: mxTable(ModuleBackup), ctx: lk("lease")},
		{step: "B4 run records", action: "dynamodb:Query", resource: mxTable(ModuleBackup), ctx: lk("run")},
		{step: "B5 run record", action: "dynamodb:PutItem", resource: mxTable(ModuleBackup), ctx: lk("run")},
		{step: "B7 exact-key family", action: "dynamodb:Scan", resource: mxTable(ModuleGitHub)},
		{step: "B8 index", action: "dynamodb:Scan", resource: mxTable(ModuleOIDC)},
		{step: "B11 reports list", action: "s3:ListBucket", resource: mxBucket, ctx: pre("github/reports/")},
		{step: "B11 reports list", action: "s3:ListBucket", resource: mxBucket, ctx: pre("slack/reports/")},
		{step: "B12 report read", action: "s3:GetObject", resource: mxBucket + "/github/reports/r1"},
		{step: "B12 report read", action: "s3:GetObject", resource: mxBucket + "/slack/reports/r1"},
		{step: "B13 archive object", action: "s3:PutObject", resource: obj + ".tar.gz.age"},
		{step: "B13 archive SSE", action: "kms:GenerateDataKey", resource: mxArchiveSSE,
			ctx: map[string][]string{"kms:ViaService": {"s3.r1.amazonaws.com"}}},
		{step: "B14 sidecar", action: "s3:PutObject", resource: obj + ".json"},
		{step: "B15 run completed", action: "dynamodb:PutItem", resource: mxTable(ModuleBackup), ctx: lk("run")},
		{step: "B16 audit", action: "sqs:SendMessage", resource: mxQueue},
		{step: "B17 retention list", action: "s3:ListBucket", resource: mxArchive, ctx: pre("pre/backup/" + mxInst + "/")},
		{step: "B18 sidecar read", action: "s3:GetObject", resource: obj + ".json"},
		{step: "B18 sidecar read SSE", action: "kms:Decrypt", resource: mxArchiveSSE,
			ctx: map[string][]string{"kms:ViaService": {"s3.r1.amazonaws.com"}}},
		{step: "B20 retention delete", action: "s3:DeleteObject", resource: obj + ".json"},
		{step: "B20 retention delete", action: "s3:DeleteObject", resource: obj + ".tar.gz.age"},
		{step: "B21 trim run records", action: "dynamodb:DeleteItem", resource: mxTable(ModuleBackup), ctx: lk("run")},
		{step: "B22 retention record", action: "dynamodb:PutItem", resource: mxTable(ModuleBackup), ctx: lk("retention")},
		{step: "B24 lease release", action: "dynamodb:DeleteItem", resource: mxTable(ModuleBackup), ctx: lk("lease")},
		{step: "logs", action: "logs:PutLogEvents", resource: arnPrefix + "logs:" + mxRegion + ":" + mxAccount + ":log-group:/aws/lambda/backup:log-stream:s"},
	}
	for _, m := range Modules() {
		if m == ModuleBackup {
			continue
		}
		ms := string(m)
		calls = append(calls, rcCall{step: "B6 state export " + ms, action: "dynamodb:Query", resource: mxTable(m), ctx: lk("rec")})
		nss := []string{"internal"}
		if m != ModuleGoogle {
			nss = append(nss, "external")
		}
		for _, ns := range nss {
			p := mxParam(ns, ms, "name/deeper")
			calls = append(calls,
				rcCall{step: "B9 list " + ns + "/" + ms, action: "ssm:GetParametersByPath", resource: bare(ns, ms)},
				rcCall{step: "B10 read " + ns + "/" + ms, action: "ssm:GetParameter", resource: p},
				rcCall{step: "B10 decrypt " + ns + "/" + ms, action: "kms:Decrypt", resource: mxKey, key: true,
					ctx: map[string][]string{"kms:ViaService": {mxSsmVia}}, ec: map[string]string{"PARAMETER_ARN": p}})
		}
	}
	return calls
}

const (
	mxArchive    = arnPrefix + "s3:::acme-archive"
	mxArchiveSSE = arnPrefix + "kms:r1:111122223333:key/archive-sse"
)

// Every call of a backup run is allowed by the backup role's policy (and, for
// the shared key, by the key's policy), so that a run does not discover a
// missing permission in production one call at a time; and what the run must
// never do (the restore identity, the namespace roots, a write outside its own
// table and archive path) stays refused.
func TestEveryCallOfABackupRunIsAllowed(t *testing.T) {
	_, key := mxPolicies(t)
	st, err := BackupRoleStatements(mxEnv(nil), ArchiveGrant{BucketArn: mxArchive, Prefix: "pre/", Instance: mxInst,
		SSEKeyArn: mxArchiveSSE, Versioned: true})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := iameval.Parse(st)
	if err != nil {
		t.Fatal(err)
	}
	decide := func(c rcCall) iameval.Decision {
		t.Helper()
		req := iameval.Request{Principal: mxRoleArn(RoleBackup), Action: c.action, Resource: c.resource}
		for k, v := range c.ctx {
			req = req.WithContext(k, v...)
		}
		if c.ec != nil {
			req = req.WithEncryptionContext(c.ec)
		}
		k := iameval.Policy{}
		if c.key {
			k = key
		}
		d, err := iameval.Evaluate(req, identity, k)
		if err != nil {
			t.Fatalf("%s: %v", c.step, err)
		}
		return d
	}
	calls := backupRunCalls()
	if len(calls) < 50 {
		t.Fatalf("the run has %d calls listed: an emptied list proves nothing", len(calls))
	}
	for _, c := range calls {
		if d := decide(c); !d.Allowed {
			t.Errorf("%s: %s on %s is refused (%s)", c.step, c.action, c.resource, d.Reason)
		}
	}

	identityArn := restoreIdentityArn(mxRegion, mxAccount, ModuleSet{Instance: mxInst})
	refused := []rcCall{
		{step: "the restore identity", action: "ssm:GetParameter", resource: identityArn},
		{step: "the restore identity, decrypted", action: "kms:Decrypt", resource: mxKey, key: true,
			ctx: map[string][]string{"kms:ViaService": {mxSsmVia}}, ec: map[string]string{"PARAMETER_ARN": identityArn}},
		{step: "write another module's parameter", action: "ssm:PutParameter", resource: mxParam("internal", "oidc", "k")},
		{step: "write another module's table", action: "dynamodb:PutItem", resource: mxTable(ModuleOIDC), ctx: map[string][]string{"dynamodb:LeadingKeys": {"rec"}}},
		{step: "write the maintenance flag", action: "dynamodb:PutItem", resource: mxTable(ModuleBackup),
			ctx: map[string][]string{"dynamodb:LeadingKeys": {MaintenancePartition}}},
		{step: "write outside the archive path", action: "s3:PutObject", resource: mxArchive + "/pre/backup/other/x"},
	}
	for _, ns := range []string{"internal", "external"} {
		refused = append(refused, rcCall{step: "list the " + ns + " root", action: "ssm:GetParametersByPath",
			resource: arnPrefix + "ssm:" + mxRegion + ":" + mxAccount + ":parameter/sluis/" + mxInst + "/" + ns})
	}
	refused = append(refused, rcCall{step: "list the installation root", action: "ssm:GetParametersByPath",
		resource: arnPrefix + "ssm:" + mxRegion + ":" + mxAccount + ":parameter/sluis/" + mxInst})
	for _, c := range refused {
		if d := decide(c); d.Allowed {
			t.Errorf("%s: %s on %s is allowed (%s)", c.step, c.action, c.resource, d.Reason)
		}
	}
	t.Logf("%d calls allowed, %d refused", len(calls), len(refused))
}
