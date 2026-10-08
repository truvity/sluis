// Package openbao is the one client the OpenBao (or Vault) backends of the
// storage module share: [github.com/truvity/sluis/storage/state/openbao] for
// state on KV version 2, and [github.com/truvity/sluis/storage/keys/transit]
// for keys on the transit engine.
//
// It does the three things both need and nothing else: log in with a JWT (a
// projected ServiceAccount token, read from its file at every login so a
// token the kubelet rotated is always the current one), keep the resulting
// token until most of its lease has gone, and make one request inside a
// namespace, turning a refusal into an [Error] that carries the status and
// the server's messages.
//
// The client does not connect when it is built: a server that is down at
// start must not stop a process that can run without it until first use.
package openbao

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultLoginMount is the JWT auth method's mount when [Login.Mount] is empty.
const DefaultLoginMount = "jwt"

// DefaultServiceAccountToken is where a pod's ServiceAccount token is. It is
// a convenient [Login.TokenFile] when the auth mount trusts the cluster's
// issuer for that token's audience.
const DefaultServiceAccountToken = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// TokenSource returns the JWT to log in with, afresh on every call.
type TokenSource func(ctx context.Context) (string, error)

// Login is how the process signs in: a JWT presented to a JWT auth mount
// under a role. The role binds the token's subject and audience and names the
// policies the session gets, so no long-lived secret is stored anywhere.
type Login struct {
	// Mount is the auth method's mount inside the namespace. Empty is
	// [DefaultLoginMount].
	Mount string
	// Role is the role to ask for. It is required.
	Role string
	// TokenFile holds the JWT. It is read at every login.
	TokenFile string
	// Source replaces TokenFile, for a token that does not live in a file.
	Source TokenSource
}

// Config configures [New].
type Config struct {
	// Address is the server, https://openbao.example:8200, with no path.
	Address string
	// Namespace is the OpenBao namespace every request (the login included)
	// is sent in. Empty is the root namespace.
	Namespace string
	// CAFile is a PEM bundle trusted in addition to the system roots, for a
	// server whose certificate comes from a private chain. CAPEM is the same
	// as bytes.
	CAFile string
	CAPEM  []byte

	// Login is the way to authenticate: set it, or Token, not both.
	Login *Login
	// Token is a static token, for a development server or a token something
	// else keeps renewed. A production process uses Login.
	Token string

	// AllowInsecureHTTP permits an http:// address. A token and a login JWT
	// cross this connection, so it is for a development server on a loopback
	// address only.
	AllowInsecureHTTP bool

	// HTTPClient replaces the HTTP client (and CAFile, CAPEM), for a test.
	HTTPClient *http.Client
	// Now replaces the clock, for a test.
	Now func() time.Time
}

// Error is a refusal from the server, kept whole so a caller can tell "no
// such key" (404) from "check-and-set mismatch" (400 with a message) without
// parsing a sentence it was handed as an error string.
type Error struct {
	Method string
	Path   string
	Status int
	// Errors are the server's messages ("errors" in the body).
	Errors []string
}

func (e *Error) Error() string {
	msg := strings.Join(e.Errors, "; ")
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("openbao: %s %s: %d: %s", e.Method, e.Path, e.Status, msg)
}

// Says reports whether one of the server's messages contains phrase, ignoring
// case. The server reports some distinct faults as 400s that differ only in
// their text, so the text is what there is to go on.
func (e *Error) Says(phrase string) bool {
	phrase = strings.ToLower(phrase)
	for _, m := range e.Errors {
		if strings.Contains(strings.ToLower(m), phrase) {
			return true
		}
	}
	return false
}

// Status returns the HTTP status of the refusal err wraps, or 0 if it is not
// one (a transport fault, a login that could not read its token).
func Status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// AsError returns the [Error] err wraps.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// Client talks to one OpenBao. It is safe for concurrent use.
type Client struct {
	base      *url.URL
	namespace string
	login     *Login
	static    string
	source    TokenSource
	http      *http.Client
	now       func() time.Time

	mu       sync.Mutex
	token    string
	renewAt  time.Time
	loggedAt time.Time
}

