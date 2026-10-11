package policy_test

import (
	"testing"

	"github.com/truvity/sluis/policy"
)

func TestWorkloadInputReadsEverySubjectSpelling(t *testing.T) {
	t.Parallel()

	if in, ok := policy.WorkloadInput("aws:111122223333:role/service/team/deployer"); !ok ||
		in.AWS == nil || in.AWS.Account != "111122223333" || in.AWS.Path != "/service/team/" || in.AWS.Name != "deployer" {
		t.Errorf("aws role with a path: %+v, %v", in.AWS, ok)
	}
	if in, ok := policy.WorkloadInput("aws:111122223333:role/deployer"); !ok || in.AWS == nil || in.AWS.Path != "/" || in.AWS.Name != "deployer" {
		t.Errorf("aws role: %+v, %v", in.AWS, ok)
	}
	if in, ok := policy.WorkloadInput("system:serviceaccount:acme:worker"); !ok || in.ServiceAccount == nil || in.ServiceAccount.Name != "worker" {
		t.Errorf("service account: %+v, %v", in.ServiceAccount, ok)
	}
	for _, bad := range []string{"", "aws:1:role/", "aws::role/x", "someone@example.test"} {
		if _, ok := policy.WorkloadInput(bad); ok {
			t.Errorf("%q was read as a workload", bad)
		}
	}
}

func TestExplainRefusesAnAudienceThatIsNoClient(t *testing.T) {
	t.Parallel()

	if _, err := (policy.Policy{Version: 1}).Explain("nowhere", policy.Input{}); err == nil {
		t.Error("an unknown audience was explained")
	}
}

// The explanation says how long the token lives (the shortest across the held
// groups, capped by the client) and which required group admits the caller.
func TestExplainSaysTheLifetimeAndTheAdmittingGroup(t *testing.T) {
	t.Parallel()

	declared, err := policy.Parse([]byte(`
version: 1
groups:
  a:t:one: { members: [one@example.test] }
  a:t:two: { members: [one@example.test] }
lifetimes: { default: 12h, a:t:two: 4h }
clients:
  capped: { kind: exchange, requires: [a:t:none, a:t:two, a:t:one], ttl_cap: 1h }
  plain:  { kind: exchange, requires: [a:t:one] }
  closed: { kind: exchange, requires: [a:t:none] }
`))
	if err != nil {
		t.Fatal(err)
	}
	in := policy.ExplainInput("one@example.test", []string{"one@example.test"})
	for audience, want := range map[string]struct {
		lifetime, by string
		admitted     bool
	}{
		"capped": {"1h0m0s", "a:t:two", true},
		"plain":  {"4h0m0s", "a:t:one", true},
		"closed": {"4h0m0s", "", false},
	} {
		got, err := declared.Explain(audience, in)
		if err != nil {
			t.Fatalf("%s: %v", audience, err)
		}
		if got.Lifetime != want.lifetime || got.AdmittedBy != want.by || got.Admitted != want.admitted {
			t.Errorf("%s: lifetime %q admitted_by %q admitted %v, want %+v", audience, got.Lifetime, got.AdmittedBy, got.Admitted, want)
		}
	}
}
