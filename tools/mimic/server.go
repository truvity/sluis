package mimic

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Options are what a caller can set beside the config.
type Options struct {
	// HTTPClient reaches the stable sluis; default a client with a 10 s timeout.
	HTTPClient *http.Client
	// Now is the clock; default time.Now.
	Now func() time.Time
	// Logger; default slog.Default. No token, code or secret is ever logged.
	Logger *slog.Logger
}

// Server is mimic: the sign-in at the stable sluis, the hand-off of a CI job's token, and one issuer per tenant.
type Server struct {
	cfg     *Config
	keys    *keys
	sluis   *stable
	tenants map[string]*tenant // by path, "/<profile>/<tenant>"
	secure  bool
	now     func() time.Time
	log     *slog.Logger
	mux     *http.ServeMux

	mu    sync.Mutex
	codes map[string]*grant
}

// New checks the config and makes the server. It refuses a config whose environment is not "devel" (ADR 0054,
// decision 3), and it reads every key and secret now, so a missing one fails the start and not a sign-in.
func New(cfg *Config, o Options) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("no config")
	}
	if cfg.Environment != Environment {
		return nil, fmt.Errorf("mimic runs on %q only, and the config says %q", Environment, cfg.Environment)
	}
	if err := cfg.Check(); err != nil {
		return nil, err
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	k, err := loadKeys(cfg.Keys)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg: cfg, keys: k, tenants: map[string]*tenant{}, secure: strings.HasPrefix(cfg.PublicURL, "https://"),
		now: o.Now, log: o.Logger, codes: map[string]*grant{},
	}
	s.sluis = &stable{cfg: cfg.Sluis, http: o.HTTPClient, now: o.Now}
	if cfg.Sluis.ClientSecret != "" {
		b, err := readSource(cfg.Sluis.ClientSecret)
		if err != nil {
			return nil, fmt.Errorf("sluis.client_secret: %w", err)
		}
		s.sluis.secret = string(b)
	}
	for i := range cfg.Tenants {
		t, err := newTenant(s, &cfg.Tenants[i])
		if err != nil {
			return nil, fmt.Errorf("tenants[%d]: %w", i, err)
		}
		s.tenants[t.cfg.path()] = t
	}
	s.routes()
	return s, nil
}

// Issuer is the issuer URL of a tenant.
func (s *Server) Issuer(profile, tenant string) string {
	return s.cfg.PublicURL + "/" + profile + "/" + tenant
}

// ServeHTTP serves every endpoint of mimic.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	m.HandleFunc("GET /login", s.handleLogin)
	m.HandleFunc("GET /callback", s.handleCallback)
	m.HandleFunc("POST /session", s.handleHandoff)
	m.HandleFunc("POST /logout", s.handleLogout)
	m.HandleFunc("GET /logout", s.handleLogout)
	for p, t := range s.tenants {
		m.HandleFunc("GET "+p+"/.well-known/openid-configuration", t.handleDiscovery)
		m.HandleFunc("GET "+p+"/jwks", t.handleJWKS)
		m.HandleFunc("GET "+p+"/authorize", t.handleAuthorize)
		m.HandleFunc("POST "+p+"/token", t.handleToken)
		m.HandleFunc("GET "+p+"/userinfo", t.handleUserinfo)
		m.HandleFunc("POST "+p+"/userinfo", t.handleUserinfo)
	}
	s.mux = m
}

// random is n random bytes, base64url.
func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// s256 is the PKCE S256 challenge of a verifier (RFC 7636 section 4.2).
func s256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (s *Server) callbackURL() string { return s.cfg.PublicURL + "/callback" }

// localPath accepts a path on mimic's own host, such as the authorize request a person started from, and nothing
// else: no scheme, no host, no "//" and no backslash.
func localPath(next string) bool {
	if next == "" || next[0] != '/' || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return false
	}
	u, err := url.Parse(next)
	return err == nil && u.Scheme == "" && u.Host == ""
}

// handleLogin starts a person's sign-in at the stable sluis, with PKCE, a state and a nonce kept in a sealed cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if next == "" {
		next = "/"
	}
	if !localPath(next) {
		http.Error(w, "next: a path on this host", http.StatusBadRequest)
		return
	}
	l := login{State: random(24), Nonce: random(24), Verifier: random(32), Next: next, Exp: s.now().Add(10 * time.Minute).Unix()}
	to, err := s.sluis.authorizeURL(r.Context(), s.callbackURL(), l.State, l.Nonce, s256(l.Verifier))
	if err != nil {
		s.log.Warn("mimic: the stable sluis cannot be reached", "error", err)
		http.Error(w, "the stable sluis cannot be reached", http.StatusBadGateway)
		return
	}
	v, err := s.keys.seal(loginCookie, l)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setCookie(w, loginCookie, v, time.Unix(l.Exp, 0))
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// handleCallback finishes a person's sign-in: the state of the cookie, the code for the ID token, the checks of the
// token, and the session cookie.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(loginCookie)
	var l login
	if err != nil || s.keys.open(loginCookie, c.Value, &l) != nil || s.now().Unix() >= l.Exp {
		http.Error(w, "no sign-in in progress; start again", http.StatusBadRequest)
		return
	}
	s.clearCookie(w, loginCookie)
	q := r.URL.Query()
	if q.Get("state") != l.State {
		http.Error(w, "state: does not match the sign-in", http.StatusBadRequest)
		return
	}
	if e := q.Get("error"); e != "" {
		http.Error(w, "the stable sluis refused the sign-in", http.StatusForbidden)
		return
	}
	if iss := q.Get("iss"); iss != "" && iss != s.cfg.Sluis.Issuer {
		http.Error(w, "iss: another issuer", http.StatusBadRequest)
		return
	}
	sub, err := s.sluis.exchange(r.Context(), q.Get("code"), s.callbackURL(), l.Verifier, l.Nonce)
	if err != nil {
		s.log.Info("mimic: a person's sign-in was refused", "reason", err.Error())
		http.Error(w, "the sign-in was refused", http.StatusForbidden)
		return
	}
	if err := s.startSession(w, sub, "person"); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.log.Info("mimic: session started", "via", "person", "sluis_sub", sub.Sub)
	http.Redirect(w, r, l.Next, http.StatusSeeOther)
}

// handleHandoff takes a CI job's token of the stable sluis, got by workload identity for mimic's audience, and answers
// with the session cookie.
func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_request"`)
		http.Error(w, "a sluis token as the bearer token", http.StatusUnauthorized)
		return
	}
	sub, err := s.sluis.verify(r.Context(), strings.TrimSpace(raw), s.cfg.Sluis.Audience, "", "at+jwt", "jwt", "")
	if err != nil {
		s.log.Info("mimic: a hand-off was refused", "reason", err.Error())
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "the token was refused", http.StatusUnauthorized)
		return
	}
	if err := s.startSession(w, sub, "ci"); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.log.Info("mimic: session started", "via", "ci", "sluis_sub", sub.Sub)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	s.clearCookie(w, SessionCookie)
	w.WriteHeader(http.StatusNoContent)
}
