package cli

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/internal/config"
	"github.com/truvity/sluis/audit/profile"
	"github.com/truvity/sluis/audit/store/s3store"
)

func composedDestinations(t *testing.T, doc string) map[string]*profile.Profile {
	t.Helper()
	d, err := profile.ParseDeployment([]byte("apiVersion: " + profile.DeploymentAPIVersion + "\n" + doc))
	if err != nil {
		t.Fatal(err)
	}
	fw, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Compose(fw)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const mixed = `
profiles:
  security:
    frameworks: [security]
    key_alias: alias/audit-security
  evidence:
    frameworks: [evidence-etsi]
    key_alias: alias/audit-evidence
  activity:
    frameworks: [history]
`

func TestObjectLockIsTheAttestedDestinationsAlone(t *testing.T) {
	plans, err := PlanDestinations(config.Archive{Bucket: config.Bucket{Name: "b"}, LockMode: "compliance", KMSKey: "alias/audit-archive"},
		composedDestinations(t, mixed))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]DestinationPlan{
		"security": {Lock: s3store.None, KMSKey: "alias/audit-security"},
		"evidence": {Lock: s3store.Compliance, KMSKey: "alias/audit-evidence"},
		"activity": {Lock: s3store.None, KMSKey: "alias/audit-archive"},
	}
	for name, w := range want {
		if plans[name] != w {
			t.Errorf("%s: %+v, want %+v", name, plans[name], w)
		}
	}
}

func TestAnAttestedDestinationIsRefusedOnAnS3CompatibleEndpoint(t *testing.T) {
	_, err := PlanDestinations(config.Archive{Bucket: config.Bucket{Name: "b", Endpoint: "https://r2.example.test"}, LockMode: "compliance"},
		composedDestinations(t, mixed))
	if err == nil || !strings.Contains(err.Error(), "destination evidence is attested") || !strings.Contains(err.Error(), "S3 only") {
		t.Fatalf("got %v", err)
	}
	// Without an attested destination the endpoint is fine.
	plans, err := PlanDestinations(config.Archive{Bucket: config.Bucket{Name: "b", Endpoint: "https://r2.example.test"}},
		composedDestinations(t, "profiles:\n  activity:\n    frameworks: [history]\n"))
	if err != nil || plans["activity"].Lock != s3store.None {
		t.Fatalf("plans %v, %v", plans, err)
	}
}

func TestAnAttestedDestinationIsRefusedAnArchiveThatWritesNoLock(t *testing.T) {
	_, err := PlanDestinations(config.Archive{Bucket: config.Bucket{Name: "b"}, LockMode: "none"}, composedDestinations(t, mixed))
	if err == nil || !strings.Contains(err.Error(), "destination evidence is attested") {
		t.Fatalf("got %v", err)
	}
}
