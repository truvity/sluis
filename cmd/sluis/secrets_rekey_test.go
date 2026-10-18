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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/rekey"
)

const rekeyQueueURL = "https://sqs.eu-west-1.amazonaws.com/111122223333/audit-ingest"

// rekeyQueue is the audit queue; denied answers as AWS does to a principal that may not send.
type rekeyQueue struct {
	mu     sync.Mutex
	sent   int
	denied bool
	onSend func()
}

func (q *rekeyQueue) SendMessageBatch(_ context.Context, in *sqs.SendMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.denied {
		return nil, errors.New("operation error SQS: SendMessageBatch, https response error StatusCode: 403, AccessDenied: " +
			"User: arn:aws:sts::111122223333:assumed-role/operator/oleg is not authorized to perform: sqs:SendMessage on resource: " + rekeyQueueURL)
	}
	if q.onSend != nil {
		q.onSend()
	}
	out := &sqs.SendMessageBatchOutput{}
	for _, e := range in.Entries {
		q.sent++
		out.Successful = append(out.Successful, sqstypes.SendMessageBatchResultEntry{Id: e.Id})
	}
	return out, nil
}

// parameters is a Parameter Store with two parameters on the AWS-managed key.
type parameters struct {
	keys   map[string]string
	writes int
}

func newParameters() *parameters {
	return &parameters{keys: map[string]string{"/sluis/example/internal/oidc/a": "alias/aws/ssm", "/sluis/example/external/oidc/b": "alias/aws/ssm"}}
}

func (p *parameters) DescribeParameters(context.Context, *awsssm.DescribeParametersInput, ...func(*awsssm.Options)) (*awsssm.DescribeParametersOutput, error) {
	out := &awsssm.DescribeParametersOutput{}
	for n, k := range p.keys {
		out.Parameters = append(out.Parameters, types.ParameterMetadata{Name: aws.String(n), Type: types.ParameterTypeSecureString,
			KeyId: aws.String(k), Version: 3, LastModifiedDate: aws.Time(time.Now().Add(-100 * 24 * time.Hour))})
	}
	return out, nil
}

func (p *parameters) GetParameters(_ context.Context, in *awsssm.GetParametersInput, _ ...func(*awsssm.Options)) (*awsssm.GetParametersOutput, error) {
	out := &awsssm.GetParametersOutput{}
	for _, n := range in.Names {
		out.Parameters = append(out.Parameters, types.Parameter{Name: aws.String(n), Value: aws.String("SECRET-VALUE"), Version: 3})
	}
	return out, nil
}

func (p *parameters) PutParameter(_ context.Context, in *awsssm.PutParameterInput, _ ...func(*awsssm.Options)) (*awsssm.PutParameterOutput, error) {
	p.keys[aws.ToString(in.Name)] = aws.ToString(in.KeyId)
	p.writes++
	return &awsssm.PutParameterOutput{Version: 4}, nil
}

func rekeyOptions() rekey.Options {
	return rekey.Options{Root: "/sluis/example", KeyID: "alias/example-secrets",
		Sleep: func(context.Context, time.Duration) error { return nil }}
}

