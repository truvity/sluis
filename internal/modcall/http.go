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

// maxBody bounds a call and its answer.
const maxBody = 1 << 20

// RPCPath is where a module's Service answers calls.
const RPCPath = "/rpc"

// Verifier checks the bearer of a call and says who it is. On Kubernetes it is
// a TokenReview of a projected ServiceAccount token whose audience is this
// module (docs/decisions/0071, 4).
type Verifier func(ctx context.Context, bearer string) (subject string, err error)

// TokenSource yields the bearer for a call to the module whose audience is
// given (internal/consoleauth.Source, one per audience).
type TokenSource func(ctx context.Context, audience string) (string, error)

// Handler serves the module's calls at [RPCPath]. A call with no bearer, or one
// verify refuses, is a 401 and reaches no method.
func (s *Server) Handler(verify Verifier) http.Handler {
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
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(s.Dispatch(WithCaller(r.Context(), subject), req))
	})
	return mux
}

// HTTPCaller calls modules' Services.
type HTTPCaller struct {
	// URLs maps a module to its Service's base URL; Audiences to the audience of
	// the bearer it verifies (the module's name when absent).
	URLs      map[string]string
	Audiences map[string]string
	Token     TokenSource
	Client    *http.Client
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
	body, err := json.Marshal(Request{Kind: Kind, Module: module, Method: method, Payload: payload})
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
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("%w: module %s: %w", ErrTransport, module, err)
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
