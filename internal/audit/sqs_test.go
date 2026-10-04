package audit_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/port"
)

// fakeSQS is a queue that can be told to fail or to hang.
type fakeSQS struct {
	mu     sync.Mutex
	sent   []types.SendMessageBatchRequestEntry
	calls  int
	down   bool
	hang   bool
	failed map[string]bool // entry ids SQS fails for its own reasons, once
}

func (f *fakeSQS) SendMessageBatch(ctx context.Context, in *sqs.SendMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error) {
	f.mu.Lock()
	f.calls++
	down, hang := f.down, f.hang
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if down {
		return nil, errors.New("queue unreachable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &sqs.SendMessageBatchOutput{}
	for _, e := range in.Entries {
		if f.failed[aws.ToString(e.Id)] {
			delete(f.failed, aws.ToString(e.Id))
			out.Failed = append(out.Failed, types.BatchResultErrorEntry{Id: e.Id, Code: aws.String("InternalError")})
			continue
		}
		f.sent = append(f.sent, e)
		out.Successful = append(out.Successful, types.SendMessageBatchResultEntry{Id: e.Id})
	}
	return out, nil
}

func (f *fakeSQS) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

func signIn() audit.Actor { return audit.Person("a@example.com") }

func openSQS(t *testing.T, q *fakeSQS, url string, sync bool, log *slog.Logger) *audit.Trail {
	t.Helper()
	trail, err := audit.Open(context.Background(), audit.Config{
		SQS: &audit.SQSConfig{QueueURL: url, Timeout: time.Second}, SQSClient: q, Sync: sync, Log: log,
		SyncTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	return trail
}

type lines struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lines) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *lines) String() string               { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// On SQS there is no receiver: opening needs no token, registers nothing, and
// says once that the catalogue travels with the writer.
func TestSQSTrailSkipsRegistrationAndSaysSo(t *testing.T) {
	var out lines
	q := &fakeSQS{}
	trail := openSQS(t, q, "https://sqs.eu-west-1.amazonaws.com/1/audit", false, slog.New(slog.NewTextHandler(&out, nil)))
	if !trail.Connected() {
		t.Fatal("an SQS trail is connected")
	}
	if n := strings.Count(out.String(), "catalogue is not registered"); n != 1 {
		t.Fatalf("the skip is logged %d times, want once:\n%s", n, out.String())
	}
	if q.calls != 0 {
		t.Fatalf("opening called SQS %d times", q.calls)
	}
}

// A block action is on the queue before RecordDurable returns, with the
// record's id as the message attribute the writer reads.
func TestSQSBlockRecordIsQueuedBeforeItReturns(t *testing.T) {
	q := &fakeSQS{}
	trail := openSQS(t, q, "https://sqs/q", false, nil)
	err := trail.RecordDurable(context.Background(),
		audit.RecoverySignedIn(audit.RecoveryIdentity("recovery"), "console", "recovery", audit.Succeeded()))
	if err != nil {
		t.Fatal(err)
	}
	if q.count() != 1 {
		t.Fatalf("queued %d", q.count())
	}
	e := q.sent[0]
	if aws.ToString(e.MessageAttributes["record-id"].StringValue) == "" || e.MessageGroupId != nil {
		t.Fatalf("a standard queue gets the record id and no group: %+v", e)
	}
}

// A FIFO queue gets a group and the record id as the deduplication id.
func TestSQSFifoQueueGetsAGroupAndADeduplicationID(t *testing.T) {
	q := &fakeSQS{}
	trail := openSQS(t, q, "https://sqs/q.fifo", false, nil)
	if err := trail.RecordDurable(context.Background(),
		audit.RecoverySignedIn(audit.RecoveryIdentity("recovery"), "console", "recovery", audit.Succeeded())); err != nil {
		t.Fatal(err)
	}
	e := q.sent[0]
	if aws.ToString(e.MessageGroupId) == "" || aws.ToString(e.MessageDeduplicationId) != aws.ToString(e.MessageAttributes["record-id"].StringValue) {
		t.Fatalf("%+v", e)
	}
}

// With SQS down a block record refuses (that is the recovery sign-in), and an
// async one never fails or blocks its caller, synchronous trail or not.
func TestSQSOutageRefusesABlockRecordButNeverASignIn(t *testing.T) {
	for _, sync := range []bool{false, true} {
		q := &fakeSQS{down: true}
		trail := openSQS(t, q, "https://sqs/q", sync, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := trail.RecordDurable(ctx,
			audit.RecoverySignedIn(audit.RecoveryIdentity("recovery"), "console", "recovery", audit.Succeeded()))
		cancel()
		if err == nil || !strings.Contains(err.Error(), "could not be kept") {
			t.Fatalf("sync=%v: block with the queue down = %v", sync, err)
		}
		start := time.Now()
		for range 5 {
			trail.Record(context.Background(), audit.SignedIn(signIn(), "console", "google", audit.Succeeded()))
		}
		// The first waits the bounded syncWait at most; later ones do not wait.
		if d := time.Since(start); d > 1500*time.Millisecond {
			t.Fatalf("sync=%v: five sign-ins took %s with the queue down", sync, d)
		}
	}
}

// A hung queue costs a synchronous sign-in no more than the wait.
func TestSQSHangCostsASignInOnlyTheWait(t *testing.T) {
	q := &fakeSQS{hang: true}
	trail := openSQS(t, q, "https://sqs/q", true, nil)
	start := time.Now()
	trail.Record(context.Background(), audit.SignedIn(signIn(), "console", "google", audit.Succeeded()))
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a sign-in took %s on a hung queue", d)
	}
}

// Synchronous: the record is on the queue when Record returns, which is what
// a Lambda needs before it freezes.
func TestSyncTrailHasSentWhenRecordReturns(t *testing.T) {
	q := &fakeSQS{}
	trail := openSQS(t, q, "https://sqs/q", true, nil)
	trail.Record(context.Background(), audit.SignedIn(signIn(), "console", "google", audit.Succeeded()))
	if q.count() != 1 {
		t.Fatalf("queued %d after Record returned, want 1", q.count())
	}
}

// Async (Kubernetes): Flush waits for what is queued, and a failed entry is
// sent again.
func TestAsyncTrailFlushDeliversAndRetriesAnEntrySQSFailed(t *testing.T) {
	q := &fakeSQS{failed: map[string]bool{"0": true}}
	trail := openSQS(t, q, "https://sqs/q", false, nil)
	trail.Record(context.Background(), audit.SignedIn(signIn(), "console", "google", audit.Succeeded()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := trail.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if q.count() != 1 {
		t.Fatalf("queued %d, want 1 after the retry", q.count())
	}
}

func TestFromPlan(t *testing.T) {
	sqsChoice := func(s port.Settings) port.Table {
		return port.Table{port.ConcernAudit: {Adapter: "sqs", Settings: s}}
	}
	cfg, err := audit.FromPlan(audit.Config{Writer: "http://w"}, sqsChoice(port.Settings{
		"queueURL": "https://sqs/q", "region": "eu-west-1", "timeout": "2s"}))
	if err != nil || cfg.SQS == nil || cfg.SQS.QueueURL != "https://sqs/q" || cfg.SQS.Region != "eu-west-1" ||
		cfg.SQS.Timeout != 2*time.Second || cfg.Writer != "" {
		t.Fatalf("sqs: %+v %v", cfg, err)
	}
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "sluis-http")
	if cfg, _ = audit.FromPlan(audit.Config{}, sqsChoice(port.Settings{"queueURL": "q"})); !cfg.Sync {
		t.Fatal("on Lambda the trail is synchronous")
	}
	for name, bad := range map[string]port.Settings{
		"no queue": {}, "typo": {"queueUrl": "q", "regoin": "x"}, "timeout": {"queueURL": "q", "timeout": "soon"},
	} {
		if _, err := audit.FromPlan(audit.Config{}, sqsChoice(bad)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// The legacy mapping is untouched, and connect still needs its writer.
	connect := port.Table{port.ConcernAudit: {Adapter: "connect"}}
	if cfg, err = audit.FromPlan(audit.Config{Writer: "http://w", TokenFile: "t"}, connect); err != nil || cfg.Writer != "http://w" || cfg.SQS != nil {
		t.Fatalf("connect: %+v %v", cfg, err)
	}
	if _, err = audit.FromPlan(audit.Config{}, connect); err == nil {
		t.Fatal("connect without a writer")
	}
	if cfg, _ = audit.FromPlan(audit.Config{Writer: "http://w"}, port.Table{port.ConcernAudit: {Adapter: "log"}}); cfg.Writer != "" {
		t.Fatal("log keeps nothing")
	}
	if cfg, err = audit.FromPlan(audit.Config{Writer: "w"}, nil); err != nil || cfg.Writer != "w" {
		t.Fatal("no plan leaves the config alone")
	}
}
