package suite

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// envKMS turns on the tests of the "services in the cluster" lane, which
// installs e2e-kms.yaml over e2e.yaml: the notary signs with a KMS key, reached
// through the SDK's default credential chain, with the endpoint and credentials
// from a Secret, and nothing in the cluster reads SQS, DynamoDB or SSM.
const envKMS = "E2E_KMS"

// runJob starts a Job from a CronJob of the release and waits for it to
// complete, returning its log. A Job that fails is a failed test with the log.
func runJob(t *testing.T, cronjob string, patience time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience+time.Minute)
	defer cancel()

	kctx := getenv(envKubecontext, defaultKubecontext)
	ns := shared.names.Namespace
	job := cronjob + "-e2e-" + randomTenant(t)[len("e2e-suite-"):]
	kubectl := func(args ...string) (string, error) {
		argv := append([]string{"--context", kctx, "-n", ns}, args...)
		out, err := exec.CommandContext(ctx, "kubectl", argv...).CombinedOutput() //nolint:gosec // fixed argv, no shell
		return string(out), err
	}
	if out, err := kubectl("create", "job", job, "--from=cronjob/"+cronjob); err != nil {
		t.Fatalf("start %s: %v\n%s", cronjob, err, out)
	}
	defer func() { _, _ = kubectl("delete", "job", job, "--ignore-not-found") }()

	logs, _ := kubectl("wait", "--for=condition=complete", "job/"+job, "--timeout="+patience.String())
	out, _ := kubectl("logs", "job/"+job, "--all-containers", "--tail=200")
	if !strings.Contains(logs, "condition met") {
		t.Fatalf("%s did not complete: %s\nlog:\n%s", cronjob, logs, out)
	}
	return out
}

// TestNotaryOpensItsKMSKey runs the notary once. It opens the seal key at
// start-up (the alias, its type and its public half) before it looks at an hour,
// so a Job that completes has reached KMS with the credentials and the endpoint
// the Secret gave it; with the endpoint ignored it would have gone to AWS and
// failed. A fresh archive has no closed hour yet, so there is nothing to seal.
func TestNotaryOpensItsKMSKey(t *testing.T) {
	if os.Getenv(envKMS) == "" {
		t.Skipf("%s is not set: this lane installs the KMS overlay", envKMS)
	}
	logs := runJob(t, shared.names.Release+"-notary", 3*time.Minute)
	t.Logf("notary log:\n%s", logs)
}

// TestVerifyReadsTheArchive runs the verifier once over the archive the suite
// has written to, with the same static credentials the writers use.
func TestVerifyReadsTheArchive(t *testing.T) {
	if os.Getenv(envKMS) == "" {
		t.Skipf("%s is not set: this lane installs the KMS overlay", envKMS)
	}
	logs := runJob(t, shared.names.Release+"-verify", 3*time.Minute)
	t.Logf("verify log:\n%s", logs)
}
