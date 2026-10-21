package policy_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
)

const limitsBase = `
version: 1
groups:
  all:sluis:viewer:
    matchers:
      - aws: { account: "111122223333", role: "sluis-*", path: "/" }
      - service_account: { namespace: sluis, name: sluis }
clients:
  otlp:    { kind: exchange, requires: [all:sluis:viewer] }
  console: { kind: exchange, requires: [all:sluis:viewer] }
  grafana: { kind: exchange, requires: [all:sluis:viewer] }
`

func TestExchangeAllowed(t *testing.T) {
	t.Parallel()
	set := compile(t, limitsBase+`
exchange_limits:
  - match: { aws: { account: "111122223333", role: "sluis-*", path: "/" } }
    clients: [otlp]
  - match: { aws: { account: "111122223333", role: "sluis-console", path: "/" } }
    clients: [console]
`)
	fn := func(name string) policy.Input {
		return policy.Input{AWS: &policy.AWSRole{Account: "111122223333", Path: "/", Name: name}}
	}
	for _, tc := range []struct {
		name     string
		in       policy.Input
		audience string
		want     bool
	}{
		{"an audience its limit names", fn("sluis-github"), "otlp", true},
		{"an audience no matching limit names", fn("sluis-github"), "console", false},
		{"one more by a second matching limit: the union", fn("sluis-console"), "console", true},
		{"and still the first's", fn("sluis-console"), "otlp", true},
		{"but nothing past the union", fn("sluis-console"), "grafana", false},
		{"another path is another identity", policy.Input{AWS: &policy.AWSRole{Account: "111122223333", Path: "/x/", Name: "sluis-github"}}, "grafana", true},
		{"a workload no limit matches", policy.Input{ServiceAccount: &policy.ServiceAccountRef{Namespace: "sluis", Name: "sluis"}}, "grafana", true},
		{"a person no limit matches", policy.Input{Email: "ada@example.com"}, "console", true},
	} {
		if got := set.ExchangeAllowed(tc.in, tc.audience); got != tc.want {
			t.Errorf("%s: ExchangeAllowed(%s) = %v, want %v", tc.name, tc.audience, got, tc.want)
		}
	}

	clients, limited := set.ExchangeClients(fn("sluis-console"))
	if !limited || !slices.Equal(clients, []string{"console", "otlp"}) {
		t.Errorf("ExchangeClients = %v %v, want the sorted union", clients, limited)
	}
	if clients, limited := set.ExchangeClients(policy.Input{Email: "ada@example.com"}); limited || clients != nil {
		t.Errorf("ExchangeClients for an unlimited proof = %v %v", clients, limited)
	}
}

// A limit is matched on the identity alone: a person's directory groups do
// not make an email matcher match, and holding a group changes nothing.
func TestAnExchangeLimitReadsTheIdentityOnly(t *testing.T) {
	t.Parallel()
	set := compile(t, limitsBase+`
exchange_limits:
  - match: { email_domain: example.com }
    clients: [console]
`)
	in := policy.Input{Email: "ada@example.com", DirectoryGroups: []string{"admins@example.com"}, Authoritative: true}
	if set.ExchangeAllowed(in, "otlp") {
		t.Error("a person in the limited domain reached otlp")
	}
	if !set.ExchangeAllowed(in, "console") {
		t.Error("a person in the limited domain was kept from console")
	}
	if !set.ExchangeAllowed(policy.Input{Email: "eve@example.org"}, "otlp") {
		t.Error("a person of another domain was limited")
	}
}

func TestExchangeLimitsAreValidated(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ limits, want string }{
		"an undeclared client": {
			"  - match: { email: a@example.com }\n    clients: [otlp, nowhere]\n", `"nowhere" is not a declared client`,
		},
		"no clients": {
			"  - match: { email: a@example.com }\n    clients: []\n", "no clients",
		},
		"a client twice": {
			"  - match: { email: a@example.com }\n    clients: [otlp, otlp]\n", "listed twice",
		},
		"a match with no condition": {
			"  - match: {}\n    clients: [otlp]\n", "exchange_limits[0]: match has no condition",
		},
		"a match with two conditions": {
			"  - match: { email: a@example.com, email_domain: example.com }\n    clients: [otlp]\n", "exactly one is allowed",
		},
		"a match on every CI job": {
			"  - match: { github: {} }\n    clients: [otlp]\n", "matches every CI job",
		},
		"a malformed AWS account": {
			"  - match: { aws: { account: \"1234\" } }\n    clients: [otlp]\n", "12-digit",
		},
	} {
		parsed, err := policy.Parse([]byte(limitsBase + "exchange_limits:\n" + tc.limits))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if _, err := policy.NewSet(parsed); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: NewSet = %v, want an error containing %q", name, err, tc.want)
		}
	}

	// A misspelt key is the parser's to refuse, as everywhere in the policy.
	if _, err := policy.Parse([]byte(limitsBase + "exchange_limits:\n  - match: { email: a@example.com }\n    client: [otlp]\n")); err == nil {
		t.Error("a limit with `client:` for `clients:` parsed")
	}
}
