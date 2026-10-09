package lambdaapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-lambda-go/events"
	"github.com/truvity/sluis/storage/logattr"
)

// maxResponse is what a function may return synchronously, 6 MB, counted after
// base64 has inflated a binary body. A response over it is a 502 from the
// platform with no explanation, so it is refused here, naming the cause.
const maxResponse = 6 << 20

// maxRequest is the request body a handler is given: API Gateway's own limit
// is 10 MB, Lambda's is 6 MB, and the console takes nothing near either.
const maxRequest = 6 << 20

// HTTP serves API Gateway HTTP API events (payload format 2.0) with an
// [http.Handler]: the same mux the Kubernetes server serves on its listener.
type HTTP struct {
	handler http.Handler
	// settle is called when a request is over, for the work it left running
	// after its response: a frozen process would never finish it.
	settle func()
	// refresh runs one pass of the directory refresh (the {"kind":"refresh"}
	// event); nil when this function has no directory.
	refresh func(context.Context) (RefreshResult, error)
	// cloudflare runs one pass over the Cloudflare presets (the
	// {"kind":"cloudflare"} event); nil when the function has no such section.
	// It returns the number of presets that failed and a one-line summary.
	cloudflare func(context.Context) (failed int, summary string, err error)
	// controllers run a controller pass per {"kind":"tick"|"run"} event, by the
	// kind of the event's target; kindOf says which kind a target is.
	controllers map[string]*Controller
	kindOf      func(target string) string
	log         *slog.Logger
}

// WithControllers makes the function answer {"kind":"tick"|"run","target":...}
// events by running one pass of the target under the controller of its kind
// (the controllers' names are the keys: "github", "slack"). kindOf says which
// kind a target is, from the policy that declares it; an empty answer is a
// target nobody declares.
func (h *HTTP) WithControllers(kindOf func(target string) string, controllers map[string]*Controller) *HTTP {
	h.controllers, h.kindOf = controllers, kindOf
	return h
}

// WithRefresh makes the function answer {"kind":"refresh"} events with run.
func (h *HTTP) WithRefresh(run func(context.Context) (RefreshResult, error)) *HTTP {
	h.refresh = run
	return h
}

// WithCloudflare makes the function answer {"kind":"cloudflare"} events with run.
func (h *HTTP) WithCloudflare(run func(context.Context) (failed int, summary string, err error)) *HTTP {
	h.cloudflare = run
	return h
}

// NewHTTP adapts handler. settle may be nil.
func NewHTTP(handler http.Handler, settle func(), log *slog.Logger) *HTTP {
	if log == nil {
		log = slog.Default()
	}
	return &HTTP{handler: handler, settle: settle, log: log}
}

// Handle implements [Handler]: payload is an API Gateway HTTP API event.
func (h *HTTP) Handle(ctx context.Context, payload json.RawMessage) (any, error) {
	// An API Gateway event has no top-level `kind`; a scheduled one does.
	var peek struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(payload, &peek); err == nil && peek.Kind != "" {
		if peek.Kind == KindTick || peek.Kind == KindRun {
			return h.controller(ctx, payload)
		}
		return h.scheduled(ctx, peek.Kind)
	}
	var event events.APIGatewayV2HTTPRequest
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("the event is not an API Gateway HTTP API event: %w", err)
	}
	if event.Version != "2.0" {
		return nil, fmt.Errorf("the event has version %q: this function takes API Gateway HTTP API payload format 2.0 "+
			"(a REST API or an ALB sends another shape)", event.Version)
	}
	return h.Serve(ctx, event), nil
}

