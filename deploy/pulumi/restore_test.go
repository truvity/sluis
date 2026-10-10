package sluispulumi_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

var (
	adminRole      = arnp + "iam::" + account + ":role/operators-admin"
	breakglassRole = arnp + "iam::" + account + ":role/operators-breakglass"
)

func buildRestore(t *testing.T, mutate func(*arp.RestoreArgs)) (*recorder, map[string]string, error) {
	t.Helper()
	a := &arp.RestoreArgs{
		BackupCommon:    backupCommon(t),
		RestoreInvokers: arp.RestoreInvokers{AdminRoleArns: []string{adminRole}, BreakglassRoleArns: []string{breakglassRole}},
	}
	if mutate != nil {
		mutate(a)
	}
	return run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		r, err := arp.NewRestore(ctx, "staging-restore", a)
		if err != nil {
			return err
		}
		collect("functionArn", r.FunctionArn)
		collect("roleArn", r.RoleArn)
		collect("aliases", r.AliasArns.ApplyT(func(m map[string]string) string { raw, _ := json.Marshal(m); return string(raw) }).(pulumi.StringOutput))
		collect("alarms", r.AlarmNames.ApplyT(func(v []string) string { return strings.Join(v, ",") }).(pulumi.StringOutput))
		collect("keyArn", r.ArchiveKeyArn)
		return nil
	})
}

func mustRestore(t *testing.T, mutate func(*arp.RestoreArgs)) (*recorder, map[string]string) {
	t.Helper()
	rec, out, err := buildRestore(t, mutate)
	if err != nil {
		t.Fatal(err)
	}
	return rec, out
}

func TestTheRestoreFunctionIsTheReleaseZipWithTheRestoreDocument(t *testing.T) {
	rec, out := mustRestore(t, nil)
	fn := rec.one(t, fnType, "staging-restore-fn")
	if got := prop(fn, "name").StringValue(); got != "sluis-staging-restore" {
		t.Errorf("function name = %q", got)
	}
	if got := prop(fn, "timeout").NumberValue(); got != 900 {
		t.Errorf("timeout = %v", got)
	}
	if v := prop(fn, "reservedConcurrentExecutions"); v.HasValue() && !v.IsNull() {
		t.Errorf("reserved concurrency = %v: the lease allows one restore at a time", v)
	}
	if prop(fn, "environment").ObjectValue()["variables"].ObjectValue()["SLUIS_CONFIG"].StringValue() != "/opt/sluis/sluis.yaml" {
		t.Error("SLUIS_CONFIG is not the layer's document")
	}
	doc := layerDocument(t, rec, "staging-restore-config")
	if at(t, doc, "apiVersion") != "sluis.truvity.github.io/sluis-backup/v1" || at(t, doc, "backup", "role") != "restore" {
		t.Errorf("document = %v", doc)
	}
	if len(at(t, doc, "ports", "dynamodb", "tables").(map[string]any)) != 6 || at(t, doc, "secrets", "layout") != "v5" {
		t.Error("the restore document does not name the six tables on layout v5")
	}
	if at(t, doc, "backup", "target", "bucket") != archiveBucket || at(t, doc, "backup", "key") != "alias/staging-archive" {
		t.Errorf("backup = %v", doc["backup"])
	}
	if !strings.HasSuffix(out["functionArn"], ":function:sluis-staging-restore") {
		t.Errorf("functionArn = %s", out["functionArn"])
	}
}

