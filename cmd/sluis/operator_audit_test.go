//go:build !lambda

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port/memory"
)

const testQueue = "https://sqs.eu-west-1.amazonaws.com/111122223333/audit-ingest"

// queue is the audit-ingest queue. denied makes it answer as AWS does to a
// principal that may not send; onSend runs as a message arrives.
type queue struct {
	mu     sync.Mutex
	sent   int
	denied bool
	onSend func()
}

func (q *queue) SendMessageBatch(_ context.Context, in *sqs.SendMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.denied {
		return nil, errors.New("operation error SQS: SendMessageBatch, https response error StatusCode: 403, AccessDenied: " +
			"User: arn:aws:sts::111122223333:assumed-role/operator/oleg is not authorized to perform: sqs:SendMessage on resource: " + testQueue)
	}
	if q.onSend != nil {
		q.onSend()
	}
	out := &sqs.SendMessageBatchOutput{}
	for _, e := range in.Entries {
		q.sent++
		out.Successful = append(out.Successful, types.SendMessageBatchResultEntry{Id: e.Id})
	}
	return out, nil
}

func sqsAudit(t *testing.T, q *queue, stderr *bytes.Buffer) *operatorAudit {
	t.Helper()
	trail, err := audit.Open(context.Background(), audit.Config{
		SQS: &audit.SQSConfig{QueueURL: testQueue, Timeout: time.Second}, SQSClient: q,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	return &operatorAudit{rec: trail, dest: "queue " + testQueue, stderr: stderr}
}

func freezeTables() []maintenance.Table {
	return []maintenance.Table{{Name: "oidc", State: memory.New()}, {Name: "github", State: memory.New()}}
}

func anyOn(tables []maintenance.Table) bool {
	for _, s := range maintenance.Statuses(context.Background(), tables) {
		if s.On {
			return true
		}
	}
	return false
}

func runMaint(t *testing.T, sub string, tables []maintenance.Table, au *operatorAudit) (string, error) {
	t.Helper()
	var out bytes.Buffer
	orig := maintenanceSleep
	maintenanceSleep = func(context.Context, time.Duration) {}
	t.Cleanup(func() { maintenanceSleep = orig })
	err := maintenanceRun(context.Background(), &out, sub, tables, "example", "oleg", "layout v5", false, false, false, au)
	return out.String(), err
}

// A denied send refuses the freeze, names the queue and the principal AWS
// refused, and leaves the installation unfrozen.
func TestMaintenanceOnRefusesWhenTheAuditSendIsDenied(t *testing.T) {
	tables := freezeTables()
	_, err := runMaint(t, "on", tables, sqsAudit(t, &queue{denied: true}, &bytes.Buffer{}))
	if !errors.Is(err, ErrAuditRefused) {
		t.Fatalf("on with a denied queue = %v, want ErrAuditRefused", err)
	}
	for _, want := range []string{testQueue, "arn:aws:sts::111122223333:assumed-role/operator/oleg", "--no-audit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if anyOn(tables) {
		t.Error("the freeze was set although its record was refused")
	}
}

// The lift is held to the same rule: a flag that is on stays on.
func TestMaintenanceOffRefusesWhenTheAuditSendIsDenied(t *testing.T) {
	tables := freezeTables()
	if _, err := runMaint(t, "on", tables, sqsAudit(t, &queue{}, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if _, err := runMaint(t, "off", tables, sqsAudit(t, &queue{denied: true}, &bytes.Buffer{})); !errors.Is(err, ErrAuditRefused) {
		t.Fatalf("off with a denied queue = %v, want ErrAuditRefused", err)
	}
	if !anyOn(tables) {
		t.Error("the flag was lifted although its record was refused")
	}
}

// The record is on the queue before the flag is set.
func TestMaintenanceOnDeliversItsRecordBeforeTheFlagIsSet(t *testing.T) {
	tables := freezeTables()
	var onAtSend bool
	q := &queue{}
	q.onSend = func() { onAtSend = anyOn(tables) }
	if _, err := runMaint(t, "on", tables, sqsAudit(t, q, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if q.sent != 1 {
		t.Fatalf("sent %d records, want 1", q.sent)
	}
	if onAtSend {
		t.Error("the flag was already set when the record was sent")
	}
	if !anyOn(tables) {
		t.Error("the freeze was not set")
	}
}

// An unreadable or restore-held table changes nothing, so records nothing and
// does not need the queue.
func TestMaintenanceThatCannotChangeAnythingRecordsNothing(t *testing.T) {
	q := &queue{}
	if _, err := runMaint(t, "off", freezeTables(), sqsAudit(t, q, &bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	if q.sent != 0 {
		t.Errorf("a lift of nothing sent %d records", q.sent)
	}
}

// --no-audit goes ahead, warns loudly, and writes the record to stderr as JSON.
func TestNoAuditProceedsWarnsAndPrintsTheRecord(t *testing.T) {
	tables := freezeTables()
	trail, err := audit.Open(context.Background(), audit.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer trail.Close()
	var stderr bytes.Buffer
	au := &operatorAudit{rec: trail, noAudit: true, stderr: &stderr}
	if _, err = runMaint(t, "on", tables, au); err != nil {
		t.Fatalf("on --no-audit = %v", err)
	}
	if !anyOn(tables) {
		t.Error("--no-audit did not freeze")
	}
	got := stderr.String()
	if !strings.Contains(got, "WARNING") || !strings.Contains(got, "--no-audit") {
		t.Errorf("no loud warning:\n%s", got)
	}
	var line string
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "{") {
			line = l
		}
	}
	for _, want := range []string{`"action":"roster.maintenance.changed"`, `"id":"`, `"state":"migration"`} {
		if !strings.Contains(line, want) {
			t.Errorf("the printed record lacks %s:\n%s", want, got)
		}
	}
}

// A record that could not be kept after the action happened says so and does
// not claim the action failed.
func TestAClosingRecordThatCannotBeKeptSaysTheActionHappened(t *testing.T) {
	au := sqsAudit(t, &queue{denied: true}, &bytes.Buffer{})
	err := au.keepAfter(context.Background(), "the prune", audit.SecretsPruned(audit.Person("oleg"),
		audit.SecretsPrune{Installation: "example", Phase: audit.PhaseCompleted}, audit.Succeeded()))
	if !errors.Is(err, ErrAuditRefused) || !strings.Contains(err.Error(), "the prune already happened") {
		t.Fatalf("keepAfter = %v", err)
	}
}
