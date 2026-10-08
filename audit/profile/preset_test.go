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

func TestDeriveIsTheHighestMinimum(t *testing.T) {
	fw := builtin(t)
	for _, c := range []struct {
		name, doc string
		want      Preset
	}{
		{"history alone", "profiles:\n  h: {frameworks: [history]}\n", Operational},
		{"security", "profiles:\n  s: {frameworks: [security]}\n", Standard},
		{"billing", "profiles:\n  b: {frameworks: [billing-nl]}\n", Standard},
		{"history and security", "profiles:\n  h: {frameworks: [history]}\n  s: {frameworks: [security]}\n", Standard},
		{"dora", "profiles:\n  s: {frameworks: [security, dora]}\n", Attested},
		{"pci-dss", "profiles:\n  p: {frameworks: [pci-dss]}\n", Attested},
		{"nen-7513", "profiles:\n  n: {frameworks: [nen-7513]}\n", Attested},
		{"evidence-etsi", "profiles:\n  e: {frameworks: [evidence-etsi]}\n", Attested},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := deployment(t, c.doc).Derive(fw)
			if err != nil || got != c.want {
				t.Fatalf("Derive = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestNothingChosenIsOperational(t *testing.T) {
	got, err := (&Deployment{}).Derive(builtin(t))
	if err != nil || got != Operational {
		t.Fatalf("Derive = %q, %v; want operational", got, err)
	}
}

func TestEveryFrameworkProfileStatesAMinimum(t *testing.T) {
	for name, f := range builtin(t) {
		if !f.MinPreset.Valid() {
			t.Errorf("%s has min_preset %q", name, f.MinPreset)
		}
	}
}

func TestAStrongerPresetIsKeptAndAWeakerOneRefused(t *testing.T) {
	fw := builtin(t)
	d := deployment(t, "profiles:\n  sec: {frameworks: [security]}\n  pay: {frameworks: [pci-dss]}\n")
	got, err := d.ResolvePreset(fw, Attested)
	if err != nil || got != Attested {
		t.Fatalf("ResolvePreset(attested) = %q, %v", got, err)
	}
	_, err = d.ResolvePreset(fw, Standard)
	if err == nil {
		t.Fatal("a standard preset under pci-dss was accepted")
	}
	for _, want := range []string{"profile pay", "pci-dss", "needs attested"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "profile sec ") {
		t.Errorf("error %q names a profile that is satisfied", err)
	}
	// A stronger one than anything needs is allowed.
	h := deployment(t, "profiles:\n  h: {frameworks: [history]}\n")
	if got, err := h.ResolvePreset(fw, Attested); err != nil || got != Attested {
		t.Fatalf("explicit attested over history = %q, %v", got, err)
	}
}

func TestADeploymentsOwnPresetIsHeldToTheSameRule(t *testing.T) {
	fw := builtin(t)
	d := deployment(t, "preset: operational\nprofiles:\n  s: {frameworks: [security]}\n")
	if _, err := d.Compose(fw); err == nil || !strings.Contains(err.Error(), "profile s") {
		t.Fatalf("Compose = %v; want a refusal naming profile s", err)
	}
	d = deployment(t, "preset: standard\nprofiles:\n  s: {frameworks: [security]}\n")
	if _, err := d.Compose(fw); err != nil {
		t.Fatal(err)
	}
	d = deployment(t, "preset: bogus\nprofiles:\n  s: {frameworks: [security]}\n")
	if _, err := d.Compose(fw); err == nil {
		t.Fatal("an unknown preset was accepted")
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
