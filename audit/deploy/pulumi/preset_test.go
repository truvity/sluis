package auditpulumi_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"
)

// profilesOf is a deployment document with one profile composed of the
// framework profiles named.
func profilesOf(frameworks string) string {
	return "profiles:\n  p:\n    frameworks: [" + frameworks + "]\n"
}

// operational is an installation that asked for no more than the write path:
// the history framework profile on its own operational bucket, no notary, no seal
// key, no schedule, no alarms, and no lock.
func operational(a *auditpulumi.Args) {
	a.Writer.DeploymentYAML = profilesOf("history")
	a.Presets = map[string]auditpulumi.PresetStorage{"operational": {Bucket: "acme-audit", Create: true}}
	a.Notary, a.Keys.Seal = auditpulumi.NotaryArgs{}, ""
	a.Alerts = auditpulumi.AlertsArgs{}
}

func TestTheOperationalPresetCreatesNeitherNotaryNorSealKeyNorAlarms(t *testing.T) {
	rec, out, err := build(t, operational)
	if err != nil {
		t.Fatal(err)
	}
	if out["presets"] != "operational" {
		t.Errorf("presets = %q", out["presets"])
	}
	for _, typ := range []string{
		"aws:kms/alias:Alias", "aws:scheduler/schedule:Schedule", "aws:cloudwatch/metricAlarm:MetricAlarm", "aws:sns/topic:Topic",
	} {
		for _, r := range rec.ofType(typ) {
			if strings.Contains(r.Name, "seal") || strings.Contains(r.Name, "notary") || typ != "aws:kms/alias:Alias" {
				t.Errorf("operational declared %s %s", typ, r.Name)
			}
		}
	}
	for _, o := range []string{"sealKeyArn", "notaryFn", "notaryRole", "schedule", "topic"} {
		if out[o] != "" {
			t.Errorf("output %s = %q under operational", o, out[o])
		}
	}
	if len(rec.ofType("aws:lambda/function:Function")) != 1 {
		t.Errorf("functions: %v", rec.names())
	}
	rec.one(t, "aws:s3/bucket:Bucket", "audit-archive-operational")
	rec.one(t, "aws:lambda/function:Function", "audit-writer")
	if len(rec.ofType("aws:s3/bucketObjectLockConfiguration:BucketObjectLockConfiguration")) != 0 {
		t.Error("operational declared Object Lock")
	}
}

func TestStandardIsDerivedFromSecurityAndHasTheNotaryAndAlarms(t *testing.T) {
	rec, out, err := build(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["presets"] != "standard" {
		t.Errorf("presets = %q", out["presets"])
	}
	if out["sealKeyAlias"] != "alias/audit-seal" {
		t.Errorf("seal key alias = %q", out["sealKeyAlias"])
	}
	rec.one(t, "aws:lambda/function:Function", "audit-notary")
	if out["topic"] == "" {
		t.Error("standard has no alarm topic")
	}
	if out["bucketNames"] != "standard=acme-audit" || out["bucketArns"] != "standard="+arnp+"s3:::acme-audit" || out["credentialsPaths"] != "" {
		t.Errorf("bucket outputs: %q %q %q", out["bucketNames"], out["bucketArns"], out["credentialsPaths"])
	}
}

func TestAnOperationalInstallationIsRefusedANotaryOrAlarmsItDoesNotHave(t *testing.T) {
	notary := func(a *auditpulumi.Args) { operational(a); a.Notary.Package = "x.zip" }
	alarms := func(a *auditpulumi.Args) {
		operational(a)
		a.Alerts.EndpointURL = pulumi.String("https://alerts.example.test/sns")
	}
	for name, c := range map[string]struct {
		edit func(*auditpulumi.Args)
		says string
	}{
		"a notary":     {notary, "Notary.Package is set and the configured presets (operational) provision no notary"},
		"alarm target": {alarms, "Alerts.EndpointURL is set and the configured presets (operational) provision no alarms"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _, err := build(t, c.edit)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("got %v, want %q", err, c.says)
			}
			if len(rec.ofType("aws:s3/bucket:Bucket")) != 0 {
				t.Error("resources were declared before the refusal")
			}
		})
	}
}