// Serve runs one request through the handler and returns its response. A
// request that cannot be made into an [http.Request] is a 400, and a handler
// that panics a 500: neither is an error of the invocation, which the platform
// would retry.
func (h *HTTP) Serve(ctx context.Context, event events.APIGatewayV2HTTPRequest) (resp events.APIGatewayV2HTTPResponse) {
	req, err := Request(ctx, event)
	if err != nil {
		h.log.WarnContext(ctx, "a request could not be read", logattr.SafeError("error", err))
		return text(http.StatusBadRequest, "bad request\n")
	}
	rec := &recorder{header: http.Header{}}
	func() {
		defer func() {
			if p := recover(); p != nil {
				h.log.ErrorContext(ctx, "a handler panicked", logattr.SafeString("panic", fmt.Sprint(p)), logattr.SafeString("path", req.URL.Path))
				resp = text(http.StatusInternalServerError, "internal error\n")
				rec = nil
			}
		}()
		h.handler.ServeHTTP(rec, req)
	}()
	if h.settle != nil {
		h.settle()
	}
	if rec == nil {
		return resp
	}
	resp = Response(rec.status(), rec.header, rec.body.Bytes())
	if size := len(resp.Body); size > maxResponse {
		h.log.ErrorContext(ctx, "a response is over what a function may return", logattr.SafeString("path", req.URL.Path), slog.Int("bytes", size))
		return text(http.StatusBadGateway, "response too large\n")
	}
	return resp
}

// Request makes the [http.Request] an event describes. The headers arrive
// lower-cased and the cookies apart from them, and the body is base64 when the
// gateway says so.
func Request(ctx context.Context, event events.APIGatewayV2HTTPRequest) (*http.Request, error) {
	body := []byte(event.Body)
	if event.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(event.Body)
		if err != nil {
			return nil, fmt.Errorf("the body is not base64: %w", err)
		}
		body = decoded
	}
	if len(body) > maxRequest {
		return nil, fmt.Errorf("the body is %d bytes, over %d", len(body), maxRequest)
	}
	host := event.Headers["host"]
	if host == "" {
		host = event.RequestContext.DomainName
	}
	scheme := "https"
	if proto := event.Headers["x-forwarded-proto"]; proto == "http" {
		scheme = "http"
	}
	target := scheme + "://" + host + event.RawPath
	if event.RawQueryString != "" {
		target += "?" + event.RawQueryString
	}
	method := event.RequestContext.HTTP.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("the request line: %w", err)
	}
	for name, value := range event.Headers {
		// A header with several values arrives comma-joined, which is also how
		// a client may have sent it: one value is what is known.
		req.Header.Set(name, value)
	}
	if len(event.Cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(event.Cookies, "; "))
	}
	req.Host = host
	req.RemoteAddr = event.RequestContext.HTTP.SourceIP
	req.ContentLength = int64(len(body))
	return req, nil
}

// Response makes an event's response of what a handler wrote. Set-Cookie goes
// in the response's cookies, as the gateway requires (a header of that name is
// dropped), the other headers keep their values comma-joined, and the body is
// base64 when it is not text.
func Response(status int, header http.Header, body []byte) events.APIGatewayV2HTTPResponse {
	out := events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers:    map[string]string{},
		Cookies:    header.Values("Set-Cookie"),
	}
	names := make([]string, 0, len(header))
	for name := range header {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch http.CanonicalHeaderKey(name) {
		case "Set-Cookie":
		case "Content-Length", "Connection", "Transfer-Encoding":
			// The gateway sets its own.
		default:
			out.Headers[name] = strings.Join(header.Values(name), ", ")
		}
	}
	if header.Get("Content-Encoding") != "" || !utf8.Valid(body) {
		out.IsBase64Encoded = true
		out.Body = base64.StdEncoding.EncodeToString(body)
	} else {
		out.Body = string(body)
	}
	return out
}

func text(status int, body string) events.APIGatewayV2HTTPResponse {
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers:    map[string]string{"Content-Type": "text/plain; charset=utf-8"},
		Body:       body,
	}
}

// recorder is the [http.ResponseWriter] a request is served into.
type recorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
	wrote  bool
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	if r.wrote {
		return
	}
	r.wrote, r.code = true, code
}

func (r *recorder) Write(p []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(p)
}

// Flush does nothing: a response goes out whole, when the handler returns.
func (r *recorder) Flush() {}

func (r *recorder) status() int {
	if r.code == 0 {
		return http.StatusOK
	}
	return r.code
}

var _ io.Writer = (*recorder)(nil)
