package sluispulumi

import (
	"testing"

	sluisconfig "github.com/truvity/sluis/config"
)

// TestTheLibrarysZipNamesAreTheReleasesNames derives the names the library
// looks for from the constant the release configuration is held to
// (config.ZipName), so an estate pin of <version> resolves every module zip.
func TestTheLibrarysZipNamesAreTheReleasesNames(t *testing.T) {
	const v = "1.76.0"
	issuer := "https://example.invalid/" + sluisconfig.ZipName(sluisconfig.ModuleIssuer, v)
	for _, m := range sluisconfig.Modules() {
		got, err := modulePackage(issuer, m)
		if err != nil {
			t.Fatal(err)
		}
		if want := "https://example.invalid/" + sluisconfig.ZipName(m, v); got != want {
			t.Errorf("module %s: the default package is %s, the release's is %s", m, got, want)
		}
	}
	for _, m := range sluisconfig.ZipModules() {
		name := sluisconfig.ZipName(m, v)
		if got := releaseName.FindStringSubmatch(name); got == nil || got[1] != v {
			t.Errorf("%s is not read as release %s by releaseName: %v", name, v, got)
		}
		if err := checkVersion(name, ""); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, orphan := range []string{"github-mint", "accessctl", "signer", "restore"} {
		if releaseName.MatchString(sluisconfig.ZipName(orphan, v)) {
			t.Errorf("the library reads a %s zip, which no release publishes", orphan)
		}
	}
}
