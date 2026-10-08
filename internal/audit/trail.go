package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/truvity/sluis/audit/sdk/auth"
	"github.com/truvity/sluis/audit/sdk/catalogue"
	"github.com/truvity/sluis/audit/sdk/emit"
	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/audit/sdk/sink"
	"go.opentelemetry.io/otel"

	"github.com/truvity/sluis/internal/logsafe"
)

// Config says which installation to connect to, if any.
type Config struct {
	// Writer is the installation's receiver: the address records are sent to,
	// and the same address the catalogue is registered with, because the
	// receiver serves that call. Empty is a trail that is not connected:
	// every record is validated against the catalogue and written to the log,
	// and kept nowhere else.
	Writer string
	// SQS, when set, publishes records to a queue instead of a receiver (the
	// `sqs` adapter; see [FromPlan]). Writer is then unused: there is no
	// receiver to register a catalogue with, and no token to present, because
	// the queue is reached with the platform's own credentials.
	SQS *SQSConfig
	// SQSClient is the client SQS publishes with. Nil builds one from the
	// default credential chain.
	SQSClient SQSAPI
	// Sync makes Record wait, bounded by SyncTimeout, until the emitter has
	// delivered what it holds. It is for a runtime that freezes the process
	// between calls (Lambda), where an async queue in memory would be lost.
	// A record not delivered in time stays queued, and is retried if the
	// process runs again; Record still never fails its caller.
	Sync bool
	// SyncTimeout bounds that wait. Default 3s.
	SyncTimeout time.Duration
	// TokenFile is this workload's projected service-account token, presented
	// on every call. The installation knows the workload by it and stamps it
	// as the observer of every record.
	TokenFile string
	// Version and Instance identify this process on every record.
	Version  string
	Instance string
	Log      *slog.Logger
	// OnFatal is told of a refusal that no retry will change, found after the
	// start: an installation that was unreachable at start and, once reached,
	// refuses the catalogue. A refusal at start stops the start; this is what
	// stops the process when the same answer comes later, so that a service
	// whose records nobody accepts does not run for the life of the pod
	// keeping nothing. Nil leaves it running, saying so once at Error.
	OnFatal func(error)
}

// Connected reports whether the configuration names an installation.
func (c Config) Connected() bool { return c.Writer != "" || c.SQS != nil }

// Trail is the recorder: an emitter bound to the catalogue, delivering to the
// installation when one is configured.
type Trail struct {
	emitter   *emit.Emitter
	catalogue *catalogue.Catalogue
	log       *slog.Logger
	connected bool
	onFatal   func(error)
	sync      bool
	// degraded is set by a failed delivery and cleared once the queue is
	// empty again. While it is set a synchronous trail does not wait: an SQS
	// outage must cost a sign-in nothing, not syncWait on every one.
	degraded atomic.Bool
	syncWait time.Duration
	stop     context.CancelFunc
	done     sync.WaitGroup
}

// registerTimeout bounds each registration attempt, so that an installation
// that is slow to answer delays the start by seconds and not by minutes.
const registerTimeout = 10 * time.Second

