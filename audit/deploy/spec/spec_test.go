package spec_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/deploy/spec"
)

func TestNames(t *testing.T) {
	if got := spec.ArchiveBucketName("audit", "111122223333", "eu-west-1"); got != "audit-111122223333-eu-west-1" {
		t.Errorf("ArchiveBucketName = %q", got)
	}

	if got := spec.ComponentRoleName("acme", "query"); got != "acme-audit-query" {
		t.Errorf("ComponentRoleName = %q", got)
	}

	got := spec.InstallationComponents("audit")
	want := []spec.Component{{"writer", "audit"}, {"digest", "audit-digest"}, {"verify", "audit-verify"}, {"query", "audit-query"}}

	if len(got) != len(want) {
		t.Fatalf("components = %v", got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("components[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestChecks(t *testing.T) {
	ok := []error{
		spec.CheckName("audit"), spec.CheckRolePath("/audit/"), spec.CheckLockMode(spec.LockGovernance),
		spec.CheckProfile("records.v1"), spec.CheckBucketPrefix("audit-archive"),
		spec.CheckTelemetryURLs("https://id.example.com", "https://otlp.example.com"),
		spec.CheckExtensionLayer([]string{"arm64", "amd64"}, []string{spec.LambdaRuntime}),
	}
	for i, err := range ok {
		if err != nil {
			t.Errorf("check %d refused a valid value: %v", i, err)
		}
	}

	for want, err := range map[string]error{
		"starting with a letter":      spec.CheckName("1audit"),
		"must start and end with /":   spec.CheckRolePath("audit/"),
		"NONE, GOVERNANCE":            spec.CheckLockMode("LEGAL"),
		"not a key component":         spec.CheckProfile(".hidden"),
		"63 at most":                  spec.CheckBucketPrefix(strings.Repeat("a", 40)),
		"OTLPEndpoint":                spec.CheckTelemetryURLs("https://id.example.com", "http://otlp.example.com"),
		"the functions' architecture": spec.CheckExtensionLayer([]string{"amd64"}, []string{spec.LambdaRuntime}),
		"the functions' runtime":      spec.CheckExtensionLayer([]string{"arm64"}, []string{"provided.al2"}),
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want one saying %q", err, want)
		}
	}
}
