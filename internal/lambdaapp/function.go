// Package lambdaapp runs sluis as ONE AWS Lambda function: one binary, one zip,
// one function (docs/reference/sluis/lambda.md). The function takes
//
//   - API Gateway HTTP API events: the issuer and the console, the same mux the
//     Kubernetes process serves on its listener;
//   - {"kind":"tick"|"run","controller":"github"|"slack","target":"<id>"}: one pass of one target under its
//     lease, by the controller the policy says the target belongs to (the
//     GitHub controller's organisations and `github:links`, the Slack
//     controller's workspaces). `tick` is what an EventBridge Scheduler
//     schedule sends, `run` what a console request sends (internal/port/invoke);
//   - {"kind":"cloudflare"}: the rotation of the Cloudflare credentials that are
//     due (the `cloudflare` section), which has no loop to run in on Lambda;
//   - {"kind":"clients"}: the reconcile of the generated client secrets, which
//     has no loop to run in on Lambda;
//   - {"kind":"refresh"}: the same reconcile and, where the directory is hosted
//     in the process, its refresh; the name before "clients", accepted for one
//     release;
//   - {"kind":"backup"}: the backup module's function (a document of apiVersion
//     sluis-backup/v1), which takes this event and `rpc` events and nothing else;
//   - {"kind":"restore"}: the same zip as the restore function (`backup.role:
//     restore`), which takes this event and `rpc` events and nothing else.
//
// This package assembles the function from the same pieces the Kubernetes
// process is assembled from (internal/rosterapp and the controllers' apps), and
// adds the two things Lambda needs: translating the platform's events, and
// reading secrets from SSM at cold start. The controllers named in the service
// document (`controllers`) do not run their loops here: each is assembled per
// invocation that needs it. It names no cluster, NATS or Valkey client, and
// cmd/sluis's lambda-build import guard and internal/boundaries keep it so.
package lambdaapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"sigs.k8s.io/yaml"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/telemetry"
)

// The environment of the function. Everything else is the configuration
// documents', OpenTelemetry's own OTEL_* variables, or the platform's:
// SLUIS_CONFIG (config.EnvConfig) names the service document, in the
// configuration layer mounted at /opt/sluis, and the secrets it names are read
// from SSM by the `secrets` source it declares.

// Function is the function, assembled at cold start and ready to be invoked.
type Function struct {
	Handler Handler
	// Flush sends what telemetry holds; the platform freezes the process when
	// an invocation returns, and a batch waiting for its timer waits for the
	// next invocation.
	Flush func(context.Context)
	// Close releases what the function holds, at shutdown.
	Close func()
}

// Module names: the role a function runs as. A zip is built for exactly one of
// them (cmd/sluis-<module>), and its document must be that module's.
const (
	ModuleIssuer     = "issuer"
	ModuleCloudflare = "cloudflare"
	ModuleBackup     = "backup"
	ModuleGitHub     = "github"
	ModuleSlack      = "slack"
	ModuleGoogle     = "google"
)

// documentModules maps the apiVersion of a module's own document to the module.
// The issuer and the Cloudflare module share `sluis/v3`, and the document's
// content tells them apart (see [ModuleOf]).
var documentModules = map[string]string{
	config.APIVersion("sluis-backup"):      ModuleBackup,
	config.APIVersion("controller-github"): ModuleGitHub,
	config.APIVersion("controller-slack"):  ModuleSlack,
	config.APIVersion("sluis-google"):      ModuleGoogle,
}

