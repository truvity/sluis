package auditpulumi_test

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/deploy/pulumi"
)

// profilesOf is a deployment document with one profile composed of the
// framework profiles named.
func profilesOf(frameworks string) string {
	return "profiles:\n  p:\n    frameworks: [" + frameworks + "]\n"
}

// operational is an installation that asked for no more than the write path:
// no notary, no seal key, no schedule, no alarms, and no lock.
func operational(a *auditpulumi.Args) {
	a.Writer.DeploymentYAML = profilesOf("history")
	a.Notary = auditpulumi.NotaryArgs{}
	a.Alerts = auditpulumi.AlertsArgs{}
	a.Archive.ObjectLockMode, a.Archive.DefaultRetentionDays = "", 0
}

func TestTheOperationalPresetCreatesNeitherNotaryNorSealKeyNorAlarms(t *testing.T) {
	rec, out, err := build(t, operational)
	if err != nil {
		t.Fatal(err)
	}
	if out["preset"] != "operational" {
		t.Errorf("preset = %q", out["preset"])
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
	rec.one(t, "aws:s3/bucket:Bucket", "audit-archive")
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
	if out["preset"] != "standard" {
		t.Errorf("preset = %q", out["preset"])
	}
	rec.one(t, "aws:kms/key:Key", "audit-seal")
	rec.one(t, "aws:lambda/function:Function", "audit-notary")
	if out["topic"] == "" {
		t.Error("standard has no alarm topic")
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
		"a notary":     {notary, "Notary.Package is set and the preset is operational"},
		"alarm target": {alarms, "Alerts.EndpointURL is set and the preset is operational"},
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

func TestAWeakerPresetThanTheProfilesNeedIsRefusedNamingTheProfile(t *testing.T) {
	_, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.DeploymentYAML = "profiles:\n  evidence:\n    frameworks: [evidence-etsi]\n  security:\n    frameworks: [security]\n"
		a.Preset = auditpulumi.PresetStandard
	})
	if err == nil {
		t.Fatal("a standard preset under evidence-etsi was accepted")
	}
	for _, want := range []string{"profile evidence", "evidence-etsi", "needs attested"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "profile security") {
		t.Errorf("%q names a profile that is satisfied", err)
	}
}

func TestAStrongerPresetThanNeededIsKept(t *testing.T) {
	_, out, err := build(t, func(a *auditpulumi.Args) {
		a.Preset = auditpulumi.PresetAttested
		a.Archive.ObjectLockMode = auditpulumi.Compliance
		a.Archive.AcknowledgeCompliance = true
		a.Archive.DefaultRetentionDays = 30
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["preset"] != "attested" {
		t.Errorf("preset = %q", out["preset"])
	}
}

func TestAttestedLocksTheArchiveInComplianceMode(t *testing.T) {
	attested := func(a *auditpulumi.Args) {
		a.Writer.DeploymentYAML = profilesOf("pci-dss")
		a.Archive.ObjectLockMode, a.Archive.DefaultRetentionDays = "", 30
	}
	// Left unset, the mode is compliance, which is acknowledged deliberately.
	if _, _, err := build(t, attested); err == nil || !strings.Contains(err.Error(), "AcknowledgeCompliance") {
		t.Fatalf("got %v, want the compliance acknowledgement", err)
	}
	rec, out, err := build(t, func(a *auditpulumi.Args) { attested(a); a.Archive.AcknowledgeCompliance = true })
	if err != nil {
		t.Fatal(err)
	}
	if out["preset"] != "attested" {
		t.Errorf("preset = %q", out["preset"])
	}
	lock := rec.one(t, "aws:s3/bucketObjectLockConfiguration:BucketObjectLockConfiguration", "audit-archive")
	if mode := prop(lock, "rule").ObjectValue()["defaultRetention"].ObjectValue()["mode"].StringValue(); mode != "COMPLIANCE" {
		t.Errorf("lock mode = %s", mode)
	}
	// A weaker lock than the preset keeps is refused.
	_, _, err = build(t, func(a *auditpulumi.Args) { attested(a); a.Archive.ObjectLockMode = auditpulumi.Governance })
	if err == nil || !strings.Contains(err.Error(), "preset is attested") {
		t.Errorf("governance under attested: %v", err)
	}
}

func TestAnUnknownPresetOrFrameworkIsRefused(t *testing.T) {
	if _, _, err := build(t, func(a *auditpulumi.Args) { a.Preset = "gold" }); err == nil || !strings.Contains(err.Error(), `"gold"`) {
		t.Errorf("an unknown preset: %v", err)
	}
	if _, _, err := build(t, func(a *auditpulumi.Args) { a.Writer.DeploymentYAML = profilesOf("no-such") }); err == nil ||
		!strings.Contains(err.Error(), "no-such") {
		t.Errorf("an unknown framework profile: %v", err)
	}
}

func TestThePresetInTheDeploymentAndInTheArgumentsMustAgree(t *testing.T) {
	_, _, err := build(t, func(a *auditpulumi.Args) {
		a.Writer.DeploymentYAML = "preset: standard\n" + profilesOf("security")
		a.Preset = auditpulumi.PresetAttested
		a.Archive.ObjectLockMode, a.Archive.AcknowledgeCompliance = auditpulumi.Compliance, true
	})
	if err == nil || !strings.Contains(err.Error(), "set one of them") {
		t.Fatalf("got %v", err)
	}
}
