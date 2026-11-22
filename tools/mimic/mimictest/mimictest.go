// Package mimictest starts mimic in a test, beside a fake stable sluis, so that a test of mimic, or a federation test
// of sluis, can sign in at a mimic issuer with no network and no key on disk beyond the test's temporary directory.
//
// The fake stable sluis has a discovery document, a key set, an authorization endpoint that signs in the person the
// test names at once, and a token endpoint with PKCE; Token mints a token for a CI job's hand-off. Every key is made
// at test time.
package mimictest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/v2/tools/mimic"
)

// The names of the fake stable sluis and of the test config.
const (
	SluisClientID     = "mimic"
	sluisClientSecret = "test-mimic-client-secret"
	SluisKID          = "sluis-k1"
	ClientSecret      = "test-relying-party-secret"
)

// Person is who the fake stable sluis signs in next.
type Person struct {
	Sub    string
	Groups []string
	AMR    []string
}

// Sluis is a fake stable sluis.
type Sluis struct {
	t      testing.TB
	Server *httptest.Server
	Issuer string
	Key    *ecdsa.PrivateKey

	mu    sync.Mutex
	next  Person
	codes map[string]pending
}

type pending struct {
	person              Person
	nonce, challenge, r string
}

// NewSluis starts a fake stable sluis that signs ES384.
func NewSluis(t testing.TB) *Sluis {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &Sluis{t: t, Key: k, codes: map[string]pending{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": s.Issuer, "authorization_endpoint": s.Issuer + "/authorize", "token_endpoint": s.Issuer + "/token",
			"jwks_uri": s.Issuer + "/jwks",
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: SluisKID, Algorithm: "ES384", Use: "sig"}}})
	})
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("POST /token", s.token)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	s.Issuer = s.Server.URL
	return s
}

// SignInNext sets the person whom the next sign-in at the fake stable sluis signs in.
func (s *Sluis) SignInNext(p Person) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = p
}

func (s *Sluis) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != SluisClientID || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code := rand.Text()
	s.mu.Lock()
	s.codes[code] = pending{person: s.next, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), r: q.Get("redirect_uri")}
	s.mu.Unlock()
	v := url.Values{"code": {code}, "state": {q.Get("state")}, "iss": {s.Issuer}}
	http.Redirect(w, r, q.Get("redirect_uri")+"?"+v.Encode(), http.StatusSeeOther) //nolint:gosec // a fake sluis for tests, on loopback
}

func (s *Sluis) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, secret, _ := r.BasicAuth()
	s.mu.Lock()
	p, ok := s.codes[r.PostForm.Get("code")]
	delete(s.codes, r.PostForm.Get("code"))
	s.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || id != SluisClientID || secret != sluisClientSecret || r.PostForm.Get("redirect_uri") != p.r ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	extra := map[string]any{"nonce": p.nonce}
	if p.person.AMR != nil {
		extra["amr"] = p.person.AMR
	}
	writeJSON(w, map[string]any{"id_token": s.Token(p.person.Sub, p.person.Groups, SluisClientID, "JWT", extra), "token_type": "Bearer"})
}

