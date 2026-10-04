package access_test

import (
	"context"
	"testing"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/policy"
)

// A workload reading the console's API is whatever the policy's
// `service_account` matchers make it — and nothing by virtue of being a
// ServiceAccount. Recovery ALSO arrives as a ServiceAccount and is an
// operator by construction, so the two differ only by source; a workload
// that picked up recovery's treatment would make every pod in a
// federated cluster an operator of this installation.
func TestAWorkloadIsWhatItsMatchersMakeItAndNeverRecovery(t *testing.T) {
	t.Parallel()

	declared, err := policy.Parse([]byte(`
version: 1
groups:
  all:access-roster:operator: { members: [platform@example.com] }
  all:access-roster:viewer:
    matchers:
      - service_account: { cluster: mgmt, namespace: access-issuer, name: github-roster }
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	authorizer := access.NewAuthorizer(set, nil, 0)

	workload := func(namespace, name string) access.Principal {
		account := policy.ServiceAccountRef{Cluster: "mgmt", Namespace: namespace, Name: name}
		return access.Principal{
			Subject:        account.Subject(),
			Source:         access.SourceWorkload,
			ServiceAccount: &account,
		}
	}

	// The controller the policy names is a viewer: enough to read who
	// holds a group, never enough to connect or disconnect anything.
	controller, err := authorizer.Authorize(context.Background(), workload("access-issuer", "github-roster"))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if controller.Role != access.RoleViewer {
		t.Errorf("the named controller's role = %q, want viewer", controller.Role)
	}
	if controller.Source != access.SourceWorkload {
		t.Errorf("source = %q, want workload", controller.Source)
	}

	// Any other account in the same cluster, even in the issuer's own
	// namespace, is nobody. Being able to mount a token is not being
	// allowed to ask.
	neighbour, err := authorizer.Authorize(context.Background(), workload("access-issuer", "something-else"))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if neighbour.Role != access.RoleNone {
		t.Errorf("an account the policy does not name has role %q, want none", neighbour.Role)
	}
}

// A Lambda controller's role is a workload too, and what it may do is the
// policy's `aws` matchers: an empty policy admits it to nothing.
func TestAnAWSRoleIsWhatItsMatchersMakeIt(t *testing.T) {
	t.Parallel()

	declared, err := policy.Parse([]byte(`
version: 1
groups:
  all:access-roster:viewer:
    matchers:
      - aws: { account: "111122223333", role: sluis-github }
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	authorizer := access.NewAuthorizer(set, nil, 0)
	role := func(name string) access.Principal {
		r := policy.AWSRole{Account: "111122223333", Path: "/", Name: name}
		return access.Principal{Subject: r.Subject(), Source: access.SourceWorkload, AWS: &r}
	}

	named, err := authorizer.Authorize(context.Background(), role("sluis-github"))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if named.Role != access.RoleViewer || named.Source != access.SourceWorkload {
		t.Errorf("the named role: role %q source %q, want viewer/workload", named.Role, named.Source)
	}
	other, err := authorizer.Authorize(context.Background(), role("someone-else"))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if other.Role == access.RoleViewer || other.Role == access.RoleOperator {
		t.Errorf("an unnamed role got %q", other.Role)
	}
}
