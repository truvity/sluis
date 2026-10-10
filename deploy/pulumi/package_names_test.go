package sluispulumi

import "testing"

// The release zips are named sluis-lambda_<version> (before 1.75, the all-in-one
// zip) and sluis-<module>_<version> (one per module). The
// version is read from either, and the floor applies to both.
func TestThePerModuleZipsAreNamedLikeTheAllInOneZip(t *testing.T) {
	for _, name := range []string{
		"sluis-lambda_1.75.0_linux_arm64.zip", "sluis-issuer_1.75.0_linux_arm64.zip",
		"sluis-cloudflare_v1.75.0-rc.2_linux_arm64.zip", "sluis-backup_1.75.0_linux_arm64.zip",
	} {
		if got := packageRelease("/tmp/"+name, ""); got == "" || got[0] == 'v' {
			t.Errorf("%s: release %q", name, got)
		}
		if err := checkVersion(name, ""); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := checkVersion("sluis-issuer_1.62.0_linux_arm64.zip", ""); err == nil {
		t.Error("a module zip older than the floor was accepted")
	}
	if err := checkVersion("sluis-signer_1.75.0_linux_arm64.zip", ""); err == nil {
		t.Error("a signer zip, which does not exist, was accepted")
	}
}
