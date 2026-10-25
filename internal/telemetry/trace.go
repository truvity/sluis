package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// The span attribute names this repository sets itself.
const (
	// AttrTargetKind is what a reconciler's tick is over: `github-tick`,
	// `slack-tick`, `github-links`.
	AttrTargetKind = "access_roster.target.kind"
	// AttrTargetID is the target's own name: a GitHub organisation or a Slack
	// workspace the policy declares. The one identifier a span may carry, and
	// never a person's.
	AttrTargetID = "access_roster.target.id"
	// AttrOutcome is how a tick ended: ok or failed.
	AttrOutcome = "access_roster.outcome"
	// AttrPort and AttrOperation name a storage port and the call on it. The
	// KEY of the call is never an attribute: it names a person.
	AttrPort      = "access_roster.port"
	AttrOperation = "access_roster.operation"
	// AttrResendCount is OpenTelemetry's name for which attempt of a request a
	// span is: 0 the first, 1 the first retry.
	AttrResendCount = "http.request.resend_count"
	// AttrCloudflarePages and AttrCloudflareListed are how many pages a
	// Cloudflare listing read and how many entries it returned: counts, never
	// a name or an id.
	AttrCloudflarePages  = "sluis.cloudflare.pages"
	AttrCloudflareListed = "sluis.cloudflare.listed"
)

// SpanAttributeAllowlist is every attribute a span leaving this process may
// carry. The first group is ours: what a tick is over, how it ended, which
// port operation. The second is the shape of an RPC or an HTTP request, which
// names a method, a route and a status and nothing about the caller.
//
// Everything else is dropped on the way out by [FilterExporter], whoever set it:
// the HTTP instrumentation records the client's address, the user agent and
// the raw URL path, the RPC instrumentation records the peer, and a recorded
// error carries a message that may quote an address or a group. Spans are read
// by everyone with any grant on the trace store, so the rule that they hold no
// personal data (docs/decisions/0026-0032, docs/concepts/sluis/ports.md) has to be a
// property of the exporter and not a promise by each caller. Never an email,
// a subject, a group name, a token or its hash.
var SpanAttributeAllowlist = map[attribute.Key]bool{
	AttrTargetKind: true, AttrTargetID: true, AttrOutcome: true, AttrPort: true, AttrOperation: true,

	"rpc.system.name": true, "rpc.method": true, "rpc.response.status_code": true, "error.type": true,
	"http.request.method": true, "http.response.status_code": true, "http.route": true,

	// A function invocation (the Lambda request id), and the AWS SDK client
	// spans (storage/awsotel): the service and operation called, the status and
	// the AWS request id. None names a caller or a stored key.
	"faas.invocation_id": true, "rpc.system": true, "rpc.service": true,
	"aws.request_id": true, "http.status_code": true,

	// A call to Cloudflare's API (internal/cloudflare/cfapi): which attempt,
	// and a listing's size. Never the URL, which names a token's id.
	AttrResendCount: true, AttrCloudflarePages: true, AttrCloudflareListed: true,
}

// FilterExporter wraps an exporter so that only [SpanAttributeAllowlist]
// leaves the process. Span events (a recorded error's message is one), links'
// attributes and a status description are removed outright: they are free text.
func FilterExporter(next sdktrace.SpanExporter) sdktrace.SpanExporter {
	return filtering{next}
}

type filtering struct{ next sdktrace.SpanExporter }

func (f filtering) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	stubs := make(tracetest.SpanStubs, 0, len(spans))
	for _, s := range spans {
		stub := tracetest.SpanStubFromReadOnlySpan(s)
		stub.Attributes = keep(stub.Attributes)
		stub.Events = nil
		stub.Status.Description = ""
		for i := range stub.Links {
			stub.Links[i].Attributes = nil
		}
		stubs = append(stubs, stub)
	}
	return f.next.ExportSpans(ctx, stubs.Snapshots())
}

func (f filtering) Shutdown(ctx context.Context) error { return f.next.Shutdown(ctx) }

func keep(in []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(in))
	for _, kv := range in {
		if SpanAttributeAllowlist[kv.Key] {
			out = append(out, kv)
		}
	}
	return out
}

// TracerName is the instrumentation scope of the spans this repository starts
// itself.
const TracerName = "github.com/truvity/sluis"

// Tracer is the repository's tracer, on whatever provider is global: a no-op
// until [Start] installs one.
func Tracer() trace.Tracer { return otel.Tracer(TracerName) }

// ConnectServerInterceptor instruments a Connect server: one server span per
// call, continuing the caller's trace. Its metrics are off: the HTTP handler's
// route metrics carry the request rate and latency under this service's own
// names. Pass it to connect.NewServer.
func ConnectServerInterceptor() connect.ServerInterceptor {
	i, err := otelconnect.NewServerInterceptor(connectInterceptorOptions()...)
	if err != nil {
		// NewServerInterceptor fails only on a bad option, and these are
		// constants.
		panic("telemetry: " + err.Error())
	}
	return i
}

// ConnectClientInterceptor is the same for a client: one client span per
// call, and the traceparent carried to the server. Pass it to
// connect.NewClient.
func ConnectClientInterceptor() connect.ClientInterceptor {
	i, err := otelconnect.NewClientInterceptor(connectInterceptorOptions()...)
	if err != nil {
		panic("telemetry: " + err.Error())
	}
	return i
}

func connectInterceptorOptions() []otelconnect.Option {
	return []otelconnect.Option{otelconnect.WithoutMetrics(), otelconnect.WithTrustRemote()}
}