// Token is a token of the fake stable sluis for the audience, with the type header typ. extra adds or replaces
// claims; a nil value removes one.
func (s *Sluis) Token(sub string, groups []string, aud, typ string, extra map[string]any) string {
	s.t.Helper()
	now := time.Now()
	c := map[string]any{"iss": s.Issuer, "sub": sub, "aud": aud, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "groups": groups}
	for k, v := range extra {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return Sign(s.t, s.Key, jose.ES384, SluisKID, typ, c)
}

// Sign makes a compact JWS of the claims.
func Sign(t testing.TB, key any, alg jose.SignatureAlgorithm, kid, typ string, claims map[string]any) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), kid)
	if typ != "" {
		opts = opts.WithType(jose.ContentType(typ))
	}
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, opts)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(claims)
	obj, err := sg.Sign(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Mimic is a running mimic beside its fake stable sluis.
type Mimic struct {
	t      testing.TB
	Server *httptest.Server
	URL    string
	Config *mimic.Config
	Mimic  *mimic.Server
	Sluis  *Sluis
}

// Callback is the callback of the relying party in the test config. Nothing listens on it: a test stops at the redirect.
const Callback = "http://127.0.0.1:9/callback"

// Config is the test config: two tenants of each profile's shape, each with one confidential client and two
// identities. URL is mimic's public URL and dir holds its keys.
func Config(t testing.TB, publicURL, sluisIssuer string) *mimic.Config {
	t.Helper()
	dir := t.TempDir()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return "file:" + p
	}
	signing := write("signing.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	secret := write("secret", []byte(rand.Text()+rand.Text()+"\n"))
	sluisSecret := write("sluis-secret", []byte(sluisClientSecret))
	clientSecret := write("client-secret", []byte(ClientSecret))
	tenant := func(name, profile string) mimic.Tenant {
		return mimic.Tenant{
			Name: name, Profile: profile, Domain: name + ".test",
			Clients: []mimic.Client{{ID: "rp-" + name, RedirectURIs: []string{Callback}, Secret: clientSecret}},
			Identities: []mimic.Identity{
				{Username: "alice", Name: "Alice Example", Groups: []string{"staff", "admins"}},
				{Username: "bob", Email: "bob.builder", Name: "Bob Example", Groups: []string{"staff"}},
			},
		}
	}
	return &mimic.Config{
		Environment: mimic.Environment, PublicURL: publicURL,
		Keys:    mimic.Keys{Signing: signing, Secret: secret},
		Sluis:   mimic.Sluis{Issuer: sluisIssuer, ClientID: SluisClientID, ClientSecret: sluisSecret},
		Tenants: []mimic.Tenant{tenant("example-a", mimic.ProfileEntra), tenant("example-b", mimic.ProfileGoogle)},
	}
}

// Start starts mimic with the test config, which edit may change first, beside a new fake stable sluis.
func Start(t testing.TB, edit func(*mimic.Config)) *Mimic {
	t.Helper()
	m := &Mimic{t: t, Sluis: NewSluis(t)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m.URL = "http://" + ln.Addr().String()
	m.Config = Config(t, m.URL, m.Sluis.Issuer)
	if edit != nil {
		edit(m.Config)
	}
	m.Mimic, err = mimic.New(m.Config, mimic.Options{})
	if err != nil {
		_ = ln.Close()
		t.Fatalf("mimic.New: %v", err)
	}
	m.Server = &httptest.Server{Listener: ln, Config: &http.Server{Handler: m.Mimic, ReadHeaderTimeout: 5 * time.Second}}
	m.Server.Start()
	t.Cleanup(m.Server.Close)
	return m
}

// Issuer is the issuer of a tenant.
func (m *Mimic) Issuer(profile, tenant string) string { return m.Mimic.Issuer(profile, tenant) }

// Browser is an HTTP client with a cookie jar that follows redirects until one leaves for the relying party's
// callback, and answers that redirect instead.
func (m *Mimic) Browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if strings.HasPrefix(req.URL.String(), Callback) {
			return http.ErrUseLastResponse
		}
		return nil
	}}
}

// HandOff posts a token of the fake stable sluis to mimic as a CI job does, and returns a browser that holds the
// session cookie.
func (m *Mimic) HandOff(sub string, groups []string) *http.Client {
	m.t.Helper()
	b := m.Browser()
	req, _ := http.NewRequest(http.MethodPost, m.URL+"/session", nil)
	req.Header.Set("Authorization", "Bearer "+m.Sluis.Token(sub, groups, SluisClientID, "at+jwt", nil))
	res, err := b.Do(req)
	if err != nil {
		m.t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		m.t.Fatalf("the hand-off answered %d", res.StatusCode)
	}
	return b
}
