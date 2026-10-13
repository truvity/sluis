package auditpulumi

import (
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/deploy/spec"
)

// TestSpecAgreesWithTheLibrary holds deploy/spec, the SDK-free copy of the
// names and checks an estate's tooling uses before anything exists, to the
// rules this library applies.
func TestSpecAgreesWithTheLibrary(t *testing.T) {
	for name, pair := range map[string][2]string{
		"name":          {nameRE.String(), spec.NamePattern.String()},
		"key component": {keyComponent.String(), spec.KeyComponentPattern.String()},
		"bucket prefix": {bucketPrefixRE.String(), spec.BucketPrefixPattern.String()},
		"architecture":  {LambdaArchitecture, spec.LambdaArchitecture},
		"runtime":       {LambdaRuntime, spec.LambdaRuntime},
		"lock none":     {None, spec.LockNone},
		"governance":    {Governance, spec.LockGovernance},
		"compliance":    {Compliance, spec.LockCompliance},
		"operational":   {PresetOperational, spec.PresetOperational},
		"standard":      {PresetStandard, spec.PresetStandard},
		"attested":      {PresetAttested, spec.PresetAttested},
		"archive":       {ArchiveBucketName("p", "111122223333", "eu-west-1"), spec.ArchiveBucketName("p", "111122223333", "eu-west-1")},
		"role":          {ComponentRoleName("p", "writer"), spec.ComponentRoleName("p", "writer")},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: the library's is %q, deploy/spec's %q", name, pair[0], pair[1])
		}
	}

	lib, own := InstallationComponents("audit"), spec.InstallationComponents("audit")
	if !slices.EqualFunc(lib, own, func(a Component, b spec.Component) bool {
		return a.Name == b.Name && a.ServiceAccount == b.ServiceAccount
	}) {
		t.Errorf("components: the library's %v, deploy/spec's %v", lib, own)
	}

	agree := func(what string, a, b error) {
		t.Helper()

		if (a == nil) != (b == nil) {
			t.Errorf("%s: the library says %v, deploy/spec %v", what, a, b)
		}

		if a != nil && !strings.HasSuffix(a.Error(), b.Error()) {
			t.Errorf("%s: the library says %q, deploy/spec %q", what, a, b)
		}
	}

	for _, v := range []string{"audit", "A", "", strings.Repeat("a", 40)} {
		agree("name "+v, CheckName(v), spec.CheckName(v))
		agree("profile "+v, CheckProfile(v), spec.CheckProfile(v))
		agree("bucket prefix "+v, CheckBucketPrefix(v), spec.CheckBucketPrefix(v))
	}

	for _, v := range []string{"/", "/audit/", "audit", ""} {
		agree("role path "+v, CheckRolePath(v), spec.CheckRolePath(v))
	}

	for _, v := range []string{None, Governance, Compliance, "LEGAL"} {
		agree("lock "+v, CheckLockMode(v), spec.CheckLockMode(v))
	}

	agree("telemetry", CheckTelemetryURLs("https://id.example.com", "https://otlp.example.com"),
		spec.CheckTelemetryURLs("https://id.example.com", "https://otlp.example.com"))
	agree("layer", CheckExtensionLayer([]string{"arm64"}, []string{LambdaRuntime}), spec.CheckExtensionLayer([]string{"arm64"}, []string{LambdaRuntime}))

	if (CheckTelemetryURLs("http://x", "") == nil) || (spec.CheckTelemetryURLs("http://x", "") == nil) {
		t.Error("a plain-http issuer was accepted")
	}

	if (CheckExtensionLayer([]string{"x86"}, nil) == nil) || (spec.CheckExtensionLayer([]string{"x86"}, nil) == nil) {
		t.Error("a layer for another architecture was accepted")
	}
}
