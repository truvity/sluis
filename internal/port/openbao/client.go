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

	"github.com/truvity/sluis/internal/port"
)

// The login methods.
const (
	// MethodKubernetes is the Kubernetes auth method, mounted at `kubernetes`
	// unless the configuration says otherwise.
	MethodKubernetes = "kubernetes"
	// MethodJWT is the JWT/OIDC auth method, mounted at `jwt` unless the
	// configuration says otherwise.
	MethodJWT = "jwt"
)

// DefaultServiceAccountToken is where a pod's ServiceAccount token is.
const DefaultServiceAccountToken = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// DefaultMount is the KV version 2 mount.
const DefaultMount = "kv"

// TokenSource returns the JWT to log in with, afresh on every call. A Lambda
// plugs in the web identity token of its outbound federation here.
type TokenSource func(ctx context.Context) (string, error)

// FileTokenSource reads the JWT from a file on every call, so a projected
// token the kubelet rotates is always the current one.
func FileTokenSource(path string) TokenSource {
	return func(context.Context) (string, error) {
		raw, err := os.ReadFile(path) //nolint:gosec // the path is the deployment's configuration
		if err != nil {
			return "", fmt.Errorf("read the login token: %w", err)
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("the login token file %s is empty", path)
		}
		return token, nil
	}
}

// Auth is how the service logs in.
type Auth struct {
	// Method is [MethodKubernetes] or [MethodJWT].
	Method string
	// Mount is the auth method's mount path inside each namespace. Empty is
	// the method's own name.
	Mount string
	// Role is the role the login asks for.
	Role string
	// TokenFile is where the JWT is read from. Empty is the ServiceAccount
	// token for [MethodKubernetes]; [MethodJWT] needs this or Token.
	TokenFile string
	// Token, when set, is the JWT source and TokenFile is ignored.
	Token TokenSource
}

// Config is what NewClient and New need.
type Config struct {
	// Address is the server: https://openbao.example, with no path.
	Address string
	// CAFile and CAPEM are the certificate authorities the server's
	// certificate is verified against, instead of the system's. Both empty
	// uses the system's.
	CAFile string
	CAPEM  []byte
	// Mount is the KV version 2 mount. Empty is [DefaultMount].
	Mount string
	// Namespace is the OpenBao namespace a target without one writes to.
	Namespace string
	Auth      Auth
	// Client replaces the HTTP client, for a test.
	Client *http.Client
	// Now replaces the clock, for a test.
	Now func() time.Time
}

// Client is the HTTP client of an OpenBao: the login, the token per namespace and
// one request, shared by the Export and the Secrets adapters.
type Client struct {
	base      *url.URL
	mount     string
	namespace string
	auth      Auth
	token     TokenSource
	client    *http.Client
	now       func() time.Time

	mu       sync.Mutex
	sessions map[string]session
}

type session struct {
	token    string
	renewAt  time.Time
	loggedAt time.Time
}

// NewClient validates the configuration and returns the client. It does not
// connect: an OpenBao that is down at start must not stop the service.
func NewClient(cfg Config) (*Client, error) {
	base, err := url.Parse(cfg.Address)
	if err != nil || base.Host == "" || base.Scheme != "https" ||
		base.User != nil || (base.Path != "" && base.Path != "/") || base.RawQuery != "" {
		return nil, fmt.Errorf("openbao: address %q is not an https URL with a host and no path or credentials "+
			"(a token and a JWT cross this connection: TLS is required)", cfg.Address)
	}
	base.Path = ""
	mount := cfg.Mount
	if mount == "" {
		mount = DefaultMount
	}
	if err = checkPath(mount); err != nil {
		return nil, fmt.Errorf("openbao: mount %q is not a mount path", mount)
	}
	auth := cfg.Auth
	switch auth.Method {
	case MethodKubernetes, MethodJWT:
	default:
		return nil, fmt.Errorf("openbao: auth method %q is %q or %q", auth.Method, MethodKubernetes, MethodJWT)
	}
	if auth.Role == "" {
		return nil, errors.New("openbao: auth.role is required")
	}
	if auth.Mount == "" {
		auth.Mount = auth.Method
	}
	if err = checkPath(auth.Mount); err != nil {
		return nil, fmt.Errorf("openbao: auth.mount %q is not a mount path", auth.Mount)
	}
	token := auth.Token
	if token == nil {
		file := auth.TokenFile
		if file == "" && auth.Method == MethodKubernetes {
			file = DefaultServiceAccountToken
		}
		if file == "" {
			return nil, errors.New("openbao: auth method jwt needs a tokenFile or a token source")
		}
		token = FileTokenSource(file)
	}
	client := cfg.Client
	if client == nil {
		if client, err = httpClient(cfg); err != nil {
			return nil, err
		}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		base: base, mount: mount, namespace: cfg.Namespace, auth: auth, token: token,
		client: client, now: now, sessions: map[string]session{},
	}, nil
}

