package modcall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxBody bounds what the listener reads of a call: the largest payload any
// method may take plus the envelope. A method's own bound ([MaxBytes]) is
// checked by [Server.Dispatch]; this one only stops a body that no method could
// accept.
const maxBody = HardMaxBytes + envelopeSlack

// RPCPath is where a module's Service answers calls.
const RPCPath = "/rpc"

// Verifier checks the bearer of a call and says who it is (the token's
// subject). On Kubernetes it is a TokenReview of a projected ServiceAccount
// token whose audience is this module (docs/decisions/0071, 4).
type Verifier func(ctx context.Context, bearer string) (subject string, err error)

// TokenSource yields the bearer for a call to the module whose audience is
// given (internal/consoleauth.Source, one per audience).
type TokenSource func(ctx context.Context, audience string) (string, error)

// HandlerOption configures [Server.Handler].
type HandlerOption func(*handlerConfig)

type handlerConfig struct{ classes map[string]string }

// WithClasses maps verified subjects to caller classes, for a subject whose
// ServiceAccount is not named for its class.
func WithClasses(classes map[string]string) HandlerOption {
	return func(c *handlerConfig) { c.classes = classes }
}

// ClassOfSubject is the caller class of a verified subject: the ServiceAccount's
// name for `system:serviceaccount:<namespace>:<name>`, the subject itself
// otherwise.
func ClassOfSubject(subject string) string {
	if rest, ok := strings.CutPrefix(subject, "system:serviceaccount:"); ok {
		if _, name, found := strings.Cut(rest, ":"); found && name != "" {
			return name
		}
	}
	return subject
}

// Handler serves the module's calls at [RPCPath]. A call with no bearer, or one
// verify refuses, is a 401 and reaches no method. The caller class is the
// verified subject's ([ClassOfSubject], or [WithClasses]); the request body
// has no say in it.
func (s *Server) Handler(verify Verifier, opts ...HandlerOption) http.Handler {
	var hc handlerConfig
	for _, o := range opts {
		o(&hc)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+RPCPath, func(w http.ResponseWriter, r *http.Request) {
		bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || bearer == "" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		subject, err := verify(r.Context(), bearer)
		if err != nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		var req Request
		if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeJSON(w, Response{Error: &Error{Code: CodeBadRequest, Message: "the request is too large"}})
				return
			}
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		class, mapped := hc.classes[subject]
		if !mapped {
			class = ClassOfSubject(subject)
		}
		writeJSON(w, s.Dispatch(WithCaller(r.Context(), class), req))
	})
	return mux
}

func writeJSON(w http.ResponseWriter, resp Response) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// HTTPCaller calls modules' Services.
type HTTPCaller struct {
	// URLs maps a module to its Service's base URL; Audiences to the audience of
	// the bearer it verifies (the module's name when absent).
	URLs      map[string]string
	Audiences map[string]string
	Token     TokenSource
	Client    *http.Client
	// MaxBytes bounds a request payload and a result ([DefaultMaxBytes] when 0);
	// raise it only for a callee that registered a larger [MaxBytes].
	MaxBytes int
}

// Call implements [Caller].
func (c *HTTPCaller) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	base, ok := c.URLs[module]
	if !ok {
		return nil, fmt.Errorf("%w: no Service is configured for module %q", ErrNoRoute, module)
	}
	audience := c.Audiences[module]
	if audience == "" {
		audience = module
	}
	bearer, err := c.Token(ctx, audience)
	if err != nil {
		return nil, &Error{Code: CodeUnavailable, Message: "no credential for " + module}
	}
	ctx, envelope, p, err := Begin(ctx, module, method, payload, c.MaxBytes)
	if err != nil {
		return nil, err
	}
	out, err := c.post(ctx, base, bearer, module, envelope, p.max)
	return p.Finish(out, err)
}

func (c *HTTPCaller) post(ctx context.Context, base, bearer, module string, envelope Request, maxBytes int) ([]byte, error) {
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+RPCPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: module %s: %w", ErrTransport, module, err)
	}
	defer func() { _ = res.Body.Close() }()
	limit := int64(maxBytes + envelopeSlack)
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: module %s: %w", ErrTransport, module, err)
	}
	if int64(len(raw)) > limit {
		return nil, &Error{Code: CodeInternal, Message: "the result is too large"}
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: module %s answered %d", ErrTransport, module, res.StatusCode)
	}
	var out Response
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: module %s: the answer is not valid", ErrTransport, module)
	}
	return Result(out)
}

var (
	// ErrNoRoute is a call to a module no transport is configured for.
	ErrNoRoute = errors.New("module call: no route")
	// ErrTransport is a call that did not get an answer from the callee.
	ErrTransport = errors.New("module call: transport failed")
)