// A standard preset and an operational one beside it run the notary: what the
// installation provisions is what any of its configured presets does.
func TestTheFeaturesAreTheUnionOverTheConfiguredPresets(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.DeploymentYAML = "profiles:\n  activity:\n    frameworks: [history]\n  security:\n    frameworks: [security]\n"
		a.Presets["operational"] = auditpulumi.PresetStorage{Bucket: "acme-audit-operational", Create: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["presets"] != "operational,standard" {
		t.Errorf("presets = %q", out["presets"])
	}
	rec.one(t, "aws:lambda/function:Function", "audit-notary")
	if out["topic"] == "" || out["sealKeyArn"] == "" {
		t.Errorf("no alarm topic or seal key: %v", out)
	}
	// Each profile's records are in the bucket of its own preset.
	if r := ruleFor(t, rec, "audit-archive-operational", "records/activity/"); r == nil {
		t.Error("no rule for activity in the operational bucket")
	}
	if r := ruleFor(t, rec, "audit-archive-standard", "records/security/"); r == nil {
		t.Error("no rule for security in the standard bucket")
	}
}

func TestAProfileWhosePresetIsNotConfiguredIsRefusedNamingBoth(t *testing.T) {
	_, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.DeploymentYAML = "profiles:\n  evidence:\n    frameworks: [evidence-etsi]\n  security:\n    frameworks: [security]\n"
	})
	if err == nil {
		t.Fatal("an attested profile with no attested preset was accepted")
	}
	for _, want := range []string{"profile evidence", "evidence-etsi", "attested preset", "configures only standard", "presets.attested"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "profile security") {
		t.Errorf("%q names a profile that is satisfied", err)
	}
}

func TestAProfileAskingForAWeakerPresetThanItNeedsIsRefused(t *testing.T) {
	_, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.DeploymentYAML = "profiles:\n  security:\n    frameworks: [security]\n    preset: operational\n"
	})
	if err == nil || !strings.Contains(err.Error(), "weaker than its framework profiles need") {
		t.Fatalf("got %v", err)
	}
}

func TestAStrongerPresetThanNeededIsKept(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.DeploymentYAML = "profiles:\n  security:\n    frameworks: [security]\n    preset: attested\n"
		a.Presets = map[string]auditpulumi.PresetStorage{"attested": {Bucket: "acme-audit-attested", Create: true}}
		a.Archive.AcknowledgeCompliance, a.Archive.DefaultRetentionDays = true, 30
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["presets"] != "attested" {
		t.Errorf("presets = %q", out["presets"])
	}
	rec.one(t, lockType, "audit-archive-attested")
}

func TestAttestedLocksItsBucketInComplianceMode(t *testing.T) {
	attested := func(a *auditpulumi.Args) {
		a.Writer.DeploymentYAML = profilesOf("pci-dss")
		a.Presets = map[string]auditpulumi.PresetStorage{"attested": {Bucket: "acme-audit-attested", Create: true}}
		a.Archive.DefaultRetentionDays = 30
	}
	// Left unset, the mode is compliance, which is acknowledged deliberately.
	if _, _, err := build(t, attested); err == nil || !strings.Contains(err.Error(), "AcknowledgeCompliance") {
		t.Fatalf("got %v, want the compliance acknowledgement", err)
	}
	rec, out, err := build(t, func(a *auditpulumi.Args) { attested(a); a.Archive.AcknowledgeCompliance = true })
	if err != nil {
		t.Fatal(err)
	}
	if out["presets"] != "attested" {
		t.Errorf("presets = %q", out["presets"])
	}
	lock := rec.one(t, "aws:s3/bucketObjectLockConfiguration:BucketObjectLockConfiguration", "audit-archive-attested")
	if mode := prop(lock, "rule").ObjectValue()["defaultRetention"].ObjectValue()["mode"].StringValue(); mode != "COMPLIANCE" {
		t.Errorf("lock mode = %s", mode)
	}
	// The governance trial is the one softer mode; no lock at all is refused.
	rec, _, err = build(t, func(a *auditpulumi.Args) { attested(a); a.Archive.ObjectLockMode = auditpulumi.Governance })
	if err != nil {
		t.Fatal(err)
	}
	lock = rec.one(t, "aws:s3/bucketObjectLockConfiguration:BucketObjectLockConfiguration", "audit-archive-attested")
	if mode := prop(lock, "rule").ObjectValue()["defaultRetention"].ObjectValue()["mode"].StringValue(); mode != "GOVERNANCE" {
		t.Errorf("lock mode = %s", mode)
	}
	_, _, err = build(t, func(a *auditpulumi.Args) { attested(a); a.Archive.ObjectLockMode = auditpulumi.None })
	if err == nil || !strings.Contains(err.Error(), "ObjectLockMode is NONE") {
		t.Errorf("NONE under attested: %v", err)
	}
}

