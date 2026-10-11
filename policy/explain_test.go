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