// Open connects to the installation the configuration names, or opens a trail
// that only logs when it names none.
//
// A catalogue the installation refuses stops the start: the records it
// describes would be written against a description nobody accepted. An
// installation that cannot be reached does not: the records wait in the
// emitter's queue, in memory, and registration is tried again until it
// succeeds -- or is refused, which is Config.OnFatal's to act on.
func Open(ctx context.Context, cfg Config) (*Trail, error) {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	c, doc, err := Catalogue()
	if err != nil {
		return nil, err
	}
	t := &Trail{log: log, catalogue: c, connected: cfg.Connected(), onFatal: cfg.OnFatal,
		sync: cfg.Sync, syncWait: cfg.SyncTimeout}
	if t.syncWait <= 0 {
		t.syncWait = 3 * time.Second
	}

	hooks, err := emit.Instrument(emit.Hooks{
		OnRefused: func(r *record.Record, err error) {
			log.Error("an audit record does not satisfy the catalogue", "action", r.GetAction(), "error", logsafe.Error(err))
		},
		OnFailed: func(err error, delivery sink.Delivery, n int) {
			t.degraded.Store(true)
			if notTrusted(err) {
				// Retrying will not mend a token the writer does not trust.
				// Say what to look at, rather than "could not be reached"
				// until the queue gives up.
				log.Error("the audit installation does not trust this workload's token; records will not be kept until it does",
					"records", n, "token_file", cfg.TokenFile, "error", logsafe.Error(err))
				return
			}
			log.Warn("audit records could not be delivered yet", "records", n, "delivery", delivery.String(), "error", logsafe.Error(err))
		},
		// The queue gave one up. The Info line for this record was written
		// when it was made and reads as kept; this is the line that says it
		// was not, by id, so the two can be found from each other.
		OnDropped: func(r *record.Record, reason string) {
			log.Error("audit record dropped and is not in the trail",
				"audit.id", r.GetId(), "audit.action", r.GetAction(), "reason", reason)
		},
	}, otel.GetMeterProvider())
	if err != nil {
		return nil, err
	}
	options := emit.Options{
		Source: Source, Catalogue: c,
		Version: cfg.Version, Instance: cfg.Instance,
		Hooks: hooks,
	}

	if !t.connected {
		log.Warn("no audit installation is connected: records are validated and logged, and kept nowhere else",
			"audit", "log")
		options.Sink = sink.Discard
		if t.emitter, err = emit.New(options); err != nil {
			return nil, err
		}
		return t, nil
	}

	if cfg.SQS != nil {
		return t.openSQS(ctx, cfg, options)
	}

	if cfg.TokenFile == "" {
		return nil, errors.New(
			"audit: a token file is required when an installation is connected: " +
				"the writer records nothing from a caller it cannot name")
	}
	client := auth.TokenFile(cfg.TokenFile)

	options.Sink = sink.NewClient(client, cfg.Writer)
	if t.emitter, err = emit.New(options); err != nil {
		return nil, err
	}
	if err := emit.InstrumentQueue(t.emitter, otel.GetMeterProvider()); err != nil {
		_ = t.Close()
		return nil, err
	}

	// The same address the records go to: the receiver serves RegisterCatalogue
	// beside the sink, because an installation belongs to one application.
	registration := emit.Registration{
		URL: cfg.Writer, Source: c.Source, Version: c.Version,
		Document: doc.YAML, Schemas: doc.Schemas, HTTP: client,
	}
	err = register(ctx, registration)
	switch {
	case errors.Is(err, emit.ErrCatalogueRefused):
		_ = t.Close()
		return nil, err
	case notTrusted(err):
		log.Error("the audit installation does not trust this workload's token; registration is retried, and records will not be kept until it does",
			"writer", cfg.Writer, "token_file", cfg.TokenFile, "error", logsafe.Error(err))
		t.retryRegistration(ctx, registration)
	case err != nil:
		log.Warn("the audit installation could not be reached; records wait in the emitter's queue, and registration is retried",
			"writer", cfg.Writer, "error", logsafe.Error(err))
		t.retryRegistration(ctx, registration)
	default:
		log.Info("audit installation connected", "audit", "connected",
			"writer", cfg.Writer, "catalogue", c.Source+"@"+c.Version)
	}
	return t, nil
}

// openSQS opens a trail that publishes to a queue. There is no receiver, so
// the catalogue is not registered: the audit writer's Lambda carries the
// catalogue files in its own package, and registering would be a call to
// nothing. That is said once here, and is neither an error nor retried.
func (t *Trail) openSQS(ctx context.Context, cfg Config, options emit.Options) (*Trail, error) {
	api := cfg.SQSClient
	if api == nil {
		var err error
		if api, err = openSQSClient(ctx, *cfg.SQS); err != nil {
			return nil, err
		}
	}
	pub, err := newSQSPublisher(api, *cfg.SQS)
	if err != nil {
		return nil, err
	}
	options.Sink = pub
	options.Timeout = pub.timeout
	if t.sync {
		// Records are waited for one by one, so do not let the emitter sit on
		// one for its default second before the first attempt.
		options.Flush = 10 * time.Millisecond
	}
	if t.emitter, err = emit.New(options); err != nil {
		return nil, err
	}
	if err := emit.InstrumentQueue(t.emitter, otel.GetMeterProvider()); err != nil {
		_ = t.Close()
		return nil, err
	}
	t.log.Info("audit records are published to SQS; the catalogue is not registered, because it is delivered with the audit writer's package",
		"audit", "sqs", "queue", cfg.SQS.QueueURL, "catalogue", t.catalogue.Source+"@"+t.catalogue.Version, "sync", t.sync)
	return t, nil
}

func register(ctx context.Context, r emit.Registration) error {
	attempt, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()
	return emit.Register(attempt, r)
}

// notTrusted is the writer answering that it does not accept the token: a
// configuration to fix, not an outage to wait out.
func notTrusted(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return true
	}
	return false
}

func (t *Trail) retryRegistration(ctx context.Context, r emit.Registration) {
	retry, stop := context.WithCancel(context.WithoutCancel(ctx))
	t.stop = stop
	t.done.Add(1)
	go func() {
		defer t.done.Done()
		t.registerUntilDone(retry, r)
	}()
}

// registerUntilDone tries again, backing off to a minute, until the receiver
// answers — and stops trying if it answers with a refusal, which no retry
// will change, after saying so as loudly as the log can and handing it to
// OnFatal, which is what stops the process.
func (t *Trail) registerUntilDone(ctx context.Context, r emit.Registration) {
	wait := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		err := register(ctx, r)
		switch {
		case err == nil:
			t.log.Info("audit catalogue registered", "audit", "connected", "catalogue", r.Source+"@"+r.Version)
			return
		case errors.Is(err, emit.ErrCatalogueRefused):
			t.log.Error("the audit installation refused the catalogue; records will not be kept until it is fixed",
				"error", logsafe.Error(err))
			if t.onFatal != nil {
				t.onFatal(err)
			}
			return
		}
		wait = min(wait*2, time.Minute)
	}
}

