package cli

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/internal/config"
	"github.com/truvity/sluis/audit/profile"
	"github.com/truvity/sluis/audit/store/s3store"
)

func deploymentFor(t *testing.T, doc string) *profile.Deployment {
	t.Helper()
	d, err := profile.ParseDeployment([]byte("apiVersion: " + profile.DeploymentAPIVersion + "\n" + doc))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// hive-like: the operational preset on an S3-compatible store.
const hiveLike = `
presets:
  operational: {bucket: hive-audit, prefix: operational/, region: auto, endpoint: "https://acct.r2.cloudflarestorage.com", credentials: internal/audit/r2}
profiles:
  activity: {frameworks: [history], categories: [activity]}
`

// Truvity-like: the standard preset on S3, with a key of its own.
const truvityLike = `
presets:
  standard: {bucket: example-audit, prefix: standard/, region: eu-central-1, key_alias: alias/audit-archive}
profiles:
  security: {frameworks: [security], categories: [security]}
`

// mixed: a standard and an attested preset, in two buckets.
const mixedPresets = `
presets:
  standard: {bucket: example-audit, prefix: standard/, region: eu-central-1}
  attested: {bucket: example-audit-locked, prefix: attested/, region: eu-central-1, key_alias: alias/audit-locked}
profiles:
  security: {frameworks: [security], categories: [security]}
  evidence: {frameworks: [evidence-etsi], categories: [evidence]}
`

func TestEachPresetIsOpenedInItsOwnStore(t *testing.T) {
	a := config.Archive{StateRoot: "/audit/main", KMSKey: "alias/audit-default"}

	plans, err := PlanPresets(deploymentFor(t, hiveLike), a)
	if err != nil {
		t.Fatal(err)
	}
	p := plans[profile.Operational]
	if p.Options.Bucket != "hive-audit" || p.Options.Prefix != "operational/" || p.Options.Lock != s3store.None ||
		p.Options.Endpoint != "https://acct.r2.cloudflarestorage.com" || p.Options.KMSKeyID != "" {
		t.Errorf("operational on R2: %+v", p.Options)
	}
	if p.Credentials == nil || p.Credentials.Root != "/audit/main" || p.Credentials.Address != "internal/audit/r2" {
		t.Errorf("credentials: %+v", p.Credentials)
	}

	plans, err = PlanPresets(deploymentFor(t, truvityLike), a)
	if err != nil {
		t.Fatal(err)
	}
	if o := plans[profile.Standard].Options; o.Lock != s3store.None || o.KMSKeyID != "alias/audit-archive" || o.Endpoint != "" {
		t.Errorf("standard on S3: %+v", o)
	}

	plans, err = PlanPresets(deploymentFor(t, mixedPresets), a)
	if err != nil {
		t.Fatal(err)
	}
	std, att := plans[profile.Standard].Options, plans[profile.Attested].Options
	if std.Lock != s3store.None || std.KMSKeyID != "alias/audit-default" || std.Bucket != "example-audit" {
		t.Errorf("standard: %+v", std)
	}
	if att.Lock != s3store.Compliance || att.KMSKeyID != "alias/audit-locked" || att.Bucket != "example-audit-locked" {
		t.Errorf("attested: %+v", att)
	}
}

func TestCredentialsNeedTheStateRoot(t *testing.T) {
	_, err := PlanPresets(deploymentFor(t, hiveLike), config.Archive{})
	if err == nil || !strings.Contains(err.Error(), "archive.stateRoot") {
		t.Fatalf("got %v", err)
	}
}

func TestAProfileNeedsAConfiguredPreset(t *testing.T) {
	d := deploymentFor(t, truvityLike+"  payments: {frameworks: [pci-dss]}\n")
	fw, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	err = d.CheckStorage(fw)
	if err == nil || !strings.Contains(err.Error(), "profile payments") || !strings.Contains(err.Error(), "attested") ||
		!strings.Contains(err.Error(), "standard") {
		t.Fatalf("got %v", err)
	}
}
