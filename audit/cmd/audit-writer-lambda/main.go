// Command audit-writer-lambda is the write path as an AWS Lambda function behind
// an SQS event source mapping.
//
// It is the same writer as audit-writer's: the package writer, opened over the
// same archive with the same profiles under the same `require: archived` guard,
// and a record is acknowledged only once it is in the bucket. What differs is
// everything around it, and all of it follows from there being no process that
// stays up:
//
//   - the input is the SQS event, with partial batch responses
//     (ReportBatchItemFailures), so a message that failed goes back to the queue
//     and the ones that did not are deleted by the platform;
//   - there is no database, so the deduplication table is DynamoDB
//     (dedupe/dynamodbdedupe), shared by every concurrent invocation environment;
//   - there is no listener, no registry, no stream: the catalogues the writer
//     runs with are files in the package;
//   - configuration is a file in the package (docs/decisions/0063): the Pulumi
//     library renders it from the stack's own outputs and ships it in the zip, so
//     there is nothing to fetch at cold start and nothing in the environment
//     that is a secret;
//   - telemetry is OTEL_* and nothing else, sent to the extension's loopback
//     proxy (docs/concepts/audit/aws-lambda.md), and flushed at the end of every
//     invocation because an environment is frozen between them, and again on
//     SIGTERM.
//
// This is not deployed by this repository; deploy/pulumi is the library that
// builds it into a stack.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/truvity/sluis/storage/awsotel"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"go.opentelemetry.io/otel"

	"github.com/truvity/sluis/audit/dedupe/dynamodbdedupe"
	"github.com/truvity/sluis/audit/internal/buildinfo"
	"github.com/truvity/sluis/audit/internal/cli"
	"github.com/truvity/sluis/audit/internal/config"
	"github.com/truvity/sluis/audit/internal/sqsorigin"
	"github.com/truvity/sluis/audit/internal/telemetry"
	"github.com/truvity/sluis/audit/profile"
	"github.com/truvity/sluis/audit/sdk/catalogue"
	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/audit/sdk/sink"
	"github.com/truvity/sluis/audit/writer"
)

// The configuration file is where the layer of the Pulumi library mounts it,
// /opt/audit/audit.yaml, unless --config or AUDIT_CONFIG names another. The old
// place, the function package's own root, is still tried when the layer's is
// absent, so that a deployment built by the previous release keeps starting;
// that fallback goes in the release after this one.
const (
	defaultConfig = "/opt/audit/audit.yaml"
	legacyConfig  = "/var/task/audit.yaml"
)

