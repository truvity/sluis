//nolint:lll // fixtures and table rows are one-line documents
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

// endpoint-like: the operational preset on an S3-compatible store.
const endpointLike = `
presets:
  operational: {bucket: edge-audit, prefix: operational/, region: auto, endpoint: "https://acct.r2.cloudflarestorage.com", credentials: internal/audit/r2}
profiles:
  activity: {frameworks: [history], categories: [activity]}
`

// standard-like: the standard preset on S3, with a key of its own.
const standardLike = `
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

	plans, err := PlanPresets(deploymentFor(t, endpointLike), a)
	if err != nil {
		t.Fatal(err)
	}
	p := plans[profile.Operational]
	if p.Options.Bucket != "edge-audit" || p.Options.Prefix != "operational" || p.Options.Lock != s3store.None ||
		p.Options.Endpoint != "https://acct.r2.cloudflarestorage.com" || p.Options.KMSKeyID != "" {
		t.Errorf("operational on R2: %+v", p.Options)
	}
	if p.Credentials == nil || p.Credentials.Root != "/audit/main" || p.Credentials.Address != "internal/audit/r2" {
		t.Errorf("credentials: %+v", p.Credentials)
	}

	plans, err = PlanPresets(deploymentFor(t, standardLike), a)
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
	_, err := PlanPresets(deploymentFor(t, endpointLike), config.Archive{})
	if err == nil || !strings.Contains(err.Error(), "archive.stateRoot") {
		t.Fatalf("got %v", err)
	}
}

func TestAProfileNeedsAConfiguredPreset(t *testing.T) {
	d := deploymentFor(t, standardLike+"  payments: {frameworks: [pci-dss]}\n")
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

// The preset's prefix is prepended to the layout every reader expects
// (records/<profile>/..., seals/<profile>/..., keys/, catalogue/, schema/, dlq/),
// and an empty prefix writes the keys an installation always wrote.
func TestThePresetPrefixIsPrependedToTheBucketLayout(t *testing.T) {
	for prefix, want := range map[string]string{
		"":          "records/security/acme/2026/10/08/12/01",
		"standard/": "standard/records/security/acme/2026/10/08/12/01",
		"a/b/":      "a/b/records/security/acme/2026/10/08/12/01",
	} {
		doc := "presets:\n  standard: {bucket: b"
		if prefix != "" {
			doc += ", prefix: " + prefix
		}
		doc += "}\nprofiles:\n  security: {frameworks: [security]}\n"
		plans, err := PlanPresets(deploymentFor(t, doc), config.Archive{})
		if err != nil {
			t.Fatal(err)
		}
		if got := s3store.KeyFor(plans[profile.Standard].Options, "records/security/acme/2026/10/08/12/01"); got != want {
			t.Errorf("prefix %q: key %q, want %q", prefix, got, want)
		}
		if got := s3store.KeyFor(plans[profile.Standard].Options, "seals/security/acme/2026/10/08/12.jws"); got != strings.TrimSuffix(want, "records/security/acme/2026/10/08/12/01")+"seals/security/acme/2026/10/08/12.jws" { //nolint:lll // a table row
			t.Errorf("prefix %q: seal key %q", prefix, got)
		}
	}
}

// minted: the operational preset on R2, with credentials minted from a
// Cloudflare preset instead of a static document.
const mintedLike = `
presets:
  operational: {bucket: edge-audit, prefix: operational/, region: auto, endpoint: "https://acct.r2.cloudflarestorage.com", credentials_preset: {account: acct, minter: internal/cloudflare/main/minter, prototype: proto-r2-00001, lifetime: 15m}}
profiles:
  activity: {frameworks: [history], categories: [activity]}