func TestObjectLockWhereThereIsNoAttestedBucketToCreateIsRefused(t *testing.T) {
	for _, mode := range []string{auditpulumi.Governance, auditpulumi.Compliance} {
		_, _, err := build(t, func(a *auditpulumi.Args) {
			a.Archive.ObjectLockMode, a.Archive.AcknowledgeCompliance, a.Archive.DefaultRetentionDays = mode, true, 30
		})
		if err == nil || !strings.Contains(err.Error(), "no preset is the attested preset") {
			t.Errorf("%s on a standard archive: %v", mode, err)
		}
	}
	// An attested preset on an existing bucket is locked by its owner, not here.
	_, _, err := build(t, attested(func(a *auditpulumi.Args) {
		a.Presets["attested"] = auditpulumi.PresetStorage{Bucket: "acme-locked"}
	}))
	if err == nil || !strings.Contains(err.Error(), "no preset is the attested preset with Create") {
		t.Errorf("a lock mode for an existing attested bucket: %v", err)
	}
}

func TestAnUnknownPresetOrFrameworkIsRefused(t *testing.T) {
	if _, _, err := build(t, func(a *auditpulumi.Args) {
		a.Presets["gold"] = auditpulumi.PresetStorage{Bucket: "acme-gold"}
	}); err == nil || !strings.Contains(err.Error(), `"gold"`) {
		t.Errorf("an unknown preset: %v", err)
	}
	if _, _, err := build(t, func(a *auditpulumi.Args) { a.Writer.DeploymentYAML = profilesOf("no-such") }); err == nil ||
		!strings.Contains(err.Error(), "no-such") {
		t.Errorf("an unknown framework profile: %v", err)
	}
}

// The storage of each preset is named in one place.
func TestThePresetsInTheDocumentAndInTheArgumentsAreRefusedTogether(t *testing.T) {
	_, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.DeploymentYAML = profilesOf("security") + "presets:\n  standard:\n    bucket: acme-audit\n"
	})
	if err == nil || strings.Count(err.Error(), "Presets is set and") != 1 || !strings.Contains(err.Error(), "Presets") {
		t.Fatalf("got %v", err)
	}
}

