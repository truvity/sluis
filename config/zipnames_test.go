package config

import (
	"slices"
	"testing"
)

func TestZipModulesAreTheRegistry(t *testing.T) {
	want := []string{"issuer", "cloudflare", "github", "google", "slack", "backup"}
	if got := ZipModules(); !slices.Equal(got, want) {
		t.Fatalf("ZipModules = %v, want %v", got, want)
	}
	if got := ZipName("github", "1.76.0"); got != "sluis-github_1.76.0_linux_arm64.zip" {
		t.Errorf("ZipName = %s", got)
	}
}
