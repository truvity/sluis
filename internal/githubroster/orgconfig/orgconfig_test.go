package orgconfig_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/githubroster/orgconfig"
)

func valid() orgconfig.Config {
	return orgconfig.Config{
		Read:              orgconfig.Read{RoleARN: "arn:aws:iam::111122223333:role/sluis-read", Region: "eu-west-1", Parameter: "/example/doc"},
		PlatformVariables: []string{"CI_*"},
		PlatformValued:    []string{"ISSUER"},
		SecretPrefix:      "/example/credentials/",
		Variables:         map[string]orgconfig.Variable{"CI_X": {Value: "x"}, "ISSUER": {Value: "https://x.example"}},
	}
}

func TestAnUnsetModeIsReportAndOnlyEnforceEnforces(t *testing.T) {
	t.Parallel()

	c := valid()
	if c.EffectiveMode() != orgconfig.ModeReport {
		t.Errorf("an unset mode is %q", c.EffectiveMode())
	}

	c.Mode = orgconfig.ModeEnforce
	if c.EffectiveMode() != orgconfig.ModeEnforce {
		t.Errorf("enforce is %q", c.EffectiveMode())
	}

	// A value that is neither never enforces, even if it got past validation.
	c.Mode = "ENFORCE"
	if c.EffectiveMode() != orgconfig.ModeReport {
		t.Errorf("%q enforced", c.Mode)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	if err := valid().Validate(); err != nil {
		t.Fatalf("a valid configuration was refused: %v", err)
	}

	for name, tc := range map[string]struct {
		change func(*orgconfig.Config)
		want   string
	}{
		"a mode":                             {func(c *orgconfig.Config) { c.Mode = "apply" }, "mode"},
		"a role":                             {func(c *orgconfig.Config) { c.Read.RoleARN = "role" }, "roleArn"},
		"a region":                           {func(c *orgconfig.Config) { c.Read.Region = "europe" }, "region"},
		"a parameter":                        {func(c *orgconfig.Config) { c.Read.Parameter = "doc" }, "parameter"},
		"a pattern":                          {func(c *orgconfig.Config) { c.PlatformVariables = []string{"[a"} }, "platform_variables"},
		"a prefix":                           {func(c *orgconfig.Config) { c.SecretPrefix = "/example/secrets" }, "secret_prefix"},
		"an unowned variable":                {func(c *orgconfig.Config) { c.Variables["MINE"] = orgconfig.Variable{Value: "x"} }, "MINE"},
		"a variable no value":                {func(c *orgconfig.Config) { c.Variables["CI_X"] = orgconfig.Variable{} }, "no value"},
		"a document under the secret prefix": {func(c *orgconfig.Config) { c.Read.Parameter = "/example/credentials/doc" }, "under secretPrefix"},
		"an external id":                     {func(c *orgconfig.Config) { c.Read.ExternalID = "x" }, "externalId"},
		"two variables GitHub reads as one": {func(c *orgconfig.Config) {
			c.Variables["ci_x"] = orgconfig.Variable{Value: "y"}
		}, "one name"},
		"selected no repos": {func(c *orgconfig.Config) {
			c.Variables["CI_X"] = orgconfig.Variable{Value: "x", Visibility: "selected"}
		}, "no repository"},
	} {
		c := valid()
		tc.change(&c)

		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, tc.want)
		}
	}
}

func TestSessionPolicyLimitsTheRoleToTheDocumentAndTheSecrets(t *testing.T) {
	t.Parallel()

	c := valid()
	c.Read.ExternalID = "ext-123"

	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}

	// STS allows an underscore in an external id.
	c.Read.ExternalID = "ext_123-ABC"
	if err := c.Validate(); err != nil {
		t.Fatalf("an external id with an underscore: %v", err)
	}

	policy, err := c.SessionPolicy()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`"Action":"ssm:GetParameter"`,
		":ssm:eu-west-1:111122223333:parameter/example/doc",
		":ssm:eu-west-1:111122223333:parameter/example/credentials/*",
		`"kms:ViaService":"ssm.eu-west-1.amazonaws.com"`,
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy %s lacks %q", policy, want)
		}
	}

	// Case is not the platform's to guess: a name written in lower case is the platform's too.
	if !c.Ceiling().PlatformOwns("ci_anything") || !c.Ceiling().ValueOnly("issuer") {
		t.Error("the ceiling must read names without regard to case")
	}
}