func TestTheRestoreFunctionHasTheAdminAndBreakglassAliasesOnly(t *testing.T) {
	rec, out := mustRestore(t, nil)
	var got map[string]string
	if err := json.Unmarshal([]byte(out["aliases"]), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["live-admin"] == "" || got["live-breakglass"] == "" {
		t.Errorf("aliases = %v", got)
	}
	if n := len(rec.ofType("aws:lambda/alias:Alias")); n != 2 {
		t.Errorf("%d aliases declared", n)
	}
	// Both alias kinds are asynchronous without retry: a restore that failed is run again by a person.
	if n := len(rec.ofType("aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig")); n != 2 {
		t.Errorf("%d async configurations", n)
	}
	for _, c := range rec.ofType("aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig") {
		if prop(c, "maximumRetryAttempts").NumberValue() != 0 {
			t.Errorf("%s retries", c.Name)
		}
	}
}

func TestOnlyTheInvokersAreInTheRestoreResourcePolicy(t *testing.T) {
	rec, _ := mustRestore(t, func(a *arp.RestoreArgs) {
		a.RestoreInvokers.AdminRoleArns = []string{adminRole, arnp + "iam::" + account + ":role/operators-admin-2"}
	})
	perms := rec.ofType("aws:lambda/permission:Permission")
	if len(perms) != 3 {
		t.Fatalf("%d permissions, want one per invoker", len(perms))
	}
	byClass := map[string][]string{}
	for _, p := range perms {
		if prop(p, "action").StringValue() != "lambda:InvokeFunction" {
			t.Errorf("%s: action %v", p.Name, prop(p, "action"))
		}
		principal := prop(p, "principal").StringValue()
		if principal == "*" || !strings.Contains(principal, ":role/") {
			t.Errorf("%s: principal %q", p.Name, principal)
		}
		if set := func(k string) bool { v := prop(p, k); return v.HasValue() && !v.IsNull() }; set("functionUrlAuthType") || set("sourceArn") {
			t.Errorf("%s: more than a principal and an alias", p.Name)
		}
		q := prop(p, "qualifier")
		if !q.IsString() {
			t.Errorf("%s: no qualifier: an unqualified invoke would be allowed", p.Name)
			continue
		}
		byClass[q.StringValue()] = append(byClass[q.StringValue()], principal)
	}
	want := map[string][]string{
		"live-admin":      {adminRole, arnp + "iam::" + account + ":role/operators-admin-2"},
		"live-breakglass": {breakglassRole},
	}
	for class, principals := range byClass {
		slices.Sort(principals)
		w := want[class]
		slices.Sort(w)
		if !slices.Equal(principals, w) {
			t.Errorf("alias %s allows %v, want %v", class, principals, w)
		}
	}
	if len(byClass) != 2 {
		t.Errorf("permissions on %v", byClass)
	}
	// No service principal, no schedule: nothing invokes the restore but a person.
	if n := len(rec.ofType("aws:scheduler/schedule:Schedule")); n != 0 {
		t.Errorf("%d schedules", n)
	}
}

func TestTheRestoreRolePolicyIsTheOneRestoreRoleStatementsRenders(t *testing.T) {
	rec, out := mustRestore(t, nil)
	doc := prop(rec.one(t, policyType, "staging-restore-policy"), "policy").StringValue()
	st := statements(t, doc)
	g := grants(st)
	if got := g["lambda:InvokeFunction"]; len(got) != 2 || !strings.HasSuffix(got[0], ":function:sluis-staging-restore:live-admin") ||
		!strings.HasSuffix(got[1], ":function:sluis-staging-restore:live-breakglass") {
		t.Errorf("self-invoke grants = %v", got)
	}
	if got := g["s3:PutObject"]; contains(got, arnp+"s3:::"+archiveBucket+"/vault/backup/staging/*") {
		t.Errorf("the restore role writes the archive: %v", got)
	}
	if got := g["s3:GetObject"]; !contains(got, arnp+"s3:::"+archiveBucket+"/vault/backup/staging/*") {
		t.Errorf("the restore role does not read the archive: %v", got)
	}
	if got := g["kms:Decrypt"]; !contains(got, out["keyArn"]) {
		t.Errorf("decrypt grants = %v", got)
	}
	if contains(g["kms:GenerateDataKey"], out["keyArn"]) {
		t.Error("the restore role can seal a data key")
	}
	for _, s := range st {
		if s["Effect"] == "Deny" {
			t.Errorf("a deny in the restore role: %v", s["Sid"])
		}
	}
	for _, m := range arp.Modules() {
		table := arnp + "dynamodb:" + region + ":" + account + ":table/sluis-staging-" + string(m)
		if !contains(g["dynamodb:PutItem"], table) {
			t.Errorf("no write on the %s table: %v", m, g["dynamodb:PutItem"])
		}
	}
}

func TestTheRestoreAlarmFiresOnAnyInvocation(t *testing.T) {
	rec, out := mustRestore(t, func(a *arp.RestoreArgs) {
		a.AlarmActionArns = []string{arnp + "sns:" + region + ":" + account + ":oncall"}
	})
	if out["alarms"] != "sluis-staging-restore-invoked" {
		t.Errorf("alarms = %s", out["alarms"])
	}
	a := rec.one(t, "aws:cloudwatch/metricAlarm:MetricAlarm", "staging-restore-alarm-invoked")
	if prop(a, "namespace").StringValue() != "AWS/Lambda" || prop(a, "metricName").StringValue() != "Invocations" ||
		prop(a, "threshold").NumberValue() != 1 || prop(a, "comparisonOperator").StringValue() != "GreaterThanOrEqualToThreshold" ||
		prop(a, "period").NumberValue() != 60 || prop(a, "treatMissingData").StringValue() != "notBreaching" {
		t.Errorf("alarm: %v", a.Inputs)
	}
	if len(prop(a, "alarmActions").ArrayValue()) != 1 {
		t.Errorf("alarm actions: %v", prop(a, "alarmActions"))
	}
	if n := len(rec.ofType("aws:cloudwatch/metricAlarm:MetricAlarm")); n != 1 {
		t.Errorf("%d alarms", n)
	}
}

func TestTheRestoreTimeoutLeavesRoomForItsMargin(t *testing.T) {
	if arp.MinRestoreTimeoutSeconds != 180 || arp.RestoreMargin.Seconds() != 90 {
		t.Fatalf("margin %s, floor %d", arp.RestoreMargin, arp.MinRestoreTimeoutSeconds)
	}
	rec, _ := mustRestore(t, func(a *arp.RestoreArgs) { a.Function.TimeoutSeconds = 180 })
	if got := prop(rec.one(t, fnType, "staging-restore-fn"), "timeout").NumberValue(); got != 180 {
		t.Errorf("timeout = %v", got)
	}
	if _, _, err := buildRestore(t, func(a *arp.RestoreArgs) { a.Function.TimeoutSeconds = 179 }); err == nil {
		t.Error("a timeout under twice the margin was accepted")
	}
}

func TestRestoreArgumentsAreHeldToWhatTheResourcePolicyMayName(t *testing.T) {
	user := arnp + "iam::" + account + ":user/someone"
	for name, mut := range map[string]func(*arp.RestoreArgs){
		"no invokers":   func(a *arp.RestoreArgs) { a.RestoreInvokers = arp.RestoreInvokers{} },
		"no breakglass": func(a *arp.RestoreArgs) { a.RestoreInvokers.BreakglassRoleArns = nil },
		"no admin":      func(a *arp.RestoreArgs) { a.RestoreInvokers.AdminRoleArns = nil },
		"a wildcard":    func(a *arp.RestoreArgs) { a.RestoreInvokers.AdminRoleArns = []string{"*"} },
		"an account": func(a *arp.RestoreArgs) {
			a.RestoreInvokers.AdminRoleArns = []string{arnp + "iam::" + account + ":root"}
		},
		"a user":                 func(a *arp.RestoreArgs) { a.RestoreInvokers.BreakglassRoleArns = []string{user} },
		"one role in both":       func(a *arp.RestoreArgs) { a.RestoreInvokers.BreakglassRoleArns = []string{adminRole} },
		"the archive is created": func(a *arp.RestoreArgs) { a.Archive.Create = &arp.ArchiveBucketArgs{} },
		"no archive key":         func(a *arp.RestoreArgs) { a.ArchiveKeyAlias = "" },
		"a table missing":        func(a *arp.RestoreArgs) { delete(a.State.Tables, arp.ModuleBackup) },
	} {
		if _, _, err := buildRestore(t, mut); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		_, err := arp.NewRestore(ctx, "x", nil)
		return err
	}); err == nil {
		t.Error("nil args accepted")
	}
}

