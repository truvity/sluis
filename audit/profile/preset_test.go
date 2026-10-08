package profile

import (
	"strings"
	"testing"
)

func deployment(t *testing.T, doc string) *Deployment {
	t.Helper()
	d, err := ParseDeployment([]byte("apiVersion: " + DeploymentAPIVersion + "\n" + doc))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAProfilesPresetIsTheHighestMinimum(t *testing.T) {
	fw := builtin(t)
	for _, c := range []struct {
		name, doc string
		want      Preset
	}{
		{"history alone", "profiles:\n  p: {frameworks: [history]}\n", Operational},
		{"security", "profiles:\n  p: {frameworks: [security]}\n", Standard},
		{"billing", "profiles:\n  p: {frameworks: [billing-nl]}\n", Standard},
		{"dora", "profiles:\n  p: {frameworks: [security, dora]}\n", Attested},
		{"pci-dss", "profiles:\n  p: {frameworks: [pci-dss]}\n", Attested},
		{"nen-7513", "profiles:\n  p: {frameworks: [nen-7513]}\n", Attested},
		{"evidence-etsi", "profiles:\n  p: {frameworks: [evidence-etsi]}\n", Attested},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := deployment(t, c.doc).ProfileNeeds(fw)
			if err != nil || got["p"].Preset != c.want {
				t.Fatalf("ProfileNeeds = %+v, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestEveryFrameworkProfileStatesAMinimum(t *testing.T) {
	for name, f := range builtin(t) {
		if !f.MinPreset.Valid() {
			t.Errorf("%s has min_preset %q", name, f.MinPreset)
		}
	}
}

const (
	hiveLike = `
presets:
  operational: {bucket: hive-audit, prefix: operational/, region: auto, endpoint: "https://acct.r2.cloudflarestorage.com", credentials: internal/audit/r2}
profiles:
  history: {frameworks: [history], categories: [activity]}
`
	truvityLike = `
presets:
  standard: {bucket: example-audit, prefix: standard/, region: eu-central-1, key_alias: alias/audit-archive}
profiles:
  security: {frameworks: [security], categories: [security]}
`
	mixed = `
presets:
  standard: {bucket: example-audit, prefix: standard/, region: eu-central-1}
  attested: {bucket: example-audit-locked, prefix: attested/, region: eu-central-1}
profiles:
  security: {frameworks: [security], categories: [security]}
  payments: {frameworks: [pci-dss], categories: [payments]}
`
)

func TestEveryProfilesPresetMustBeConfigured(t *testing.T) {
	fw := builtin(t)
	for name, doc := range map[string]string{"hive": hiveLike, "truvity": truvityLike, "mixed": mixed} {
		if err := deployment(t, doc).CheckStorage(fw); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A profile whose preset is not configured is refused, naming both.
	d := deployment(t, "presets:\n  standard: {bucket: b}\nprofiles:\n  pay: {frameworks: [pci-dss]}\n")
	err := d.CheckStorage(fw)
	if err == nil {
		t.Fatal("an attested profile with no attested preset was accepted")
	}
	for _, want := range []string{"profile pay", "attested", "standard", "presets.attested"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	// So is a deployment with no presets at all, and a profile below the
	// configured presets (it has to ask for one of them).
	if err := deployment(t, "profiles:\n  h: {frameworks: [history]}\n").CheckStorage(fw); err == nil {
		t.Error("no presets accepted")
	}
	if err := deployment(t, "presets:\n  standard: {bucket: b}\nprofiles:\n  h: {frameworks: [history]}\n").CheckStorage(fw); err == nil ||
		!strings.Contains(err.Error(), "profile h") || !strings.Contains(err.Error(), "operational") {
		t.Errorf("history on a standard-only installation: %v", err)
	}
	if err := deployment(t, "presets:\n  standard: {bucket: b}\nprofiles:\n  h: {frameworks: [history], preset: standard}\n").CheckStorage(fw); err != nil {
		t.Errorf("a profile asking for the configured stronger preset: %v", err)
	}
}

func TestAWeakerProfilePresetIsRefused(t *testing.T) {
	d := deployment(t, "presets:\n  attested: {bucket: b}\nprofiles:\n  p: {frameworks: [pci-dss], preset: standard}\n")
	if err := d.CheckStorage(builtin(t)); err == nil || !strings.Contains(err.Error(), "profile p") {
		t.Fatalf("CheckStorage = %v; want a refusal naming profile p", err)
	}
}

func TestPresetStorageRefusals(t *testing.T) {
	for name, c := range map[string]struct{ doc, want string }{
		"attested on an endpoint":   {"presets:\n  attested: {bucket: b, endpoint: \"https://x.example\"}\nprofiles:\n  p: {frameworks: [pci-dss]}\n", "Object Lock"},
		"ARN as key alias":          {"presets:\n  standard: {bucket: b, key_alias: \"arn:aws:kms:eu-central-1:111122223333:key/abc\"}\nprofiles:\n  p: {frameworks: [security]}\n", "never a key id or ARN"},
		"key alias on endpoint":     {"presets:\n  standard: {bucket: b, endpoint: \"https://x.example\", key_alias: alias/k}\nprofiles:\n  p: {frameworks: [security]}\n", "key_alias"},
		"credentials on AWS":        {"presets:\n  standard: {bucket: b, credentials: internal/x}\nprofiles:\n  p: {frameworks: [security]}\n", "workload's identity"},
		"no bucket":                 {"presets:\n  standard: {prefix: standard/}\nprofiles:\n  p: {frameworks: [security]}\n", "bucket is required"},
		"unknown preset":            {"presets:\n  gold: {bucket: b}\nprofiles:\n  p: {frameworks: [security]}\n", "gold"},
		"prefix without slash":      {"presets:\n  standard: {bucket: b, prefix: standard}\nprofiles:\n  p: {frameworks: [security]}\n", "ending in a slash"},
		"endpoint not a URL":        {"presets:\n  operational: {bucket: b, endpoint: nope}\nprofiles:\n  p: {frameworks: [history]}\n", "not an http(s) URL"},
		"the old key_alias":         {"presets:\n  standard: {bucket: b}\nprofiles:\n  p: {frameworks: [security], key_alias: alias/k}\n", "key_alias"},
		"the old deployment preset": {"preset: standard\npresets:\n  standard: {bucket: b}\nprofiles:\n  p: {frameworks: [security]}\n", "preset"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDeployment([]byte("apiVersion: " + DeploymentAPIVersion + "\n" + c.doc))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ParseDeployment = %v; want a refusal saying %q", err, c.want)
			}
		})
	}
}

func TestFeaturesAreWhatAnyConfiguredPresetNeeds(t *testing.T) {
	for name, c := range map[string]struct {
		doc  string
		want Features
	}{
		"hive":    {hiveLike, Features{}},
		"truvity": {truvityLike, Features{Notary: true, Alarms: true}},
		"mixed":   {mixed, Features{Notary: true, Alarms: true, ObjectLock: true, PseudonymKeys: true}},
	} {
		if got := deployment(t, c.doc).Features(); got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
}

func TestLockModeIsAPropertyOfThePreset(t *testing.T) {
	for p, want := range map[Preset]string{Operational: "none", Standard: "none", Attested: "compliance"} {
		if got := p.LockMode(); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
}

func TestFeatures(t *testing.T) {
	want := map[Preset]Features{
		Operational: {},
		Standard:    {Notary: true, Alarms: true},
		Attested:    {Notary: true, Alarms: true, ObjectLock: true, PseudonymKeys: true},
	}
	for p, f := range want {
		if got := p.Features(); got != f {
			t.Errorf("%s: %+v, want %+v", p, got, f)
		}
	}
}

func TestParsePreset(t *testing.T) {
	if p, err := ParsePreset(""); p != "" || err != nil {
		t.Fatalf("ParsePreset(\"\") = %q, %v", p, err)
	}
	if _, err := ParsePreset("gold"); err == nil {
		t.Fatal("gold accepted")
	}
}

func TestAProfileThatPseudonymisesNeedsThePseudonymKey(t *testing.T) {
	frameworks, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseDeployment([]byte("profiles:\n  activity:\n    frameworks: [history]\n  trail:\n    frameworks: [security]\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.PseudonymProfiles(frameworks)
	if err != nil {
		t.Fatal(err)
	}
	composed, _ := d.Compose(frameworks)
	var want []string
	for name, p := range composed {
		if p.Pseudonymises() {
			want = append(want, name)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("PseudonymProfiles = %v, composed profiles that pseudonymise = %v", got, want)
	}
	if !composed["trail"].Pseudonymises() || len(got) == 0 {
		t.Errorf("the security profile keeps no pseudonym: %v (identity %v)", got, composed["trail"].Identity)
	}
}

// A framework profile that demands Object Lock keeps its copies where they cannot
// be deleted, which is what the attested preset is: so its minimum has to be
// attested, or the writer would put such a copy where it can be.
func TestEveryFrameworkThatDemandsALockNeedsTheAttestedPreset(t *testing.T) {
	for name, f := range builtin(t) {
		if LockRank(f.Integrity.ObjectLockMode) > LockRank(LockNone) && f.MinPreset != Attested {
			t.Errorf("%s demands %s Object Lock and has min_preset %s", name, f.Integrity.ObjectLockMode, f.MinPreset)
		}
	}
}

func TestADestinationsOwnPreset(t *testing.T) {
	fw := builtin(t)
	d := deployment(t, "profiles:\n  a: {frameworks: [history], preset: attested}\n  b: {frameworks: [security]}\n")
	got, err := d.Compose(fw)
	if err != nil {
		t.Fatal(err)
	}
	if got["a"].Preset != Attested || got["b"].Preset != Standard {
		t.Errorf("presets: a %s, b %s", got["a"].Preset, got["b"].Preset)
	}
	d = deployment(t, "profiles:\n  p: {frameworks: [pci-dss], preset: standard}\n")
	if _, err := d.Compose(fw); err == nil || !strings.Contains(err.Error(), "profile p") {
		t.Errorf("a destination weaker than its frameworks: %v", err)
	}
}