`

func TestAPresetStoreMintsItsCredentialsAndTheStaticPathStaysTheDefault(t *testing.T) {
	a := config.Archive{StateRoot: "/audit/main"}
	plans, err := PlanPresets(deploymentFor(t, mintedLike), a)
	if err != nil {
		t.Fatal(err)
	}
	p := plans[profile.Operational]
	if p.Credentials != nil || p.Minted == nil || p.Minted.Root != "/audit/main" || p.Minted.Spec.Minter != "internal/cloudflare/main/minter" {
		t.Errorf("minted plan: %+v / %+v", p.Credentials, p.Minted)
	}
	if _, err = PlanPresets(deploymentFor(t, mintedLike), config.Archive{}); err == nil || !strings.Contains(err.Error(), "stateRoot") {
		t.Errorf("no state root: %v", err)
	}
	// The static document is unchanged and involves nothing of Cloudflare.
	plans, err = PlanPresets(deploymentFor(t, endpointLike), a)
	if err != nil || plans[profile.Operational].Minted != nil || plans[profile.Operational].Credentials == nil {
		t.Errorf("static plan: %+v %v", plans[profile.Operational], err)
	}
}

// stored: the operational preset on R2, with the credentials a sluis
// installation rotates for one of its Cloudflare presets.
const storedLike = `
presets:
  operational: {bucket: edge-audit, prefix: operational/, region: auto, endpoint: "https://acct.r2.cloudflarestorage.com", credentials_ref: external/cloudflare/audit-r2}
profiles:
  activity: {frameworks: [history], categories: [activity]}
`

func TestAPresetStoreReadsTheCredentialsSluisRotates(t *testing.T) {
	for name, a := range map[string]config.Archive{
		"ssm":  {SluisRoot: "/sluis/main"},
		"file": {SluisDir: "/etc/audit/sluis"},
	} {
		plans, err := PlanPresets(deploymentFor(t, storedLike), a)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		p := plans[profile.Operational]
		if p.Credentials != nil || p.Minted != nil || p.Stored == nil || p.Stored.Ref != "external/cloudflare/audit-r2" ||
			p.Stored.Root != a.SluisRoot || p.Stored.Dir != a.SluisDir || p.Stored.Endpoint != "https://acct.r2.cloudflarestorage.com" {
			t.Errorf("%s: stored plan: %+v / %+v / %+v", name, p.Credentials, p.Minted, p.Stored)
		}
	}
	if _, err := PlanPresets(deploymentFor(t, storedLike), config.Archive{StateRoot: "/audit/main"}); err == nil || !strings.Contains(err.Error(), "sluisRoot") {
		t.Errorf("no sluis root or dir: %v", err)
	}
	if _, err := PlanPresets(deploymentFor(t, storedLike), config.Archive{SluisRoot: "/sluis/main", SluisDir: "/etc/audit/sluis"}); err == nil || !strings.Contains(err.Error(), "both") {
		t.Errorf("both a root and a dir: %v", err)
	}
}

func TestCredentialsRefIsOneSourceOnAnEndpoint(t *testing.T) {
	for name, doc := range map[string]string{
		"with static":   `{bucket: b, endpoint: "https://acct.r2.cloudflarestorage.com", credentials: internal/x, credentials_ref: external/cloudflare/r2}`,
		"with a minter": `{bucket: b, endpoint: "https://acct.r2.cloudflarestorage.com", credentials_ref: external/cloudflare/r2, credentials_preset: {account: a, minter: internal/m, prototype: p, lifetime: 15m}}`,
		"on aws":        `{bucket: b, credentials_ref: external/cloudflare/r2}`,
		"internal":      `{bucket: b, endpoint: "https://acct.r2.cloudflarestorage.com", credentials_ref: internal/cloudflare/main/minter}`,
		"traversal":     `{bucket: b, endpoint: "https://acct.r2.cloudflarestorage.com", credentials_ref: external/cloudflare/../x}`,
	} {
		_, err := profile.ParseDeployment([]byte("apiVersion: " + profile.DeploymentAPIVersion + "\npresets:\n  operational: " + doc + "\nprofiles:\n  a: {frameworks: [history]}\n"))
		if err == nil || !strings.Contains(err.Error(), "credentials_ref") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