// HTTPHandler wraps a server's handler in a server span, continuing the
// caller's traceparent, and records the request count and duration per route.
//
// route turns a request into one of a SMALL FIXED SET of names: it is the
// span's name, `http.route` and the metrics' `route` label, and a raw path must
// never reach any of them (a path carries callback codes, state and, on a
// console, names). The set is the caller's to keep bounded.
//
// A handler that learns more than the path told route can rename the request
// with [SetRoute]: the span and the metrics then carry that name instead.
func HTTPHandler(next http.Handler, service string, route func(*http.Request) string) http.Handler {
	instruments := newHTTPInstruments()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := route(r)
		span := trace.SpanFromContext(r.Context())
		span.SetAttributes(attribute.String("http.route", name))
		span.SetAttributes(InvocationAttrs(r.Context())...)
		holder := &routeHolder{name: name}
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, holder))
		recorder := &statusRecorder{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(recorder, r)
		if renamed := holder.get(); renamed != name {
			name = renamed
			span.SetAttributes(attribute.String("http.route", name))
			span.SetName(r.Method + " " + name)
		}
		instruments.record(r.Context(), name, recorder.status(), time.Since(started))
	})
	return otelhttp.NewHandler(inner, service,
		// Spans and nothing else: the instruments above are this service's own.
		otelhttp.WithMeterProvider(noop.NewMeterProvider()),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + route(r) }),
	)
}

// routeKey carries a request's [routeHolder] from [HTTPHandler] to the handler.
type routeKey struct{}

// routeHolder is the route a request is recorded under, which [SetRoute] may
// change while the handler runs.
type routeHolder struct {
	mu   sync.Mutex
	name string
}

func (h *routeHolder) get() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.name
}

// SetRoute renames the route of the request whose context ctx is, for its span
// and its request metrics, once a handler knows more than the path said: the
// token endpoint names a Cloudflare mint `token_mint` once it has read the
// audience. name must be one of the same small fixed set the route function
// keeps, never anything a request carries. Outside [HTTPHandler] it does nothing.
func SetRoute(ctx context.Context, name string) {
	if h, ok := ctx.Value(routeKey{}).(*routeHolder); ok {
		h.mu.Lock()
		h.name = name
		h.mu.Unlock()
	}
}

const httpMeterName = "github.com/truvity/sluis/http"

type httpInstruments struct {
	requests Int64Counter
	duration Float64Histogram
}

func newHTTPInstruments() httpInstruments {
	meter := otel.Meter(httpMeterName)
	// Instrument creation fails only on an invalid name, which these are not;
	// a failed one is a no-op instrument, never a stopped server.
	requests := NewInt64Counter(meter, "access_issuer.http.requests",
		metric.WithDescription("HTTP requests the issuer's listener answered, by route (a fixed set of names, never the raw path) and status class."))
	duration := NewFloat64Histogram(meter, "access_issuer.http.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Seconds from a request's arrival to the handler returning, by route and status class."),
		// 30 and 60 for a Cloudflare mint (route token_mint), which waits on
		// another function and on Cloudflare: a quantile cannot read above the
		// last finite bound, so with 10 the last, a mint rule over 10s could never fire.
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60))
	return httpInstruments{requests, duration}
}

func (m httpInstruments) record(ctx context.Context, route string, status int, took time.Duration) {
	at := metric.WithAttributes(attribute.String("route", route), attribute.String("status_class", strconv.Itoa(status/100)+"xx"))
	m.requests.Add(ctx, 1, at)
	m.duration.Record(ctx, took.Seconds(), at)
}

// statusRecorder remembers the status a handler wrote. Unwrap lets
// http.ResponseController reach the underlying writer's Flush and Hijack.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.code == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusRecorder) Flush() {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	http.NewResponseController(s.ResponseWriter).Flush() //nolint:errcheck // a writer that cannot flush is not an error here
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// status is what was written; a handler that wrote nothing answered 200.
func (s *statusRecorder) status() int {
	if s.code == 0 {
		return http.StatusOK
	}
	return s.code
}

// The wire propagator: how a trace crosses a boundary that is not an HTTP header: a module
// call whose envelope carries the W3C `traceparent` and `tracestate` fields.
// It uses the W3C propagator directly, not the global one, so a process that
// installed none still continues a trace it was handed.
var wire = propagation.TraceContext{}

// InjectWire returns the `traceparent` and `tracestate` of the span in ctx, both
// empty when ctx holds no valid span.
func InjectWire(ctx context.Context) (traceparent, tracestate string) {
	carrier := propagation.MapCarrier{}
	wire.Inject(ctx, carrier)
	return carrier.Get("traceparent"), carrier.Get("tracestate")
}

// ExtractWire is ctx continuing the trace the two fields name. A malformed or
// empty value leaves ctx as it was, so a bad header starts a new trace instead
// of failing the call.
func ExtractWire(ctx context.Context, traceparent, tracestate string) context.Context {
	if traceparent == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": traceparent}
	if tracestate != "" {
		carrier["tracestate"] = tracestate
	}
	return wire.Extract(ctx, carrier)
}

// AttrFaaSInvocationID is the OpenTelemetry semantic-convention name of a
// function invocation's id: on Lambda, the request id.
const AttrFaaSInvocationID = "faas.invocation_id"

type invocationKey struct{}

// WithInvocationID marks ctx with the platform's id of this function
// invocation (the Lambda request id), so the server span that is started below
// it can carry [AttrFaaSInvocationID]. An empty id leaves ctx as it was.
func WithInvocationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, invocationKey{}, id)
}

// InvocationAttrs is the span attributes the invocation in ctx adds: the
// invocation id, or nothing outside a function.
func InvocationAttrs(ctx context.Context) []attribute.KeyValue {
	if id, ok := ctx.Value(invocationKey{}).(string); ok {
		return []attribute.KeyValue{attribute.String(AttrFaaSInvocationID, id)}
	}
	return nil
}