// New validates cfg and returns the client. It does no I/O.
func New(cfg Config) (*Client, error) {
	base, err := url.Parse(cfg.Address)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" ||
		(base.Path != "" && base.Path != "/") {
		return nil, fmt.Errorf("openbao: address %q is not a URL with a host and no path or credentials", cfg.Address)
	}
	switch {
	case base.Scheme == "https":
	case base.Scheme == "http" && cfg.AllowInsecureHTTP:
	default:
		return nil, fmt.Errorf("openbao: address %q must be https: a token and a JWT cross this connection "+
			"(AllowInsecureHTTP is for a development server)", cfg.Address)
	}
	base.Path = ""
	c := &Client{base: base, namespace: cfg.Namespace, static: cfg.Token, now: cfg.Now, http: cfg.HTTPClient}
	switch {
	case cfg.Login != nil && cfg.Token != "":
		return nil, errors.New("openbao: take one of a login or a token, not both")
	case cfg.Login == nil && cfg.Token == "":
		return nil, errors.New("openbao: a login (or, for development, a token) is required")
	case cfg.Login != nil:
		l := *cfg.Login
		if l.Mount == "" {
			l.Mount = DefaultLoginMount
		}
		l.Mount = strings.Trim(l.Mount, "/")
		if l.Role == "" {
			return nil, errors.New("openbao: login.role is required")
		}
		c.login = &l
		switch {
		case l.Source != nil:
			c.source = l.Source
		case l.TokenFile != "":
			c.source = fileTokenSource(l.TokenFile)
		default:
			return nil, errors.New("openbao: login needs a tokenFile (or a token source)")
		}
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.http == nil {
		if c.http, err = httpClient(cfg); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func fileTokenSource(path string) TokenSource {
	return func(context.Context) (string, error) {
		raw, err := os.ReadFile(path) //nolint:gosec // the path is the deployment's configuration
		if err != nil {
			return "", fmt.Errorf("openbao: read the login token: %w", err)
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("openbao: the login token file %s is empty", path)
		}
		return token, nil
	}
}

func httpClient(cfg Config) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	pem := append([]byte(nil), cfg.CAPEM...)
	if cfg.CAFile != "" {
		raw, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("openbao: read the CA bundle: %w", err)
		}
		pem = append(pem, raw...)
	}
	if len(pem) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("openbao: the CA bundle holds no certificate")
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		// A redirect would carry the token, the namespace and the login JWT
		// to wherever the server names: the 3xx itself is the answer, an error.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// EscapePath escapes each segment of a slash-separated path for a URL.
func EscapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// freshToken is how old a token must be before a 403 is put down to the
// token rather than to the policy.
const freshToken = 30 * time.Second

// Response is a successful answer.
type Response struct {
	Status int
	// Data is the "data" member of the body, or nil if there is none.
	Data json.RawMessage
	// Warnings are the server's.
	Warnings []string
}

// Request sends method to /v1/<path> with body (marshalled as JSON, or nil)
// and decodes the envelope. path is already URL-escaped and may carry a
// query string. Any non-2xx status is an [*Error]. The method may be "LIST".
//
// With a login, a 403 on a token that is not new is answered once by logging
// in again, since a session can end before its lease says (revoked, or the
// server restored from a snapshot); a second refusal is the policy's.
func (c *Client) Request(ctx context.Context, method, path string, body any) (*Response, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		token, err := c.session(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		status, raw, err := c.do(ctx, method, path, token, payload)
		if err != nil {
			return nil, err
		}
		if status == http.StatusForbidden && c.login != nil && attempt == 0 && c.tokenAge() >= freshToken {
			continue
		}
		return decode(method, path, status, raw)
	}
}

func decode(method, path string, status int, raw []byte) (*Response, error) {
	if status/100 != 2 {
		refusal := &Error{Method: method, Path: pathOnly(path), Status: status}
		var body struct {
			Errors []string `json:"errors"`
		}
		if json.Unmarshal(raw, &body) == nil {
			refusal.Errors = body.Errors
		}
		return nil, refusal
	}
	out := &Response{Status: status}
	if len(bytes.TrimSpace(raw)) == 0 {
		return out, nil
	}
	var env struct {
		Data     json.RawMessage `json:"data"`
		Warnings []string        `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("openbao: %s %s: the answer is not JSON: %w", method, pathOnly(path), err)
	}
	if string(env.Data) != "null" {
		out.Data = env.Data
	}
	out.Warnings = env.Warnings
	return out, nil
}

func pathOnly(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return p[:i]
	}
	return p
}

func (c *Client) tokenAge() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loggedAt.IsZero() {
		return 0
	}
	return c.now().Sub(c.loggedAt)
}

func (c *Client) do(ctx context.Context, method, path, token string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+"/v1/"+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if c.namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.namespace)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return 0, nil, fmt.Errorf("openbao: %s %s: %w", method, pathOnly(path), err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("openbao: %s %s: %w", method, pathOnly(path), err)
	}
	return resp.StatusCode, raw, nil
}

// session returns the token to use, logging in when there is none, when most
// of its lease has gone, or when force is set.
func (c *Client) session(ctx context.Context, force bool) (string, error) {
	if c.login == nil {
		return c.static, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && !force && c.now().Before(c.renewAt) {
		return c.token, nil
	}
	jwt, err := c.source(ctx)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"role": c.login.Role, "jwt": jwt})
	path := "auth/" + EscapePath(c.login.Mount) + "/login"
	status, raw, err := c.do(ctx, http.MethodPost, path, "", body)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		if _, err = decode(http.MethodPost, path, status, raw); err == nil {
			err = fmt.Errorf("status %d", status)
		}
		return "", fmt.Errorf("openbao: log in as role %q: %w", c.login.Role, err)
	}
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int64  `json:"lease_duration"`
		} `json:"auth"`
	}
	if err = json.Unmarshal(raw, &out); err != nil || out.Auth.ClientToken == "" {
		return "", fmt.Errorf("openbao: the login on %s answered no token", c.login.Mount)
	}
	lease := time.Duration(out.Auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = time.Minute
	}
	c.token, c.loggedAt = out.Auth.ClientToken, c.now()
	c.renewAt = c.loggedAt.Add(lease * 8 / 10)
	return c.token, nil
}