// Backup and restore are two functions of one stack: their resources do not
// collide, and their roles and layers are their own.
func TestBackupAndRestoreShareAStack(t *testing.T) {
	rec, _, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		b, err := arp.NewBackup(ctx, "staging-backup", &arp.BackupArgs{BackupCommon: backupCommon(t)})
		if err != nil {
			return err
		}
		r, err := arp.NewRestore(ctx, "staging-restore", &arp.RestoreArgs{
			BackupCommon:    backupCommon(t),
			RestoreInvokers: arp.RestoreInvokers{AdminRoleArns: []string{adminRole}, BreakglassRoleArns: []string{breakglassRole}},
		})
		if err != nil {
			return err
		}
		collect("backupRole", b.RoleArn)
		collect("restoreRole", r.RoleArn)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.ofType(fnType)); n != 2 {
		t.Errorf("%d functions", n)
	}
	if n := len(rec.ofType("aws:lambda/layerVersion:LayerVersion")); n != 2 {
		t.Errorf("%d layers", n)
	}
	b := layerDocument(t, rec, "staging-backup-config")
	r := layerDocument(t, rec, "staging-restore-config")
	if _, set := b["backup"].(map[string]any)["role"]; set || r["backup"].(map[string]any)["role"] != "restore" {
		t.Error("the two documents do not differ by backup.role")
	}
}