func main() {
	if err := run(); err != nil {
		slog.ErrorContext(context.Background(), "audit-writer-lambda", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	// Declared so that the flag package accepts it; config.Path reads it, with AUDIT_CONFIG.
	flag.String("config", "", "the configuration file, or AUDIT_CONFIG, or "+defaultConfig+": the one thing that configures this process")
	showVersion := flag.Bool("version", false, "print this build's version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("audit-writer-lambda", buildinfo.Version)
		return nil
	}
	// Everything until the runtime polls for work is the platform's init, which
	// has a limit of its own (10 s). Each step says how long it took, so that a
	// slow start names its step.
	started := time.Now()
	configFile, err := config.Path(os.Args[1:], "audit-writer-lambda", defaultConfig, legacyConfig)
	if err != nil {
		return err
	}
	cfg, err := config.LoadWriterLambda(configFile)
	if err != nil {
		return err
	}
	started = initPhase("config_load", started)

	// SIGTERM is the platform's shutdown, sent when an extension is registered
	// (the telemetry layer is one) and the environment is idle: the one chance to
	// write what is held and export what is buffered.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	frameworks, err := profile.Builtin()
	if err != nil {
		return err
	}
	d, err := cli.LoadDeployment(cfg.Deployment)
	if err != nil {
		return err
	}
	profiles, err := d.Compose(frameworks)
	if err != nil {
		return err
	}
	started = initPhase("profiles", started, slog.Int("profiles", len(profiles)))
	// Each preset is its own store. A profile whose preset is not configured, or
	// whose frameworks demand a lock its preset's bucket is not written with, is
	// refused before a copy lands where it could be deleted (ADR 0056).
	archive, err := cli.OpenArchive(ctx, d, profiles, cfg.Archive, cfg.SecretReader())
	if err != nil {
		return err
	}
	started = initPhase("s3_clients", started, slog.Int("presets", len(d.Presets)),
		slog.Float64("attempt_timeout_ms", float64(cfg.Archive.AttemptTimeout.D().Milliseconds())), slog.Int("max_attempts", cfg.Archive.MaxAttempts))
	provider, err := cli.OpenKeysFrom(ctx, cfg.Keys, cfg.SecretReader())
	if err != nil {
		return err
	}
	if provider != nil {
		defer provider.Close() //nolint:errcheck // shutting down
	}
	var found []*catalogue.Catalogue
	if cfg.Catalogues != "" {
		if found, err = loadCatalogues(cfg.Catalogues); err != nil {
			return err
		}
	}
	started = initPhase("catalogues_load", started, slog.Int("catalogues", len(found)))

	table := cfg.Dedupe.DynamoDB
	var opts []func(*awsconfig.LoadOptions) error
	if table.Region != "" {
		opts = append(opts, awsconfig.WithRegion(table.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return fmt.Errorf("loading the AWS configuration for DynamoDB: %w", err)
	}
	window := table.Window.D()
	if window == 0 {
		window = widestDedupeWindow(profiles)
	}
	ddbClient := dynamodb.NewFromConfig(awsCfg, func(o *dynamodb.Options) { o.TracerProvider, o.MeterProvider = awsotel.Providers() })
	seen, err := dynamodbdedupe.New(ddbClient, table.Table, window)
	if err != nil {
		return err
	}
	started = initPhase("dedupe_client", started)

	stopTelemetry, err := telemetry.Start(ctx, "audit-writer", buildinfo.Version, slog.Default())
	if err != nil {
		return err
	}
	queue, err := telemetry.NewQueue(otel.GetMeterProvider())
	if err != nil {
		return err
	}
	started = initPhase("otel_start", started)

	evidence, err := cli.WriterEvidence(cfg.Source, cfg.Deployment, "", cfg.Catalogues)
	if err != nil {
		return err
	}
	w, err := writer.Open(ctx, writer.Config{
		Archive:          archive,
		Profiles:         profiles,
		Keys:             provider,
		Catalogues:       found,
		Dedupe:           seen,
		ForgetIdentities: cfg.ForgetIdentities,
		Version:          buildinfo.Version,
		Evidence:         evidence,
		// Concurrent invocations are replicas: each environment is its own
		// writer sharing one deduplication table. Two is "more than one" to the
		// guards that care.
		Replicas: 2,
		// What is on the queue is what a sender chose. SQS delivers the body its
		// sender wrote, so a stamp inside it (observer, recorded_at,
		// origin hash) is a claim anyone allowed to send could make about
		// anyone. FromStream stays false: the handler replaces the stamp of
		// every record from what SQS itself sets (see originFor), and only a
		// batch it has stamped is kept as stamped.
	})
	if err != nil {
		return err
	}
	// writer.Open logs its own steps (holds, ensure_catalogues,
	// record_compositions); this is their sum with the evidence digests.
	initPhase("writer_open", started)
	front, err := guard(w, cfg.Require)
	if err != nil {
		return err
	}

	h := handlerFor(front, queue, originFor(cfg.Senders, buildinfo.Version))
	h.before = func(ctx context.Context) {
		if err := w.RefreshHolds(ctx); err != nil {
			// The last answer stands, as it does for the background refresh.
			slog.ErrorContext(ctx, "could not refresh the legal holds; keeping the last answer", slog.Any("error", err))
		}
	}
	h.after = func(ctx context.Context) {
		flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := telemetry.Flush(flush); err != nil {
			slog.WarnContext(ctx, "could not export telemetry at the end of the invocation", slog.Any("error", err))
		}
	}

	go func() {
		<-ctx.Done()
		shutting, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := w.Close(shutting); err != nil {
			slog.ErrorContext(context.Background(), "flushing on shutdown", slog.Any("error", err))
		}
		_ = stopTelemetry(shutting)
		// The runtime library has no way to be told to return, and the platform
		// is about to end the environment either way.
		os.Exit(0)
	}()

	slog.InfoContext(context.Background(), "audit-writer-lambda", slog.Int("presets", len(d.Presets)), slog.Int("profiles", len(profiles)),
		slog.String("table", table.Table), slog.String("window", window.String()))
	lambda.StartWithOptions(h.Handle, lambda.WithContext(ctx))
	return nil
}

// initPhase logs how long one step of the function's init took, and returns
// the time the next step starts.
func initPhase(name string, started time.Time, attrs ...any) time.Time {
	now := time.Now()
	args := append([]any{slog.String("phase", name), slog.Float64("duration_ms", float64(now.Sub(started).Microseconds())/1000)}, attrs...)
	slog.InfoContext(context.Background(), "audit-writer-lambda init phase", args...)
	return now
}

// handlerFor is the handler of the function: the writer behind its guard, the
// queue's age metric, and the origin every record is stamped from.
func handlerFor(target sink.Sink, age queueMetrics, origin *sqsorigin.Origin) *handler {
	return &handler{target: target, age: age, now: time.Now, log: slog.Default(), origin: origin}
}

// originFor is what stamps a record that came off the queue: the observer its
// SenderId maps to in `senders`, the time SQS took the message, and this
// function's version and instance. The sender's own claims are never read.
func originFor(senders map[string]string, version string) *sqsorigin.Origin {
	return &sqsorigin.Origin{Senders: senders, Version: version, Instance: record.InstanceName()}
}

// guard holds the writer to `require`: it refuses at start-up when the chain can
// never give it, and fails any write acknowledged below it.
func guard(front sink.Sink, require string) (sink.Sink, error) {
	least, err := sink.ParseDurability(require)
	if err != nil {
		return nil, fmt.Errorf("require: %w", err)
	}
	guarded, err := sink.Guard(front, least)
	if err != nil {
		return nil, fmt.Errorf("refusing to start with require: %s: %w", require, err)
	}
	return guarded, nil
}

// widestDedupeWindow is how long a written identifier is remembered when the
// file does not say: the widest window any profile's framework profiles ask for, since one
// table serves them all.
func widestDedupeWindow(profiles map[string]*profile.Profile) time.Duration {
	var longest time.Duration
	for _, p := range profiles {
		if d := time.Duration(p.Pipeline.DedupeWindowDays) * 24 * time.Hour; d > longest {
			longest = d
		}
	}
	return longest
}

// loadCatalogues reads every catalogue in a directory.
func loadCatalogues(dir string) ([]*catalogue.Catalogue, error) {
	paths, err := cli.FindCatalogues(dir)
	if err != nil {
		return nil, err
	}
	out := make([]*catalogue.Catalogue, 0, len(paths))
	for _, path := range paths {
		c, err := catalogue.LoadFS(os.DirFS(filepath.Dir(path)), filepath.Base(path))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("catalogues names " + dir + ", which holds no catalogue")
	}
	return out, nil
}