// Connected reports whether records reach an installation.
func (t *Trail) Connected() bool { return t != nil && t.connected }

// Record implements [Recorder].
func (t *Trail) Record(ctx context.Context, r *record.Record) {
	if t == nil || r == nil {
		return
	}
	if err := t.emitter.Record(ctx, r); err != nil {
		t.line(ctx, r, err)
		return
	}
	t.line(ctx, r, nil)
	if t.sync {
		if t.degraded.Load() {
			if t.emitter.Pending() > 0 {
				return
			}
			t.degraded.Store(false)
		}
		wait, cancel := context.WithTimeout(context.WithoutCancel(ctx), t.syncWait)
		defer cancel()
		if err := t.Flush(wait); err != nil {
			t.log.WarnContext(ctx, "an audit record is still queued after the wait; it is retried while this process runs",
				"audit.id", r.GetId(), "audit.action", r.GetAction(), "error", logsafe.Error(err))
		}
	}
}

// Flush returns once the emitter holds no record waiting for delivery, or ctx
// is done. A Lambda handler calls it before returning, because the process is
// frozen afterwards and what is still in the in-memory queue may never be
// sent. It reports nothing when the trail keeps no queue.
func (t *Trail) Flush(ctx context.Context) error {
	if t == nil || t.emitter == nil {
		return nil
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for t.emitter.Pending() > 0 {
		select {
		case <-ctx.Done():
			return fmt.Errorf("audit: %d records not yet delivered: %w", t.emitter.Pending(), ctx.Err())
		case <-tick.C:
		}
	}
	return nil
}

// RecordDurable implements [Recorder]: it returns once the record is kept,
// or with the reason it could not be. That is the catalogue's `block`
// delivery, and only an action declared so is accepted here -- for an async
// action the emitter returns at enqueue, and a caller that waited for
// durability would have been told a lie.
//
// Without an installation there is nothing to wait for, and it answers nil:
// refusing a recovery sign-in on a deployment that chose to keep no trail
// would make recovery impossible by configuration.
func (t *Trail) RecordDurable(ctx context.Context, r *record.Record) error {
	if t == nil || r == nil {
		return nil
	}
	if a, ok := t.catalogue.Action(r.GetAction()); !ok || a.Delivery != "block" {
		return fmt.Errorf("audit: %s is not a block action, so nothing durable can be waited for", r.GetAction())
	}
	err := t.emitter.Record(ctx, r)
	t.line(ctx, r, err)
	if err != nil {
		return fmt.Errorf("audit: %s could not be kept: %w", r.GetAction(), err)
	}
	return nil
}

// line is the record as one log line: the same record, so that whoever reads
// the logs and whoever reads the trail can find one from the other by id.
func (t *Trail) line(ctx context.Context, r *record.Record, err error) {
	attrs := []any{
		"audit", true,
		"audit.id", r.GetId(),
		"audit.action", r.GetAction(),
		"audit.outcome", strings.ToLower(strings.TrimPrefix(r.GetOutcome().GetResult().String(), "RESULT_")),
	}
	if a := r.GetActor(); a != nil {
		attrs = append(attrs, "audit.actor.kind", a.GetKind(), "audit.actor.id", logsafe.Value(a.GetId()))
	}
	if s := r.GetSubject(); s != nil {
		attrs = append(attrs, "audit.subject.id", logsafe.Value(s.GetId()))
	}
	for i, target := range r.GetTargets() {
		attrs = append(attrs, fmt.Sprintf("audit.targets.%d", i), target.GetType()+":"+logsafe.Value(target.GetId()))
	}
	if reason := r.GetOutcome().GetReason(); reason != "" {
		attrs = append(attrs, "audit.reason", logsafe.Value(reason))
	}
	if err != nil {
		t.log.WarnContext(ctx, "audit record not kept", append(attrs, "error", logsafe.Error(err))...)
		return
	}
	t.log.InfoContext(ctx, "audit", attrs...)
}

// Close stops retrying registration and closes the emitter, which delivers
// what its queue holds within its timeout; what it cannot is dropped, and
// said to be. The queue is in memory: nothing survives the process.
func (t *Trail) Close() error {
	if t == nil {
		return nil
	}
	if t.stop != nil {
		t.stop()
	}
	t.done.Wait()
	if t.emitter != nil {
		return t.emitter.Close()
	}
	return nil
}

// schemaID reads a schema's $id, which is how the catalogue names it.
func schemaID(raw []byte) (string, error) {
	var head struct {
		ID string `json:"$id"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", err
	}
	if head.ID == "" {
		return "", errors.New("a schema without an $id")
	}
	return head.ID, nil
}