func httpClient(cfg Config) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	pem := cfg.CAPEM
	if cfg.CAFile != "" {
		raw, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("openbao: read the CA bundle: %w", err)
		}
		pem = append(pem, raw...)
	}
	if len(pem) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("openbao: the CA bundle holds no certificate")
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{
		Transport: transport, Timeout: 30 * time.Second,
		// A redirect would carry the token, the namespace and the login JWT to
		// wherever the server names: the answer is the 3xx itself, an error.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// freshToken is how old a token must be before a 403 is put down to the token.
const freshToken = 30 * time.Second

// tokenAge is how long ago this namespace's token was made; zero if none.
func (s *Client) tokenAge(ns string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.sessions[ns]
	if !ok {
		return 0
	}
	return s.now().Sub(cur.loggedAt)
}

// call makes one request inside a namespace, logging in first when there is
// no live token, and once more when the server refuses the token it had.
func (s *Client) call(ctx context.Context, ns, method, path, contentType string, body []byte) (int, []byte, error) {
	for attempt := 0; ; attempt++ {
		token, err := s.login(ctx, ns, attempt > 0)
		if err != nil {
			return 0, nil, err
		}
		status, raw, err := s.do(ctx, ns, method, path, contentType, token, body)
		if err != nil {
			return 0, nil, err
		}
		if status == http.StatusForbidden && attempt == 0 && s.tokenAge(ns) >= freshToken {
			// A token revoked or expired early reads as forbidden; one fresh
			// login tells that from a policy that does not allow the write. A
			// token made a moment ago was not revoked: the policy refused, and
			// a broken policy must not double the logins of every call.
			continue
		}
		return status, raw, nil
	}
}

func (s *Client) do(ctx context.Context, ns, method, path, contentType, token string, body []byte) (int, []byte, error) {
	endpoint := s.base.String() + "/v1/" + path
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if ns != "" {
		req.Header.Set("X-Vault-Namespace", ns)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %s %s: %s", port.ErrUnavailable, method, path, trimErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %s %s: %s", port.ErrUnavailable, method, path, trimErr(err))
	}
	return resp.StatusCode, raw, nil
}

// login returns the token for a namespace, logging in when it has none or
// most of its lease has gone, or when force is set.
func (s *Client) login(ctx context.Context, ns string, force bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.sessions[ns]; ok && !force && s.now().Before(cur.renewAt) {
		return cur.token, nil
	}
	jwt, err := s.token(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %w", port.ErrUnavailable, err)
	}
	body, _ := json.Marshal(map[string]string{"role": s.auth.Role, "jwt": jwt})
	path := "auth/" + s.auth.Mount + "/login"
	status, raw, err := s.do(ctx, ns, "POST", path, "application/json", "", body)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", answer("log in", target{Namespace: ns, Path: path}, status, raw)
	}
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int64  `json:"lease_duration"`
		} `json:"auth"`
	}
	if err = json.Unmarshal(raw, &out); err != nil || out.Auth.ClientToken == "" {
		return "", fmt.Errorf("%w: the login answered no token", port.ErrUnavailable)
	}
	lease := time.Duration(out.Auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = time.Minute
	}
	s.sessions[ns] = session{token: out.Auth.ClientToken, renewAt: s.now().Add(lease * 8 / 10), loggedAt: s.now()}
	return out.Auth.ClientToken, nil
}

// trimErr is a transport error without the URL the client already named.
func trimErr(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err.Error()
	}
	return err.Error()
}
