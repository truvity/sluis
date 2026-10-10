package sluispulumi_test

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	arp "github.com/truvity/sluis/deploy/pulumi"
)

const (
	exportType    = "aws:dynamodb/tableExport:TableExport"
	bucketType    = "aws:s3/bucket:Bucket"
	lifecycleType = "aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration"
)

func legacy() *arp.LegacyStateArgs {
	return &arp.LegacyStateArgs{Name: "staging", StateArgs: arp.StateArgs{TableName: table}}
}

func retireArgs(l *arp.LegacyStateArgs, c *arp.CompatArgs) *arp.StatesArgs {
	return &arp.StatesArgs{Modules: arp.ModuleSet{Instance: "prod"}, Legacy: l, Compat: c}
}

// The first apply of the retirement: the table is exported to a bucket that
// expires the export, and only then is the table's deletion protection turned off
// and the resource unprotected. The module tables stay protected.
func TestRetiringTheLegacyTableExportsItAndUnprotectsIt(t *testing.T) {
	var arn string
	rec, out, err := run(t, func(ctx *pulumi.Context, collect func(string, pulumi.StringInput)) error {
		s, err := arp.NewStates(ctx, "access", retireArgs(legacy(), &arp.CompatArgs{DropV4: true, RetireLegacyTable: true}))
		if err != nil {
			return err
		}
		collect("backup", s.LegacyBackupArn)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	arn = out["backup"]

	exp := rec.one(t, exportType, "access-legacy-backup-export")
	if got := prop(exp, "tableArn").StringValue(); got != mockTableArn(table) {
		t.Errorf("the export is of %q, want the legacy table %q", got, mockTableArn(table))
	}
	prefix := prop(exp, "s3Prefix").StringValue()
	if !strings.HasPrefix(prefix, "pre-v5-retire-"+table+"-") || !strings.HasSuffix(prefix, "/") {
		t.Errorf("export prefix %q, want pre-v5-retire-%s-<date>/", prefix, table)
	}
	if arn == "" {
		t.Error("the export's ARN is not an output")
	}

	// The bucket is private, encrypted, and expires the export after 30 days.
	rec.one(t, bucketType, "access-legacy-backup")
	rec.one(t, "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock", "access-legacy-backup-public-access")
	rec.one(t, "aws:s3/bucketServerSideEncryptionConfigurationV2:BucketServerSideEncryptionConfigurationV2", "access-legacy-backup-encryption")
	rules := prop(rec.one(t, lifecycleType, "access-legacy-backup-lifecycle"), "rules").ArrayValue()
	if len(rules) != 1 || rules[0].ObjectValue()["expiration"].ObjectValue()["days"].NumberValue() != 30 {
		t.Errorf("lifecycle rules %v, want one that expires after 30 days", rules)
	}

	d := rec.one(t, tableType, "staging-table")
	if prop(d, "deletionProtectionEnabled").BoolValue() || rec.isProtected(tableType, "staging-table") {
		t.Error("the legacy table is still protected")
	}
	for _, m := range arp.Modules() {
		res := "access-" + string(m) + "-table"
		if !rec.isProtected(tableType, res) || !prop(rec.one(t, tableType, res), "deletionProtectionEnabled").BoolValue() {
			t.Errorf("the table of %s lost its protection", m)
		}
	}
}

// The apply after: Legacy is gone, the flag stays, nothing else changes.
func TestWithTheLegacyTableGoneRetiringDeclaresNothingMore(t *testing.T) {
	rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
		_, err := arp.NewStates(ctx, "access", retireArgs(nil, &arp.CompatArgs{DropV4: true, RetireLegacyTable: true}))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.has(tableType, "staging-table") || len(rec.ofType(exportType)) != 0 || len(rec.ofType(bucketType)) != 0 {
		t.Error("the second apply declares the legacy table or its export")
	}
	if got := len(rec.ofType(tableType)); got != len(arp.Modules()) {
		t.Errorf("%d tables, want the module tables only", got)
	}
}

func TestWithoutTheFlagTheLegacyTableStaysProtectedAndNothingIsExported(t *testing.T) {
	for name, c := range map[string]*arp.CompatArgs{"no compat": nil, "drop only": {DropV4: true}} {
		rec, _, err := run(t, func(ctx *pulumi.Context, _ func(string, pulumi.StringInput)) error {
			_, err := arp.NewStates(ctx, "access", retireArgs(legacy(), c))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !rec.isProtected(tableType, "staging-table") || !prop(rec.one(t, tableType, "staging-table"), "deletionProtectionEnabled").BoolValue() {
			t.Errorf("%s: the legacy table is not protected", name)
		}
		if len(rec.ofType(exportType)) != 0 {
			t.Errorf("%s: an export was taken", name)
		}
	}
}

func TestRetiringNeedsTheV4ParametersDropped(t *testing.T) {
	if _, err := statesRun(t, retireArgs(legacy(), &arp.CompatArgs{RetireLegacyTable: true})); err == nil ||
		!strings.Contains(err.Error(), "Compat.DropV4") {
		t.Errorf("States: RetireLegacyTable without DropV4 = %v", err)
	}
	for name, e := range map[string]estate{
		"without DropV4": withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) { a.Compat = &arp.CompatArgs{RetireLegacyTable: true} }),
		"on v4":          {mutate: func(a *arp.LambdaArgs) { a.Compat = &arp.CompatArgs{DropV4: true, RetireLegacyTable: true} }},
		"on v4+v5": withLayout(arp.LayoutV4V5, func(a *arp.LambdaArgs) {
			a.Compat = &arp.CompatArgs{DropV4: true, RetireLegacyTable: true}
		}),
	} {
		if _, _, err := buildLambda(t, e); err == nil {
			t.Errorf("the function took RetireLegacyTable %s", name)
		}
	}
}

// With the legacy table retiring the migration role has no part of it.
func TestRetiringTakesTheMigrationRolesReadOfTheLegacyTable(t *testing.T) {
	roleArn := arnp + "iam::" + account + ":role/ops/migration"
	rec, _ := mustLambda(t, withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) {
		a.MigrationRoleArn = roleArn
		a.Compat = &arp.CompatArgs{DropV4: true, RetireLegacyTable: true}
	}))
	if rec.has(policyType, "staging-migration-legacy-read") {
		t.Error("the migration role still reads the legacy table")
	}
	// And without it, it does (the rendering the other test pins).
	rec, _ = mustLambda(t, withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) {
		a.MigrationRoleArn = roleArn
		a.Compat = &arp.CompatArgs{DropV4: true}
	}))
	if !rec.has(policyType, "staging-migration-legacy-read") {
		t.Error("DropV4 alone took the migration role's read")
	}
	// No function role grant names the legacy table on v5, retiring or not.
	for _, r := range resourcesOf(policyOf(t, withLayout(arp.LayoutV5, func(a *arp.LambdaArgs) {
		a.Compat = &arp.CompatArgs{DropV4: true, RetireLegacyTable: true}
	}))) {
		if r == legacyArn {
			t.Error("the function's role names the legacy table")
		}
	}
}
