package lambdaapp_test

import (
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/lambdaapp/githubfn"
	"github.com/truvity/sluis/internal/lambdaapp/githubmintfn"
	"github.com/truvity/sluis/internal/lambdaapp/slackfn"
	"github.com/truvity/sluis/policy"
)

// policyWithAnIssuerSecret declares a confidential client whose secret the
// installation must hold: a secret of the issuer's kind, which a provider
// module's check must not report.
func policyWithAnIssuerSecret(t *testing.T) *config.PolicyDocument {
	t.Helper()
	p, err := policy.Parse([]byte("groups:\n  a: {}\nclients:\n  grafana: { kind: confidential, secret: grafana, requires: [a] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	return &config.PolicyDocument{Policy: p}
}

// A function's check reads its own module's kinds and no other's. The GitHub and
// Slack functions declare none (an App's credential is made at run time), so
// their check reports nothing and makes no read, where Check's own "no kinds"
// would have read every kind of the policy.
func TestAProviderFunctionsCheckReportsOnlyItsOwnKinds(t *testing.T) {
	pol := policyWithAnIssuerSecret(t)
	declared := config.DeclaredSecrets(nil, pol)
	if len(declared) != 1 || declared[0].Kind != config.SecretClient {
		t.Fatalf("the fixture declares %+v: it does not test the filter", declared)
	}
	for name, kinds := range map[string][]string{"github": githubfn.Kinds, "github-mint": githubmintfn.Kinds, "slack": slackfn.Kinds} {
		if got := deploycheck.Own(declared, kinds...); len(got) != 0 {
			t.Errorf("%s reads %+v", name, got)
		}
		// No AWS is reached: nothing is declared, so there is nothing to read.
		rep, err := lambdaapp.CheckOwn(&config.Secrets{Source: "ssm", Root: "/sluis/example", Layout: config.SecretsLayoutV5}, pol, kinds...)(t.Context())
		if err != nil || !rep.OK || len(rep.Results) != 0 || rep.Layout != config.SecretsLayoutV5 {
			t.Errorf("%s: %+v, %v", name, rep, err)
		}
	}
	// And the filter does keep what a module owns.
	if got := deploycheck.Own(declared, config.SecretClient); len(got) != 1 {
		t.Errorf("Own dropped the kind it was given: %+v", got)
	}
	// The section is still held to the rules: a store that is not ssm is refused.
	if _, err := lambdaapp.CheckOwn(&config.Secrets{Source: "env"}, pol)(t.Context()); err == nil {
		t.Error("a check over a secrets source that is not ssm passed")
	}
}