// apiVersionOf is the apiVersion of the document in file, or "" for a file
// that cannot be read or has none: the loader reports what is wrong with it.
func apiVersionOf(file string) string {
	raw, err := os.ReadFile(file) //nolint:gosec // the path is the process's own configuration
	if err != nil {
		return ""
	}
	var doc struct {
		APIVersion string `json:"apiVersion"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return doc.APIVersion
}

// OpenFunc assembles the function of one module from its document.
type OpenFunc func(ctx context.Context, file string) (*Function, error)

var (
	openersMu sync.Mutex
	openers   = map[string]OpenFunc{}
)

// Register links a module's function into the binary. Each module's package
// (internal/lambdaapp/issuerfn, backupfn, cloudflarefn) registers itself in its
// init, and a main imports the ones it carries: a zip holds the code of its
// module and of no other.
func Register(module string, open OpenFunc) {
	openersMu.Lock()
	defer openersMu.Unlock()
	openers[module] = open
}

// ModuleOf is the module a service document is for, by its apiVersion:
// controller-github/v2 is the GitHub module's, controller-slack/v2 the Slack
// module's, sluis-google/v1 the Google module's and sluis-backup/v1 the
// backup module's. A sluis/v3 document that sets cloudflare.serve is the
// Cloudflare module's; any other document is the issuer's.
func ModuleOf(file string) (string, error) {
	if m, ok := documentModules[apiVersionOf(file)]; ok {
		return m, nil
	}
	svc, err := config.Load[config.Sluis](file)
	if err != nil {
		return "", err
	}
	if svc.Cloudflare != nil && svc.Cloudflare.Serve != nil {
		return ModuleCloudflare, nil
	}
	return ModuleIssuer, nil
}

// Open assembles the function of whichever module the document is for, among
// the modules linked into the binary (the deprecated `sluis-lambda` zip links
// all of them).
func Open(ctx context.Context, getenv func(string) string) (*Function, error) {
	return OpenModule(ctx, getenv, "")
}

// OpenModule is [Open] for a zip built for module: a document that is another
// module's is refused before anything is assembled, so a zip cannot run as a
// module it was not built for. An empty module accepts any linked one.
func OpenModule(ctx context.Context, getenv func(string) string, module string) (*Function, error) {
	file := strings.TrimSpace(getenv(config.EnvConfig))
	if file == "" {
		return nil, fmt.Errorf("%s is unset: it names the service document, /opt/sluis/sluis.yaml in the configuration layer", config.EnvConfig)
	}
	// A retired variable that is still set is a deployment that believes it is
	// configuring something.
	if err := config.RefuseRetired("sluis", os.Environ()); err != nil {
		return nil, err
	}
	// Which module the function runs is the document's, and the zip's module
	// must be the same.
	got, err := ModuleOf(file)
	if err != nil {
		return nil, err
	}
	if module != "" && got != module {
		return nil, fmt.Errorf("this is the sluis-%s function and %s is a %s document: a zip runs the module it was built for and no other", module, file, got)
	}
	key := got
	if module != "" {
		key = module
	}
	openersMu.Lock()
	open := openers[key]
	openersMu.Unlock()
	if open == nil {
		return nil, fmt.Errorf("%s is a %s document and this binary does not carry the %s module", file, got, key)
	}
	return open(ctx, file)
}

// SEAM (adapters): the `ssm` secrets, `kms` signing and `sqs` audit adapters
// register themselves in their packages' init; this package imports them (blank,
// beside the imports above) once they land, so that the aws-serverless preset
// resolves in the Lambda binary.

// Logger is the JSON logger a function writes to stdout, which CloudWatch Logs
// (and the OTLP layer's log pipeline) collects.
func Logger(ctx context.Context, service string, level slog.Level) (*slog.Logger, func(context.Context), error) {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	// The shutdown is not kept: a function is never shut down in order, the
	// platform freezes and ends it, so what matters is the flush. A failed
	// export is dropped, not retried: see [flusher].
	if _, err := telemetry.Start(ctx, service, log, telemetry.WithoutRetry()); err != nil {
		return nil, nil, err
	}
	f := &flusher{flush: forceFlush}
	return log, f.Flush, nil
}

// forceFlush sends the batches the global providers hold: the tracer
// provider's always, the meter provider's when metrics is set.
func forceFlush(ctx context.Context, metrics bool) error {
	type flusher interface{ ForceFlush(context.Context) error }
	var errs []error
	if p, ok := any(otel.GetMeterProvider()).(flusher); ok && metrics {
		errs = append(errs, p.ForceFlush(ctx))
	}
	if p, ok := any(otel.GetTracerProvider()).(flusher); ok {
		errs = append(errs, p.ForceFlush(ctx))
	}
	return errors.Join(errs...)
}

const (
	// flushBudget bounds the telemetry flush before an invocation returns. The
	// collector is the telemetry layer's extension on localhost, which
	// forwards each export as it comes.
	flushBudget = time.Second
	// flushBackoff is how long after a failed flush the next is skipped, and
	// doubles with each failure in a row up to flushBackoffMax.
	flushBackoff    = 5 * time.Second
	flushBackoffMax = 5 * time.Minute
	// metricsInterval is the least time between two metric flushes of one
	// instance, counted from the last successful one.
	metricsInterval = time.Minute
)

// flusher is the flush before an invocation returns, which the response waits
// for. When the collector cannot take an export (its extension has no token
// because the issuer it gets one from is overloaded, which may be this very
// function) a flush fails or runs out its budget. The next ones are then
// skipped for a while, longer for each failure in a row: what the providers
// hold goes out with a later flush or their own timers, or is dropped when
// their queues fill. Telemetry never makes a caller wait more than
// flushBudget, and only once per backoff.
//
// Traces are flushed on every invocation. Metrics are flushed on an instance's
// first invocation and then only when metricsInterval has passed since its
// last successful metric flush: the meters use cumulative temporality, so a
// skipped flush loses nothing, because the next one carries the totals, and
// re-sending them on every invocation was most of the function's telemetry
// egress. The only loss is the last metricsInterval before the platform
// retires an instance.
type flusher struct {
	flush func(ctx context.Context, metrics bool) error
	// now is the clock; nil is time.Now.
	now func() time.Time

	mu       sync.Mutex
	failures int
	skipTill time.Time
	// lastMetrics is when metrics were last flushed successfully; zero before
	// the first.
	lastMetrics time.Time
}

func (f *flusher) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

// Flush flushes within flushBudget, unless a recent flush failed: traces
// always, metrics once per metricsInterval.
func (f *flusher) Flush(ctx context.Context) {
	f.mu.Lock()
	now := f.clock()
	if now.Before(f.skipTill) {
		f.mu.Unlock()
		return
	}
	metrics := f.lastMetrics.IsZero() || now.Sub(f.lastMetrics) >= metricsInterval
	f.mu.Unlock()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushBudget)
	err := f.flush(fctx, metrics)
	cancel()
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		f.failures, f.skipTill = 0, time.Time{}
		if metrics {
			f.lastMetrics = f.clock()
		}
		return
	}
	wait := flushBackoffMax
	if f.failures < 7 {
		wait = min(flushBackoffMax, flushBackoff<<f.failures)
	}
	f.failures++
	f.skipTill = f.clock().Add(wait)
}

// CheckFunc is the {"kind":"check"} event's body: the declared secrets of the
// document in file (and the policy it names), read from SSM. only keeps the
// kinds the function's module reads; none keeps them all. The document is read
// again when asked, which is once per deploy.
func CheckFunc(file string, only ...string) func(context.Context) (deploycheck.Report, error) {
	return func(ctx context.Context) (deploycheck.Report, error) {
		c, err := config.LoadConfig[config.Sluis](file, nil)
		if err != nil {
			return deploycheck.Report{}, err
		}
		return deploycheck.Check(ctx, &c.Service.Serve, c.Policy, only...)
	}
}

// CheckAdapters is a check that first builds the adapters the function uses
// (verify) and then answers check. The secrets check proves the secrets are
// there; it never builds the adapters, so a document that lacks a setting an
// adapter needs passes it and fails the function's first tick. verify's refusal
// is the check's, with the setting it names.
func CheckAdapters(verify func(context.Context) error,
	check func(context.Context) (deploycheck.Report, error)) func(context.Context) (deploycheck.Report, error) {
	return func(ctx context.Context) (deploycheck.Report, error) {
		if err := verify(ctx); err != nil {
			return deploycheck.Report{}, fmt.Errorf("adapters: %w", err)
		}
		return check(ctx)
	}
}

// CheckOwn is the {"kind":"check"} answer of a function whose module declares
// the given kinds of secret (none, for the GitHub and Slack controllers: an
// App's credential is made at run time and no document names it). It holds the
// document's `secrets` section to the same rules as every function's, and reads
// only what the module itself reads, never another module's paths: the policy's
// client secrets are the issuer's, and a controller must not report them.
func CheckOwn(s *config.Secrets, pol *config.PolicyDocument, kinds ...string) func(context.Context) (deploycheck.Report, error) {
	return func(ctx context.Context) (deploycheck.Report, error) {
		return deploycheck.CheckDeclared(ctx, s, deploycheck.Own(config.DeclaredSecrets(nil, pol), kinds...))
	}
}
