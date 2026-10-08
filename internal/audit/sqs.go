package audit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/audit/sdk/sink"
)

// SQSConfig is where the `sqs` audit adapter publishes. The queue is the one
// the audit writer Lambda consumes; nothing else is configured here, because
// the writer, not sluis, owns the archive.
type SQSConfig struct {
	// QueueURL is the queue records are sent to. A URL ending in `.fifo` is a
	// FIFO queue: the tenant is the message group and the record id the
	// deduplication id.
	QueueURL string
	// Region and Endpoint are for the AWS client. Region empty is the default
	// chain's; Endpoint is for an emulator.
	Region   string
	Endpoint string
	// Timeout bounds one send, retries included. Default 5s: it is also the
	// longest a sign-in waits on Lambda.
	Timeout time.Duration
}

// SQSAPI is the part of the SQS client the publisher calls; *sqs.Client
// satisfies it, and so does a test's fake.
type SQSAPI interface {
	SendMessageBatch(ctx context.Context, in *sqs.SendMessageBatchInput,
		opts ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error)
}

// recordIDAttribute is the message attribute carrying the record's id, the
// name the audit writer's SQS consumer reads (sink/sqssink in truvity/audit).
// The body is the canonical record, the same bytes sqssink publishes.
const recordIDAttribute = "record-id"

const (
	sqsMaxBatchEntries = 10
	sqsMaxBatchBytes   = 256 << 10
	sqsAttempts        = 3
)

// sqsPublisher is a [sink.Sink] that puts records on a queue. It speaks the
// wire format of truvity/audit's sqssink publisher, which is not imported
// because that module sits in the same repository as the receiver and the
// writer and pulls their dependencies (and sluis itself) with it.
type sqsPublisher struct {
	api     SQSAPI
	queue   string
	fifo    bool
	timeout time.Duration
}

func newSQSPublisher(api SQSAPI, c SQSConfig) (*sqsPublisher, error) {
	if api == nil {
		return nil, errors.New("audit: an SQS client is required")
	}
	if c.QueueURL == "" {
		return nil, errors.New("audit: the sqs adapter needs a queue URL (settings.queueURL)")
	}
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	return &sqsPublisher{api: api, queue: c.QueueURL, fifo: strings.HasSuffix(c.QueueURL, ".fifo"), timeout: c.Timeout}, nil
}

// openSQSClient builds the client from the default credential chain, which on
// Lambda is the function's role and on Kubernetes Pod Identity.
func openSQSClient(ctx context.Context, c SQSConfig) (*sqs.Client, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if c.Region != "" {
		opts = append(opts, awsconfig.WithRegion(c.Region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("audit: loading the AWS configuration: %w", err)
	}
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
	}), nil
}

// Guarantees implements sink.Guarantor: SQS stores a message on several
// servers before it answers, which is "queued" and never more.
func (p *sqsPublisher) Guarantees() sink.Durability { return sink.Queued }

// Write implements sink.Sink. It reports Queued once SQS has accepted every
// message; an entry SQS fails for its own reasons is sent again a few times,
// and if it still fails the write fails and the emitter retries the batch,
// which the record id makes harmless.
func (p *sqsPublisher) Write(ctx context.Context, req *sink.Request) (*sink.Result, error) {
	ctx, done := sink.Observe(ctx, sink.TransportSQS, trace.SpanKindProducer, req)
	res, err := p.write(ctx, req)
	done(res, err)
	return res, err
}

type sqsOutgoing struct {
	id, tenant string
	body       []byte
}

