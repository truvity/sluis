package lambdatest

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/githubapp/githubfake"
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/issuerfn" // registers the issuer module; the scenarios open it by document
	"github.com/truvity/sluis/srp"
)

// Instance is the instance name the fixture's SSM root and functions carry.
const Instance = "acme"

// Harness is one installation, in process: the emulator, the SSM its functions
// read secrets from, and the GitHub organisation they talk to. State and blobs
// are the in-memory port.
type Harness struct {
	Emulator *Emulator
	SSM      *SSM
	// GitHub is the organisation githubfake serves. It points the GitHub client
	// at itself for the test's duration, so a test using the harness must not
	// run in parallel with another that does.
	GitHub *githubfake.Org
}

// RecoveryPasswordParam is where layout v5 keeps the recovery password, which
// the function never reads (docs/decisions/0076); RecoveryVerifierParam is the
// recovery account's SRP credential, which it does.
const (
	RecoveryPasswordParam = "/sluis/" + Instance + "/internal/oidc/recovery-password"
	RecoveryVerifierParam = "/sluis/" + Instance + "/internal/oidc/recovery-verifier"
)

// RecoveryPassword is the fixture's recovery password.
const RecoveryPassword = "correct horse battery staple"

// NewHarness starts the emulator, the SSM fake (holding the fixture's declared
// secret) and the GitHub fake. It sets the environment the AWS SDK reads so
// that nothing reaches a real endpoint.
func NewHarness(t *testing.T) *Harness {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "example")
	t.Setenv("AWS_REGION", Region)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_PROFILE", "")
	return &Harness{
		Emulator: New(t),
		SSM: NewSSM(t, map[string]string{
			RecoveryPasswordParam: `{"value":"` + base64.StdEncoding.EncodeToString([]byte(RecoveryPassword)) + `"}`,
			RecoveryVerifierParam: `{"value":"` + base64.StdEncoding.EncodeToString(srp.Default.NewCredential("recovery", RecoveryPassword).Document()) + `"}`,
		}),
		GitHub: githubfake.Start(t, "acme-org"),
	}
}

// IssuerDocument writes the issuer's service document: layout v5 secrets in
// the harness's SSM, state and blobs in memory, and the run-now trigger in
// memory (the v1.76 scenarios replace it with the invoke trigger aimed at the
// emulator). It returns the file's path.
func (h *Harness) IssuerDocument(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policy, []byte(`
version: 1
groups:
  all:access-roster:operator:
    members: [platform@acme.example]
    matchers: [{service_account: {namespace: sluis, name: recovery}}]
  all:access-roster:viewer: {}
lifetimes: { default: 12h }
clients:
  console: { kind: public, requires: [all:access-roster:operator], redirects: ["https://access.acme.example/console/callback"] }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sluis.yaml")
	if err := os.WriteFile(file, []byte(`apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: https://access.acme.example
publicURL: https://access.acme.example/console
policy: {file: `+policy+`}
secrets: {source: ssm, root: /sluis/`+Instance+`, layout: v5, endpoint: `+h.SSM.URL()+`}
recovery: {enabled: true, verifierSecret: recovery/verifier}
adapters:
  secrets: {adapter: ssm, settings: {endpoint: `+h.SSM.URL()+`}}
  state: {adapter: memory}
  blobs: {adapter: memory}
  trigger: {adapter: memory}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// OpenIssuer assembles the issuer function from its document, as a cold start
// does, and registers it with the emulator as sluis-<instance>-issuer.
func (h *Harness) OpenIssuer(t *testing.T) string {
	t.Helper()
	file := h.IssuerDocument(t)
	fn, err := lambdaapp.OpenModule(context.Background(), func(k string) string {
		if k == config.EnvConfig {
			return file
		}
		return ""
	}, lambdaapp.ModuleIssuer)
	if err != nil {
		t.Fatalf("open the issuer: %v", err)
	}
	t.Cleanup(fn.Close)
	name := "sluis-" + Instance + "-issuer"
	h.Emulator.Register(name, fn)
	return name
}