func sqsRekeyAudit(t *testing.T, q *rekeyQueue, stderr *bytes.Buffer) *operatorAudit {
	t.Helper()
	trail, err := audit.Open(context.Background(), audit.Config{
		SQS: &audit.SQSConfig{QueueURL: rekeyQueueURL, Timeout: time.Second}, SQSClient: q,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	return &operatorAudit{rec: trail, dest: "queue " + rekeyQueueURL, stderr: stderr}
}

// A denied send refuses the re-key, names the queue and the principal AWS
// refused, and writes no parameter.
func TestRekeyRefusesWhenTheAuditSendIsDenied(t *testing.T) {
	p := newParameters()
	var out bytes.Buffer
	err := rekeyRun(context.Background(), &out, "example", p, rekeyOptions(),
		sqsRekeyAudit(t, &rekeyQueue{denied: true}, &bytes.Buffer{}), audit.Person("oleg@example.com"), false)
	if err == nil {
		t.Fatal("the re-key went ahead without its record")
	}
	for _, want := range []string{rekeyQueueURL, "arn:aws:sts::111122223333:assumed-role/operator/oleg", "--no-audit", "nothing was changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if p.writes != 0 {
		t.Errorf("%d parameters were written", p.writes)
	}
}

// The record reaches the queue before the first parameter is written, a closing
// record follows, and nothing printed is a value.
func TestRekeyDeliversItsRecordBeforeTheFirstWriteAndAgainWhenItEnds(t *testing.T) {
	p := newParameters()
	q := &rekeyQueue{}
	writesAtFirstSend := -1
	q.onSend = func() {
		if writesAtFirstSend < 0 {
			writesAtFirstSend = p.writes
		}
	}
	var out bytes.Buffer
	if err := rekeyRun(context.Background(), &out, "example", p, rekeyOptions(),
		sqsRekeyAudit(t, q, &bytes.Buffer{}), audit.Person("oleg@example.com"), false); err != nil {
		t.Fatal(err)
	}
	if writesAtFirstSend != 0 || q.sent != 2 || p.writes != 2 {
		t.Errorf("writes at the first send %d, records %d, writes %d: want 0, 2, 2", writesAtFirstSend, q.sent, p.writes)
	}
	if strings.Contains(out.String(), "SECRET-VALUE") {
		t.Error("the output holds a value")
	}
	for n, k := range p.keys {
		if k != "alias/example-secrets" {
			t.Errorf("%s is under %s", n, k)
		}
	}
}

// A dry run records nothing and needs no queue.
func TestRekeyDryRunRecordsNothing(t *testing.T) {
	p := newParameters()
	o := rekeyOptions()
	o.DryRun = true
	var out bytes.Buffer
	if err := rekeyRun(context.Background(), &out, "example", p, o, nil, audit.Person("oleg@example.com"), false); err != nil {
		t.Fatal(err)
	}
	if p.writes != 0 || !strings.Contains(out.String(), "nothing was written") {
		t.Errorf("writes %d, output %q", p.writes, out.String())
	}
}

// dryKMS resolves the estate's alias and key ARN to one key and refuses the rest.
type dryKMS struct{}

func (dryKMS) DescribeKey(_ context.Context, in *kms.DescribeKeyInput, _ ...func(*kms.Options)) (*kms.DescribeKeyOutput, error) {
	const arn = "arn:aws:kms:eu-central-1:111122223333:key/estate"
	switch aws.ToString(in.KeyId) {
	case "alias/example-secrets", arn:
		return &kms.DescribeKeyOutput{KeyMetadata: &kmstypes.KeyMetadata{Arn: aws.String(arn)}}, nil
	case "alias/aws/ssm":
		return &kms.DescribeKeyOutput{KeyMetadata: &kmstypes.KeyMetadata{Arn: aws.String("arn:aws:kms:eu-central-1:111122223333:key/ssm")}}, nil
	}
	return nil, errors.New("AccessDeniedException")
}

// The dry run says which spelling resolved to what, counts the parameters
// already on the key under another spelling, and names the unknown ones.
func TestRekeyDryRunShowsTheKeysAndTheUnknown(t *testing.T) {
	p := newParameters()
	p.keys["/sluis/example/internal/oidc/a"] = "arn:aws:kms:eu-central-1:111122223333:key/estate"
	p.keys["/sluis/example/external/oidc/b"] = "alias/aws/ssm"
	p.keys["/sluis/example/external/oidc/c"] = "9999-some-key-id"
	o := rekeyOptions()
	o.DryRun, o.KMS = true, dryKMS{}
	var out bytes.Buffer
	if err := rekeyRun(context.Background(), &out, "example", p, o, nil, audit.Person("oleg@example.com"), false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"3 SecureString parameters, 1 already under the key", "to re-write: 1", "1 under alias/aws/ssm -> arn:aws:kms:eu-central-1:111122223333:key/ssm",
		"[the key]", "unknown key (DescribeKey refused; left alone unless --include-unknown): /sluis/example/external/oidc/c"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if p.writes != 0 {
		t.Errorf("a dry run wrote %d", p.writes)
	}
}

// --no-audit goes ahead, warns loudly and prints the record to stderr as JSON.
func TestRekeyNoAuditProceedsWarnsAndPrintsTheRecord(t *testing.T) {
	p := newParameters()
	trail, err := audit.Open(context.Background(), audit.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = trail.Close() }()
	var stderr bytes.Buffer
	au := &operatorAudit{rec: trail, noAudit: true, stderr: &stderr}
	if err = rekeyRun(context.Background(), &bytes.Buffer{}, "example", p, rekeyOptions(), au, audit.Person("oleg@example.com"), false); err != nil {
		t.Fatal(err)
	}
	if p.writes != 2 {
		t.Errorf("--no-audit wrote %d parameters", p.writes)
	}
	got := stderr.String()
	if !strings.Contains(got, "WARNING") || strings.Count(got, `"action":"roster.secrets.rekeyed"`) != 2 || !strings.Contains(got, `"phase":"started"`) {
		t.Errorf("stderr:\n%s", got)
	}
}

func TestSecretsCmdTakesItsOwnFlagsAndRefusesUnknownCommands(t *testing.T) {
	var out bytes.Buffer
	if err := secretsCmd(&out, []string{"bogus"}); err == nil {
		t.Error("an unknown command was accepted")
	}
	if err := secretsCmd(&out, []string{"rekey", "--by"}); err == nil {
		t.Error("--by without a value was accepted")
	}
	if err := secretsCmd(&out, []string{"--help"}); err != nil || !strings.Contains(out.String(), "rekey") {
		t.Errorf("--help = %v, %q", err, out.String())
	}
}
