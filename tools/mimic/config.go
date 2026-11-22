package mimic

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Environment is the one environment mimic runs in (ADR 0054, decision 3).
const Environment = "devel"

// The profiles: each shapes the claims like its real counterpart.
const (
	ProfileEntra  = "entra"
	ProfileGoogle = "google"
)

// DefaultActAs is the act-as group of a tenant whose config names none: the five-segment grant name of the stable
// line (v1 ADR 0081), with the tenant's name as the customer.
func DefaultActAs(tenant string) string { return "devel:shared:keycloak:" + tenant + ":act-as" }

// Config is mimic's config file.
type Config struct {
	// Environment must be "devel"; New refuses anything else.
	Environment string `yaml:"environment"`
	// PublicURL is the address relying parties reach mimic at; each issuer is <PublicURL>/<profile>/<tenant>. HTTPS,
	// or HTTP on a loopback address for a test.
	PublicURL string `yaml:"public_url"`
	// Listen is the address the server listens on, such as 127.0.0.1:8080.
	Listen string `yaml:"listen"`
	// SessionTTL is how long a session cookie lasts. Default 1h, at most 12h.
	SessionTTL time.Duration `yaml:"session_ttl"`
	Keys       Keys          `yaml:"keys"`
	Sluis      Sluis         `yaml:"sluis"`
	Tenants    []Tenant      `yaml:"tenants"`
}

// Keys are where mimic's own keys are. Each is a source: "file:<path>" or "env:<NAME>".
type Keys struct {
	// Signing is a PKCS#8 PEM private key, ECDSA P-256 (ES256) or RSA of 2048 bits or more (RS256).
	Signing string `yaml:"signing"`
	// Secret is at least 32 random bytes; the subject hash and the session cookie use keys derived from it.
	Secret string `yaml:"secret"`
}

// Sluis is the stable sluis that mimic is a relying party of.
type Sluis struct {
	// Issuer is the stable sluis's issuer URL; its discovery document is read from it.
	Issuer string `yaml:"issuer"`
	// ClientID is mimic's client at the stable sluis.
	ClientID string `yaml:"client_id"`
	// ClientSecret is a source of the client's secret; empty for a public client, which signs in with PKCE alone.
	ClientSecret string `yaml:"client_secret"`
	// Audience is the audience a CI job's token must carry; default ClientID.
	Audience string `yaml:"audience"`
	// Scopes are asked for at sign-in; default [openid].
	Scopes []string `yaml:"scopes"`
	// Algorithms are the signing algorithms accepted from the stable sluis; default [ES384].
	Algorithms []string `yaml:"algorithms"`
	// RequireAMR, when set, refuses a person's sign-in whose ID token has none of these `amr` values.
	RequireAMR []string `yaml:"require_amr"`
}

// Tenant is one issuer: a profile, a test domain, the group that may act as its identities, its clients and its
// identities.
type Tenant struct {
	Name    string `yaml:"name"`
	Profile string `yaml:"profile"`
	// Domain is the made-up test domain of the identities' e-mail, under .test or .example.
	Domain string `yaml:"domain"`
	// ActAs is the sluis group whose holders may use this tenant's identities; default DefaultActAs(Name).
	ActAs      string     `yaml:"act_as"`
	Clients    []Client   `yaml:"clients"`
	Identities []Identity `yaml:"identities"`
}

// Client is a relying party of a tenant, such as a realm of the group's identity provider.
type Client struct {
	ID           string   `yaml:"id"`
	RedirectURIs []string `yaml:"redirect_uris"`
	// Secret is a source of the client's secret; empty for a public client, which must use PKCE (it always does).
	Secret string `yaml:"secret"`
}

// Identity is one fixed test identity.
type Identity struct {
	// Username names the identity; a CI job picks it as login_hint.
	Username string `yaml:"username"`
	// Email is the local part of the address; the tenant's domain follows it. Default Username.
	Email  string   `yaml:"email"`
	Name   string   `yaml:"name"`
	Groups []string `yaml:"groups"`
}