// A document that carries its own presets is shipped as it is: the library
// creates nothing for them and only grants the roles.
func TestThePresetsOfTheDocumentAloneAreExistingBuckets(t *testing.T) {
	rec, out, err := build(t, func(a *auditpulumi.Args) {
		a.Presets = nil
		a.Writer.DeploymentYAML = profilesOf("security") + "presets:\n  standard:\n    bucket: acme-existing\n    prefix: standard/\n"
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.ofType("aws:s3/bucket:Bucket")); n != 0 {
		t.Errorf("%d buckets created for a document's presets", n)
	}
	g := grants(policy(t, rec, "audit-writer"))
	if !hasResource(g, "s3:PutObject", "acme-existing/standard/records/*") {
		t.Errorf("the writer's puts: %v", g["s3:PutObject"])
	}
	if out["bucketNames"] != "standard=acme-existing" {
		t.Errorf("bucketNames = %q", out["bucketNames"])
	}
	if d := layerFiles(t, rec, "audit-writer")["deployment.yaml"]; !strings.Contains(d, "bucket: acme-existing") {
		t.Errorf("deployment.yaml:\n%s", d)
	}
}

func ruleFor(t *testing.T, rec *recorder, bucket, prefix string) map[string]resource.PropertyValue {
	t.Helper()
	lc := rec.one(t, "aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration", bucket)
	for _, r := range prop(lc, "rules").ArrayValue() {
		o := r.ObjectValue()
		if f := o["filter"]; f.IsObject() && f.ObjectValue()["prefix"].StringValue() == prefix {
			out := map[string]resource.PropertyValue{}
			for k, v := range o {
				out[string(k)] = v
			}
			return out
		}
	}
	t.Fatalf("no lifecycle rule for %s", prefix)
	return nil
}

// A preset's records are in its own bucket and under its own prefix, and the key
// behind its alias is the one its bucket is encrypted with and its roles may use.
func TestEachProfileHasAPrefixAnExpiryAndTheKeyBehindItsPresetsAlias(t *testing.T) {
	rec, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.DeploymentYAML = "profiles:\n" +
			"  security:\n    frameworks: [security]\n" +
			"  billing:\n    frameworks: [billing-nl]\n" +
			"  activity:\n    frameworks: [history]\n"
		a.Presets = map[string]auditpulumi.PresetStorage{
			"operational": {Bucket: "acme-audit-operational", Prefix: "operational/", Create: true},
			"standard":    {Bucket: "acme-audit-standard", Prefix: "standard/", KeyAlias: "alias/acme-standard", Create: true},
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		bucket, prefix string
		days           float64
	}{
		{"audit-archive-standard", "standard/records/security/", 365},
		{"audit-archive-standard", "standard/records/billing/", 2557},
		{"audit-archive-operational", "operational/records/activity/", 365},
	} {
		r := ruleFor(t, rec, c.bucket, c.prefix)
		if got := r["expiration"].ObjectValue()["days"].NumberValue(); got != c.days {
			t.Errorf("%s expires after %v days, want %v", c.prefix, got, c.days)
		}
	}
	// The abort-multipart rule is the preset's prefix, not the whole bucket's.
	if r := ruleFor(t, rec, "audit-archive-standard", "standard/"); r == nil {
		t.Error("no multipart rule on the preset's prefix")
	}
	// The library creates no key: the alias is the estate's, looked up.
	for _, a := range rec.ofType("aws:kms/alias:Alias") {
		t.Errorf("the library created the alias %s", prop(a, "name").StringValue())
	}
	var looked []string
	for _, c := range rec.calls {
		if c.Token == aliasLookup {
			looked = append(looked, c.Token)
		}
	}
	if len(looked) != 3 { // Keys.Archive, Keys.Seal, and the preset's alias
		t.Errorf("alias lookups = %v", looked)
	}
	// The standard bucket is under its preset's key, the operational one under the archive key.
	for bucket, want := range map[string]string{
		"audit-archive-standard": ":key/acme-standard", "audit-archive-operational": ":key/audit-archive",
	} {
		sse := rec.one(t, "aws:s3/bucketServerSideEncryptionConfiguration:BucketServerSideEncryptionConfiguration", bucket)
		def := prop(sse, "rules").ArrayValue()[0].ObjectValue()["applyServerSideEncryptionByDefault"].ObjectValue()
		if !strings.HasSuffix(def["kmsMasterKeyId"].StringValue(), want) {
			t.Errorf("%s is encrypted under %s, want ...%s", bucket, def["kmsMasterKeyId"].StringValue(), want)
		}
	}
	// The writer may use both keys, each once and without an encryption-context
	// condition (S3 binds its own).
	var keysGranted []string
	for _, st := range policy(t, rec, "audit-writer") {
		if !strings.Contains(strings.Join(strs(st["Action"]), " "), "kms:GenerateDataKey") {
			continue
		}
		if _, ok := st["Condition"]; ok {
			continue // the pseudonym and conceal keys, if any
		}
		for _, arn := range strs(st["Resource"]) {
			keysGranted = append(keysGranted, arn[strings.LastIndex(arn, "/")+1:])
		}
	}
	sort.Strings(keysGranted)
	if strings.Join(keysGranted, ",") != "acme-standard,audit-archive" {
		t.Errorf("keys granted = %v", keysGranted)
	}
}
