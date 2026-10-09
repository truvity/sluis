package suite

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/record"
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

// TestPseudonymsAreMadeUnderKMSWithTheKeysInTheDatabase emits records naming
// the same outside person twice and a second one once. The index must carry
// pseudonyms and not the identifiers, the same pseudonym for the same person,
// and the wrapped per-tenant secret must be a row in the writer's database.
func TestPseudonymsAreMadeUnderKMSWithTheKeysInTheDatabase(t *testing.T) {
	if os.Getenv(envKMS) == "" {
		t.Skipf("%s is not set: this lane installs the KMS overlay", envKMS)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	defer func() {
		if t.Failed() {
			dumpWriterLogs(t)
		}
	}()

	tenant := randomTenant(t)
	e := writerEmitter(ctx, t)
	defer func() { _ = e.Close() }()

	people := []string{"alice@example.test", "alice@example.test", "bob@example.test"}
	for _, who := range people {
		r := &record.Record{
			Action:    "audit.search",
			Operation: auditv1.Operation_OPERATION_ACCESS,
			TenantId:  tenant,
			Actor:     &record.Actor{Kind: "service", Id: "e2e-suite"},
			// A kind the common catalogue does not declare is an outside
			// person's: the security profile pseudonymises it.
			Subject: &record.Party{Kind: "customer", Id: who},
			Targets: []*record.Target{{Type: "profile", Id: "security"}},
			Outcome: &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
		}
		if err := e.Record(ctx, r); err != nil {
			t.Fatalf("emit for %s: %v", who, err)
		}
	}

	readerDB := openDSN(ctx, t, shared.queryDSN)
	defer func() { _ = readerDB.Close() }()
	conn, err := readerDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	tenants := fmt.Sprintf(`["%s"]`, tenant)
	if _, err := conn.ExecContext(ctx, `select set_config('audit.tenant_ids', $1, false)`, tenants); err != nil {
		t.Fatal(err)
	}

	var subjects []string
	eventually(t, 150*time.Second, func() error {
		rows, err := conn.QueryContext(ctx,
			`select subject_id from events_core join events_context using (profile, id, recorded_at)
			 where tenant_id = $1 and action = 'audit.search' order by subject_id`, tenant)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		subjects = nil
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			subjects = append(subjects, s)
		}
		if len(subjects) < len(people) {
			return fmt.Errorf("%d of %d records indexed", len(subjects), len(people))
		}
		return rows.Err()
	})
	distinct := map[string]int{}
	for _, s := range subjects {
		if s == "" || strings.Contains(s, "example.test") {
			t.Fatalf("subject_id %q is not a pseudonym", s)
		}
		distinct[s]++
	}
	if len(distinct) != 2 {
		t.Fatalf("want two pseudonyms for two people, got %v", distinct)
	}

	// The wrapped secret for the tenant is a row in the writer's database.
	writerDB := openDSN(ctx, t, shared.writerDSN)
	defer func() { _ = writerDB.Close() }()
	var wrapped int
	// The id the kms adapter gives the (profile, tenant) pair: the profile is the
	// purpose of the pseudonym key and is part of what is encoded.
	suffix := "mac/pseudonym/" + base64.RawURLEncoding.EncodeToString([]byte("security/"+tenant))
	if err := writerDB.QueryRowContext(ctx, `select count(*) from audit_wrapped_keys where id like $1`, suffix).Scan(&wrapped); err != nil {
		t.Fatalf("read the wrapped keys as the writer role: %v", err)
	}
	if wrapped != 1 {
		var ids []string
		rows, _ := writerDB.QueryContext(ctx, `select id from audit_wrapped_keys`)
		for rows != nil && rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		t.Fatalf("want one wrapped key for the tenant %s in the database, got %d; the table holds %v", tenant, wrapped, ids)
	}
}

// dumpWriterLogs prints what the release's pods said, for a test that failed: the
// shared dump step matches pods by the lane's name, which is not the release's.
func dumpWriterLogs(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "kubectl", //nolint:gosec // fixed argv, no shell
		"--context", getenv(envKubecontext, defaultKubecontext), "-n", shared.names.Namespace,
		"logs", "-l", "app.kubernetes.io/instance="+shared.names.Release, "--all-containers", "--prefix", "--tail=60").CombinedOutput()
	t.Logf("the release's pods:\n%s", out)
}