// Issuer is the path of a tenant's issuer under the public URL.
func (t *Tenant) path() string { return "/" + t.Profile + "/" + t.Name }

// LoadConfig reads and checks a config file. Unknown keys are refused.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, err
	}
	return ParseConfig(b)
}

// ParseConfig reads and checks a config document.
func ParseConfig(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := c.Check(); err != nil {
		return nil, err
	}
	return &c, nil
}

var (
	nameRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	labelRE    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	clientRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	groupRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,254}$`)
)

// testTLDs are the reserved top-level names a test domain may end in (RFC 2606 section 2, RFC 6761 section 6.2):
// no real customer's domain can be one of them.
var testTLDs = []string{"test", "example"}

// Check applies the defaults and refuses a config that mimic must not run with. An error names the field, never a
// secret.
func (c *Config) Check() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if c.Environment != Environment {
		bad("environment: mimic runs on %q only, and this config says %q", Environment, c.Environment)
	}
	if err := checkBaseURL(c.PublicURL); err != nil {
		bad("public_url: %v", err)
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	switch {
	case c.SessionTTL == 0:
		c.SessionTTL = time.Hour
	case c.SessionTTL < time.Minute || c.SessionTTL > 12*time.Hour:
		bad("session_ttl: between 1m and 12h")
	}
	for k, v := range map[string]string{"keys.signing": c.Keys.Signing, "keys.secret": c.Keys.Secret} {
		if err := checkSource(v); err != nil {
			bad("%s: %v", k, err)
		}
	}
	errs = append(errs, c.Sluis.check()...)
	if len(c.Tenants) == 0 {
		bad("tenants: at least one")
	}
	seen := map[string]bool{}
	for i := range c.Tenants {
		t := &c.Tenants[i]
		at := fmt.Sprintf("tenants[%d]", i)
		errs = append(errs, t.check(at)...)
		if seen[t.path()] {
			bad("%s: %s is declared twice", at, t.path())
		}
		seen[t.path()] = true
	}
	return errors.Join(errs...)
}

func (s *Sluis) check() []error {
	var errs []error
	if err := checkBaseURL(s.Issuer); err != nil {
		errs = append(errs, fmt.Errorf("sluis.issuer: %w", err))
	}
	if !clientRE.MatchString(s.ClientID) {
		errs = append(errs, errors.New("sluis.client_id: a client id"))
	}
	if s.ClientSecret != "" {
		if err := checkSource(s.ClientSecret); err != nil {
			errs = append(errs, fmt.Errorf("sluis.client_secret: %w", err))
		}
	}
	if s.Audience == "" {
		s.Audience = s.ClientID
	}
	if len(s.Scopes) == 0 {
		s.Scopes = []string{"openid"}
	}
	if !slices.Contains(s.Scopes, "openid") {
		errs = append(errs, errors.New("sluis.scopes: must hold openid"))
	}
	if len(s.Algorithms) == 0 {
		s.Algorithms = []string{"ES384"}
	}
	for _, a := range s.Algorithms {
		if !slices.Contains(verifiable, a) {
			errs = append(errs, fmt.Errorf("sluis.algorithms: %q is not one of %v", a, verifiable))
		}
	}
	return errs
}

func (t *Tenant) check(at string) []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(at+"."+format, a...)) }
	if !nameRE.MatchString(t.Name) {
		bad("name: lower-case letters, digits and -")
	}
	if t.Profile != ProfileEntra && t.Profile != ProfileGoogle {
		bad("profile: %q or %q", ProfileEntra, ProfileGoogle)
	}
	if err := checkTestDomain(t.Domain); err != nil {
		bad("domain: %v", err)
	}
	if t.ActAs == "" {
		t.ActAs = DefaultActAs(t.Name)
	}
	if !groupRE.MatchString(t.ActAs) {
		bad("act_as: a group name")
	}
	if len(t.Clients) == 0 {
		bad("clients: at least one")
	}
	ids := map[string]bool{}
	for j, cl := range t.Clients {
		if !clientRE.MatchString(cl.ID) || ids[cl.ID] {
			bad("clients[%d].id: a client id, once", j)
		}
		ids[cl.ID] = true
		if len(cl.RedirectURIs) == 0 {
			bad("clients[%d].redirect_uris: at least one", j)
		}
		for _, r := range cl.RedirectURIs {
			if err := checkRedirect(r); err != nil {
				bad("clients[%d].redirect_uris: %v", j, err)
			}
		}
		if cl.Secret != "" {
			if err := checkSource(cl.Secret); err != nil {
				bad("clients[%d].secret: %v", j, err)
			}
		}
	}
	if len(t.Identities) == 0 {
		bad("identities: at least one")
	}
	users, mails := map[string]bool{}, map[string]bool{}
	for j := range t.Identities {
		id := &t.Identities[j]
		if !usernameRE.MatchString(id.Username) || users[id.Username] {
			bad("identities[%d].username: lower-case letters, digits, '.', '_' and '-', once", j)
		}
		users[id.Username] = true
		if id.Email == "" {
			id.Email = id.Username
		}
		if !usernameRE.MatchString(id.Email) || mails[id.Email] {
			bad("identities[%d].email: a local part of lower-case letters, digits, '.', '_' and '-', once", j)
		}
		mails[id.Email] = true
		if strings.TrimSpace(id.Name) == "" || len(id.Name) > 128 {
			bad("identities[%d].name: 1 to 128 characters", j)
		}
		for _, g := range id.Groups {
			if !groupRE.MatchString(g) {
				bad("identities[%d].groups: %q is not a group name", j, g)
			}
		}
	}
	return errs
}

// checkTestDomain refuses a domain that is not under a reserved test name, so a token never carries a real domain.
func checkTestDomain(d string) error {
	labels := strings.Split(d, ".")
	if len(labels) < 2 || !slices.Contains(testTLDs, labels[len(labels)-1]) {
		return fmt.Errorf("%q is not under a reserved test name (.test or .example)", d)
	}
	for _, l := range labels {
		if !labelRE.MatchString(l) {
			return fmt.Errorf("%q is not a lower-case domain name", d)
		}
	}
	return nil
}

// checkBaseURL takes an absolute HTTPS URL with no query or fragment, or HTTP on a loopback host for a test.
func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.HasSuffix(raw, "/") {
		return errors.New("an absolute URL with no query, fragment, user or trailing slash")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && loopback(u.Hostname()):
	default:
		return errors.New("https, or http on a loopback address")
	}
	return nil
}

func checkRedirect(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%q is not an absolute URL without a fragment", raw)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback(u.Hostname())) {
		return fmt.Errorf("%q: https, or http on a loopback address", raw)
	}
	return nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkSource takes "file:<path>" or "env:<NAME>".
func checkSource(s string) error {
	switch {
	case strings.HasPrefix(s, "file:") && len(s) > len("file:"):
	case strings.HasPrefix(s, "env:") && len(s) > len("env:"):
	default:
		return errors.New(`a source, "file:<path>" or "env:<NAME>"`)
	}
	return nil
}

// readSource reads a source. A trailing newline, as a tool writes one, is dropped. The value is never logged.
func readSource(s string) ([]byte, error) {
	var b []byte
	switch {
	case strings.HasPrefix(s, "file:"):
		var err error
		b, err = os.ReadFile(strings.TrimPrefix(s, "file:"))
		if err != nil {
			return nil, err
		}
	case strings.HasPrefix(s, "env:"):
		name := strings.TrimPrefix(s, "env:")
		v, ok := os.LookupEnv(name)
		if !ok || v == "" {
			return nil, fmt.Errorf("the environment variable %s is not set", name)
		}
		b = []byte(v)
	default:
		return nil, checkSource(s)
	}
	b = bytes.TrimSuffix(b, []byte("\n"))
	b = bytes.TrimSuffix(b, []byte("\r"))
	if len(b) == 0 {
		return nil, errors.New("the source is empty")
	}
	return b, nil
}