func (p *sqsPublisher) write(ctx context.Context, req *sink.Request) (*sink.Result, error) {
	result := &sink.Result{Durability: sink.Queued}
	if len(req.Records) == 0 {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	carrier := attributeCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	var pending []sqsOutgoing
	for _, r := range req.Records {
		line, err := record.Canonical(r)
		if err != nil {
			result.Rejected = append(result.Rejected, sink.Rejection{ID: r.GetId(), Reason: "cannot be encoded: " + err.Error()})
			continue
		}
		if len(line) > sqsMaxBatchBytes {
			result.Rejected = append(result.Rejected, sink.Rejection{ID: r.GetId(), Reason: "is larger than one SQS message can be"})
			continue
		}
		pending = append(pending, sqsOutgoing{id: r.GetId(), tenant: r.GetTenantId(), body: line})
	}

	pause := 50 * time.Millisecond
	for attempt := 1; len(pending) > 0; attempt++ {
		var failed []sqsOutgoing
		for _, chunk := range sqsChunks(pending) {
			again, err := p.send(ctx, chunk, carrier, result)
			if err != nil {
				return nil, err
			}
			failed = append(failed, again...)
		}
		if len(failed) == 0 {
			break
		}
		if attempt >= sqsAttempts {
			return nil, fmt.Errorf("audit: SQS did not take %d of %d records after %d attempts", len(failed), len(req.Records), attempt)
		}
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return nil, fmt.Errorf("audit: waiting to send again: %w", ctx.Err())
		}
		pause *= 2
		pending = failed
	}
	return result, nil
}

func sqsChunks(all []sqsOutgoing) [][]sqsOutgoing {
	var out [][]sqsOutgoing
	var cur []sqsOutgoing
	size := 0
	for _, o := range all {
		if len(cur) == sqsMaxBatchEntries || size+len(o.body) > sqsMaxBatchBytes {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, o)
		size += len(o.body)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func (p *sqsPublisher) send(ctx context.Context, batch []sqsOutgoing, carrier attributeCarrier, result *sink.Result) ([]sqsOutgoing, error) {
	entries := make([]types.SendMessageBatchRequestEntry, len(batch))
	for i, o := range batch {
		e := types.SendMessageBatchRequestEntry{
			Id:          aws.String(strconv.Itoa(i)),
			MessageBody: aws.String(string(o.body)),
			MessageAttributes: map[string]types.MessageAttributeValue{
				recordIDAttribute: {DataType: aws.String("String"), StringValue: aws.String(o.id)},
			},
		}
		if p.fifo {
			group := o.tenant
			if group == "" {
				group = "audit"
			}
			e.MessageGroupId = aws.String(group)
			e.MessageDeduplicationId = aws.String(o.id)
		}
		for k, v := range carrier {
			e.MessageAttributes[k] = v
		}
		entries[i] = e
	}
	out, err := p.api.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(p.queue), Entries: entries})
	if err != nil {
		return nil, fmt.Errorf("audit: sqs send: %w", err)
	}
	// Hold SQS to its own count: an entry that is neither successful nor
	// failed must not be counted accepted on the strength of silence.
	if len(out.Successful)+len(out.Failed) != len(batch) {
		return nil, fmt.Errorf("audit: SQS answered for %d of %d messages", len(out.Successful)+len(out.Failed), len(batch))
	}
	failed := map[string]types.BatchResultErrorEntry{}
	for _, f := range out.Failed {
		failed[aws.ToString(f.Id)] = f
	}
	var again []sqsOutgoing
	for i, o := range batch {
		f, bad := failed[strconv.Itoa(i)]
		switch {
		case !bad:
			result.Accepted++
		case f.SenderFault:
			result.Rejected = append(result.Rejected, sink.Rejection{
				ID: o.id, Reason: fmt.Sprintf("SQS refused it: %s: %s", aws.ToString(f.Code), aws.ToString(f.Message)),
			})
		default:
			again = append(again, o)
		}
	}
	return again, nil
}

// attributeCarrier is a message's attributes as a W3C trace-context carrier.
type attributeCarrier map[string]types.MessageAttributeValue

func (a attributeCarrier) Get(key string) string { return aws.ToString(a[key].StringValue) }
func (a attributeCarrier) Set(key, value string) {
	a[key] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(value)}
}
func (a attributeCarrier) Keys() []string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	return keys
}
