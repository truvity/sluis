package mimic_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/v2/tests/jwtbcp"
	"github.com/truvity/sluis/v2/tools/mimic"
	"github.com/truvity/sluis/v2/tools/mimic/mimictest"
)

const (
	actA = "devel:shared:keycloak:example-a:act-as"
	actB = "devel:shared:keycloak:example-b:act-as"
)

// rp is a relying party of one mimic tenant, as a realm of the group's identity provider is.
type rp struct {
	t        *testing.T
	m        *mimictest.Mimic
	issuer   string
	client   string
	verifier string
}

func newRP(t *testing.T, m *mimictest.Mimic, profile, tenant string) *rp {
	return &rp{t: t, m: m, issuer: m.Issuer(profile, tenant), client: "rp-" + tenant, verifier: rand.Text() + rand.Text()}
}

// challenge is the PKCE S256 challenge of a verifier (RFC 7636 section 4.2).
func challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// authorize runs the authorization request in the browser and returns the query of the redirect to the callback.
func (r *rp) authorize(b *http.Client, edit func(url.Values)) url.Values {
	r.t.Helper()
	q := url.Values{
		"response_type": {"code"}, "client_id": {r.client}, "redirect_uri": {mimictest.Callback}, "scope": {"openid email profile"},
		"state": {"st-1"}, "nonce": {"n-1"}, "code_challenge": {challenge(r.verifier)}, "code_challenge_method": {"S256"},
	}
	if edit != nil {
		edit(q)
	}
	res, err := b.Get(r.issuer + "/authorize?" + q.Encode())
	if err != nil {
		r.t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		r.t.Fatalf("authorize answered %d, not a redirect to the callback", res.StatusCode)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), mimictest.Callback) {
		r.t.Fatalf("authorize redirected to %q", res.Header.Get("Location"))
	}
	return loc.Query()
}

// token redeems a code and returns the status and the body.
func (r *rp) token(form url.Values, secret string) (int, map[string]any) {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.issuer+"/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(r.client, secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body
}

func (r *rp) redeem(code string) map[string]any {
	r.t.Helper()
	status, body := r.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {mimictest.Callback}, "code_verifier": {r.verifier}}, mimictest.ClientSecret)
	if status != http.StatusOK {
		r.t.Fatalf("token answered %d: %v", status, body)
	}
	return body
}

// claims verifies a token with the issuer's key set, as a relying party does, and returns its claims.
func (r *rp) claims(raw, typ string) map[string]any {
	r.t.Helper()
	var set jose.JSONWebKeySet
	getJSON(r.t, r.issuer+"/jwks", &set)
	tok, err := jose.ParseSignedCompact(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		r.t.Fatal(err)
	}
	if got := tok.Signatures[0].Protected.ExtraHeaders[jose.HeaderType]; got != typ {
		r.t.Fatalf("typ %v, want %s", got, typ)
	}
	keys := set.Key(tok.Signatures[0].Protected.KeyID)
	if len(keys) != 1 {
		r.t.Fatalf("the key set has no key of the token's kid")
	}
	b, err := tok.Verify(keys[0].Key)
	if err != nil {
		r.t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(b, &c); err != nil {
		r.t.Fatal(err)
	}
	return c
}

// signIn runs the whole flow for an identity and returns the ID token's claims.
func (r *rp) signIn(b *http.Client, hint string) map[string]any {
	r.t.Helper()
	q := r.authorize(b, func(q url.Values) {
		if hint != "" {
			q.Set("login_hint", hint)
		}
	})
	if q.Get("error") != "" {
		r.t.Fatalf("authorize refused: %s (%s)", q.Get("error"), q.Get("error_description"))
	}
	return r.claims(r.redeem(q.Get("code"))["id_token"].(string), "JWT")
}

func getJSON(t *testing.T, u string, v any) {
	t.Helper()
	res, err := http.Get(u) //nolint:gosec // a test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", u, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryOfEachProfile(t *testing.T) {
	m := mimictest.Start(t, nil)
	for _, c := range []struct{ profile, tenant string }{{"entra", "example-a"}, {"google", "example-b"}} {
		t.Run(c.profile, func(t *testing.T) {
			iss := m.Issuer(c.profile, c.tenant)
			if iss != m.URL+"/"+c.profile+"/"+c.tenant {
				t.Fatalf("issuer %s", iss)
			}
			var d map[string]any
			getJSON(t, iss+"/.well-known/openid-configuration", &d)
			for k, want := range map[string]string{
				"issuer": iss, "authorization_endpoint": iss + "/authorize", "token_endpoint": iss + "/token",
				"userinfo_endpoint": iss + "/userinfo", "jwks_uri": iss + "/jwks",
			} {
				if d[k] != want {
					t.Errorf("%s = %v, want %s", k, d[k], want)
				}
			}
			if got := d["code_challenge_methods_supported"]; !equal(got, []string{"S256"}) {
				t.Errorf("code_challenge_methods_supported = %v, want S256 alone", got)
			}
			if d["authorization_response_iss_parameter_supported"] != true {
				t.Errorf("the iss parameter is not advertised")
			}
			var set jose.JSONWebKeySet
			getJSON(t, iss+"/jwks", &set)
			if len(set.Keys) != 1 || set.Keys[0].KeyID == "" || set.Keys[0].IsPublic() == false {
				t.Fatalf("the key set is not one public key with a kid")
			}
		})
	}
	// An issuer that the config does not declare does not exist.
	res, err := http.Get(m.URL + "/entra/example-b/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("an undeclared issuer answered %d", res.StatusCode)
	}
}

func equal(got any, want []string) bool {
	l, ok := got.([]any)
	if !ok {
		return false
	}
	s := make([]string, 0, len(l))
	for _, v := range l {
		x, ok := v.(string)
		if !ok {
			return false
		}
		s = append(s, x)
	}
	return slices.Equal(s, want)
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestEntraClaims(t *testing.T) {
	m := mimictest.Start(t, nil)
	r := newRP(t, m, "entra", "example-a")
	c := r.signIn(m.HandOff("person-1", []string{actA}), "bob")
	for k, want := range map[string]any{
		"iss": r.issuer, "aud": "rp-example-a", "email": "bob.builder@example-a.test", "preferred_username": "bob.builder@example-a.test",
		"name": "Bob Example", "ver": "2.0", "nonce": "n-1",
	} {
		if c[k] != want {
			t.Errorf("%s = %v, want %v", k, c[k], want)
		}
	}
	if !equal(c["groups"], []string{"staff"}) {
		t.Errorf("groups = %v", c["groups"])
	}
	if act, _ := c["act"].(map[string]any); act["sub"] != "person-1" || len(act) != 1 {
		t.Errorf("act = %v, want {sub: person-1}", c["act"])
	}
	for _, k := range []string{"tid", "oid"} {
		if s, _ := c[k].(string); !uuidRE.MatchString(s) {
			t.Errorf("%s = %v, not a UUID", k, c[k])
		}
	}
	for _, k := range []string{"hd", "email_verified"} {
		if _, ok := c[k]; ok {
			t.Errorf("an Entra token carries Google's %s", k)
		}
	}
	if exp, iat := c["exp"].(float64), c["iat"].(float64); exp-iat != 600 {
		t.Errorf("lifetime %v s", exp-iat)
	}
}

func TestGoogleClaims(t *testing.T) {
	m := mimictest.Start(t, nil)
	r := newRP(t, m, "google", "example-b")
	c := r.signIn(m.HandOff("person-1", []string{actB}), "alice")
	for k, want := range map[string]any{
		"iss": r.issuer, "email": "alice@example-b.test", "hd": "example-b.test", "email_verified": true, "name": "Alice Example",
	} {
		if c[k] != want {
			t.Errorf("%s = %v, want %v", k, c[k], want)
		}
	}
	if !equal(c["groups"], []string{"staff", "admins"}) {
		t.Errorf("groups = %v", c["groups"])
	}
	if act, _ := c["act"].(map[string]any); act["sub"] != "person-1" {
		t.Errorf("act = %v", c["act"])
	}
	for _, k := range []string{"tid", "oid", "ver"} {
		if _, ok := c[k]; ok {
			t.Errorf("a Google token carries Entra's %s", k)
		}
	}
}

// The subject is a keyed hash of the sluis subject, the tenant and the identity: the same for the same three, on
// every sign-in and every start with the same secret, and different when any of them changes.
func TestTheSubjectIsStableAndKeyed(t *testing.T) {
	m := mimictest.Start(t, nil)
	both := []string{actA, actB}
	sub := func(sluisSub, profile, tenant, user string) string {
		return newRP(t, m, profile, tenant).signIn(m.HandOff(sluisSub, both), user)["sub"].(string)
	}
	first := sub("person-1", "entra", "example-a", "alice")
	if again := sub("person-1", "entra", "example-a", "alice"); again != first {
		t.Fatalf("the same person and identity got another sub")
	}
	for name, other := range map[string]string{
		"another person":   sub("person-2", "entra", "example-a", "alice"),
		"another identity": sub("person-1", "entra", "example-a", "bob"),
		"another tenant":   sub("person-1", "google", "example-b", "alice"),
	} {
		if other == first {
			t.Errorf("%s got the same sub", name)
		}
	}
	if strings.Contains(first, "person-1") || len(first) != 43 {
		t.Errorf("sub %q is not a 32-byte hash", first)
	}

	// A second mimic with the same secret gives the same subject; with another secret, another one.
	same := mimictest.Start(t, func(c *mimic.Config) { c.Keys.Secret = m.Config.Keys.Secret })
	if got := newRP(t, same, "entra", "example-a").signIn(same.HandOff("person-1", both), "alice")["sub"]; got != first {
		t.Errorf("the same secret gave another sub")
	}
	other := mimictest.Start(t, nil)
	if got := newRP(t, other, "entra", "example-a").signIn(other.HandOff("person-1", both), "alice")["sub"]; got == first {
		t.Errorf("another secret gave the same sub")
	}
}

func TestTheActAsGroupDecidesWhoMayUseATenant(t *testing.T) {
	m := mimictest.Start(t, nil)
	a := newRP(t, m, "entra", "example-a")
	for name, groups := range map[string][]string{
		"no groups":                {},
		"the other tenant's group": {actB},
		"a group of another role":  {"devel:shared:keycloak:example-a:viewer"},
		"the group in another env": {"stage:shared:keycloak:example-a:act-as"},
	} {
		t.Run(name, func(t *testing.T) {
			q := a.authorize(m.HandOff("person-1", groups), func(q url.Values) { q.Set("login_hint", "alice") })
			if q.Get("error") != "access_denied" || q.Get("code") != "" || q.Get("state") != "st-1" || q.Get("iss") != a.issuer {
				t.Fatalf("not refused with access_denied: %v", q)
			}
		})
	}
	t.Run("an identity the tenant does not have", func(t *testing.T) {
		q := a.authorize(m.HandOff("person-1", []string{actA}), func(q url.Values) { q.Set("login_hint", "mallory") })
		if q.Get("error") != "access_denied" || q.Get("code") != "" {
			t.Fatalf("not refused: %v", q)
		}
	})
	t.Run("no login_hint in a tenant of two identities", func(t *testing.T) {
		q := a.authorize(m.HandOff("person-1", []string{actA}), nil)
		if q.Get("error") != "login_required" || q.Get("code") != "" {
			t.Fatalf("not refused: %v", q)
		}
	})
	t.Run("a configured act-as group replaces the default", func(t *testing.T) {
		m := mimictest.Start(t, func(c *mimic.Config) { c.Tenants[0].ActAs = "devel:testers:mimic:act-as" })
		a := newRP(t, m, "entra", "example-a")
		if q := a.authorize(m.HandOff("person-1", []string{actA}), func(q url.Values) { q.Set("login_hint", "alice") }); q.Get("error") != "access_denied" {
			t.Fatalf("the default group was still taken: %v", q)
		}
		a.signIn(m.HandOff("person-1", []string{"devel:testers:mimic:act-as"}), "alice")
	})
	t.Run("one identity needs no login_hint", func(t *testing.T) {
		m := mimictest.Start(t, func(c *mimic.Config) { c.Tenants[0].Identities = c.Tenants[0].Identities[:1] })
		if c := newRP(t, m, "entra", "example-a").signIn(m.HandOff("person-1", []string{actA}), ""); c["name"] != "Alice Example" {
			t.Fatalf("name %v", c["name"])
		}
	})
}

func TestPKCE(t *testing.T) {
	m := mimictest.Start(t, nil)
	r := newRP(t, m, "entra", "example-a")
	b := m.HandOff("person-1", []string{actA})
	for name, edit := range map[string]func(url.Values){
		"no challenge":      func(q url.Values) { q.Del("code_challenge"); q.Del("code_challenge_method") },
		"plain":             func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"no method":         func(q url.Values) { q.Del("code_challenge_method") },
		"a short value":     func(q url.Values) { q.Set("code_challenge", "abc") },
		"not response=code": func(q url.Values) { q.Set("response_type", "token") },
		"no openid scope":   func(q url.Values) { q.Set("scope", "email") },
	} {
		t.Run("authorize refuses "+name, func(t *testing.T) {
			q := r.authorize(b, func(q url.Values) { q.Set("login_hint", "alice"); edit(q) })
			if q.Get("error") == "" || q.Get("code") != "" {
				t.Fatalf("not refused: %v", q)
			}
		})
	}
	code := func() string {
		q := r.authorize(b, func(q url.Values) { q.Set("login_hint", "alice") })
		return q.Get("code")
	}
	good := func(c string) url.Values {
		return url.Values{"grant_type": {"authorization_code"}, "code": {c}, "redirect_uri": {mimictest.Callback}, "code_verifier": {r.verifier}}
	}
	for name, edit := range map[string]func(url.Values){
		"another verifier":     func(f url.Values) { f.Set("code_verifier", rand.Text()+rand.Text()) },
		"no verifier":          func(f url.Values) { f.Del("code_verifier") },
		"another redirect_uri": func(f url.Values) { f.Set("redirect_uri", "http://127.0.0.1:9/other") },
	} {
		t.Run("token refuses "+name, func(t *testing.T) {
			c := code()
			f := good(c)
			edit(f)
			if status, body := r.token(f, mimictest.ClientSecret); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
				t.Fatalf("answered %d %v", status, body)
			}
			// The failed attempt used the code up.
			if status, _ := r.token(good(c), mimictest.ClientSecret); status != http.StatusBadRequest {
				t.Fatalf("the code still works after a failed attempt")
			}
		})
	}
	t.Run("a code is used once", func(t *testing.T) {
		c := code()
		r.redeem(c)
		if status, body := r.token(good(c), mimictest.ClientSecret); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("answered %d %v", status, body)
		}
	})
	t.Run("a wrong client secret", func(t *testing.T) {
		if status, body := r.token(good(code()), "wrong-secret"); status != http.StatusUnauthorized || body["error"] != "invalid_client" {
			t.Fatalf("answered %d %v", status, body)
		}
	})
	t.Run("another tenant's token endpoint", func(t *testing.T) {
		other := newRP(t, m, "google", "example-b")
		other.client, other.verifier = r.client, r.verifier
		if status, _ := other.token(good(code()), mimictest.ClientSecret); status == http.StatusOK {
			t.Fatalf("a code of example-a was redeemed at example-b")
		}
	})
	t.Run("an unregistered redirect_uri is not followed", func(t *testing.T) {
		res, err := b.Get(r.issuer + "/authorize?" + url.Values{"client_id": {r.client}, "redirect_uri": {"https://attacker.example.com/cb"}}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("answered %d", res.StatusCode)
		}
	})
}

func TestUserinfo(t *testing.T) {
	m := mimictest.Start(t, nil)
	r := newRP(t, m, "google", "example-b")
	q := r.authorize(m.HandOff("person-1", []string{actB}), func(q url.Values) { q.Set("login_hint", "alice") })
	body := r.redeem(q.Get("code"))
	at := r.claims(body["access_token"].(string), "at+jwt")
	req, _ := http.NewRequest(http.MethodGet, r.issuer+"/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+body["access_token"].(string))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var u map[string]any
	_ = json.NewDecoder(res.Body).Decode(&u)
	if res.StatusCode != http.StatusOK || u["sub"] != at["sub"] || u["email"] != "alice@example-b.test" || u["hd"] != "example-b.test" {
		t.Fatalf("userinfo %d %v", res.StatusCode, u)
	}
	// The ID token is not an access token.
	req.Header.Set("Authorization", "Bearer "+body["id_token"].(string))
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an ID token was taken as an access token: %d", res2.StatusCode)
	}
}

// A person signs in at the stable sluis: authorize sends them to /login, /login to the stable sluis with PKCE, and the
// callback back to the authorize request, which now has a session.
func TestAPersonSignsInThroughTheStableSluis(t *testing.T) {
	m := mimictest.Start(t, func(c *mimic.Config) { c.Sluis.RequireAMR = []string{"hwk", "mfa"} })
	r := newRP(t, m, "entra", "example-a")
	m.Sluis.SignInNext(mimictest.Person{Sub: "person-7", Groups: []string{actA}, AMR: []string{"hwk"}})
	c := r.signIn(m.Browser(), "alice")
	if act, _ := c["act"].(map[string]any); act["sub"] != "person-7" {
		t.Fatalf("act = %v", c["act"])
	}

	t.Run("a sign-in without the required amr is refused", func(t *testing.T) {
		m.Sluis.SignInNext(mimictest.Person{Sub: "person-8", Groups: []string{actA}, AMR: []string{"pwd"}})
		q := url.Values{
			"response_type": {"code"}, "client_id": {r.client}, "redirect_uri": {mimictest.Callback}, "scope": {"openid"},
			"code_challenge": {challenge(r.verifier)}, "code_challenge_method": {"S256"}, "login_hint": {"alice"},
		}
		res, err := m.Browser().Get(r.issuer + "/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("answered %d", res.StatusCode)
		}
	})
	t.Run("prompt=none without a session", func(t *testing.T) {
		q := r.authorize(m.Browser(), func(q url.Values) { q.Set("prompt", "none") })
		if q.Get("error") != "login_required" {
			t.Fatalf("answered %v", q)
		}
	})
	t.Run("login sends nobody to another host", func(t *testing.T) {
		for _, next := range []string{"https://attacker.example.com/", "//attacker.example.com/", "/\\attacker.example.com"} {
			res, err := m.Browser().Get(m.URL + "/login?" + url.Values{"next": {next}}.Encode())
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("next %q answered %d", next, res.StatusCode)
			}
		}
	})
	t.Run("a callback without the sign-in cookie", func(t *testing.T) {
		res, err := http.Get(m.URL + "/callback?code=x&state=y")
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("answered %d", res.StatusCode)
		}
	})
}

// A CI job hands over its token of the stable sluis; mimic takes only a token of that sluis, for mimic's audience,
// in its time window.
func TestTheHandOffOfACIToken(t *testing.T) {
	m := mimictest.Start(t, nil)
	post := func(tok string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, m.URL+"/session", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res
	}
	ci := "spiffe://example.com/ci/github/acme/app"
	good := m.Sluis.Token(ci, []string{actA}, mimictest.SluisClientID, "at+jwt", nil)
	res := post(good)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("a good token answered %d", res.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == mimic.SessionCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("no HttpOnly, SameSite=Lax session cookie")
	}

	other, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	for name, tok := range map[string]string{
		"no token":             "",
		"another audience":     m.Sluis.Token(ci, []string{actA}, "someone-else", "at+jwt", nil),
		"another issuer":       m.Sluis.Token(ci, []string{actA}, mimictest.SluisClientID, "at+jwt", map[string]any{"iss": "https://sluis.example.com"}),
		"expired":              m.Sluis.Token(ci, []string{actA}, mimictest.SluisClientID, "at+jwt", map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}),
		"no exp":               m.Sluis.Token(ci, []string{actA}, mimictest.SluisClientID, "at+jwt", map[string]any{"exp": nil}),
		"no sub":               m.Sluis.Token("", []string{actA}, mimictest.SluisClientID, "at+jwt", nil),
		"another key":          mimictest.Sign(t, other, jose.ES384, mimictest.SluisKID, "at+jwt", map[string]any{"iss": m.Sluis.Issuer, "sub": ci, "aud": mimictest.SluisClientID, "exp": time.Now().Add(time.Minute).Unix()}),
		"a typ of another use": m.Sluis.Token(ci, []string{actA}, mimictest.SluisClientID, "logout+jwt", nil),
		"a token of mimic itself": func() string {
			r := newRP(t, m, "entra", "example-a")
			q := r.authorize(m.HandOff(ci, []string{actA}), func(q url.Values) { q.Set("login_hint", "alice") })
			return r.redeem(q.Get("code"))["id_token"].(string)
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if res := post(tok); res.StatusCode != http.StatusUnauthorized || len(res.Cookies()) != 0 {
				t.Fatalf("answered %d with %d cookies", res.StatusCode, len(res.Cookies()))
			}
		})
	}

	t.Run("a forged session cookie is no session", func(t *testing.T) {
		r := newRP(t, m, "entra", "example-a")
		b := m.Browser()
		u, _ := url.Parse(m.URL)
		value := cookie.Value
		forged := value[:len(value)-2] + map[bool]string{true: "AA", false: "BB"}[!strings.HasSuffix(value, "AA")]
		b.Jar.SetCookies(u, []*http.Cookie{{Name: mimic.SessionCookie, Value: forged, HttpOnly: true, SameSite: http.SameSiteLaxMode}}) //nolint:gosec // a test client over loopback http
		// With no valid session, authorize sends the browser to the stable sluis, whose fake signs in nobody useful.
		m.Sluis.SignInNext(mimictest.Person{Sub: "nobody"})
		if q := r.authorize(b, func(q url.Values) { q.Set("login_hint", "alice") }); q.Get("error") != "access_denied" {
			t.Fatalf("the forged cookie was taken: %v", q)
		}
	})
}

// The hand-off's verifier refuses every vector of the JWT best current practices (RFC 8725) that applies to it.
func TestTheHandOffRefusesEveryVectorOfTheJWTBestPractices(t *testing.T) {
	m := mimictest.Start(t, nil)
	spare, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := jwtbcp.Profile{
		Alg: jose.ES384, Key: m.Sluis.Key, KID: mimictest.SluisKID, SpareRSA: spare, Now: time.Now(),
		Claims: map[string]any{
			"iss": m.Sluis.Issuer, "sub": "spiffe://example.com/ci/github/acme/app", "aud": mimictest.SluisClientID,
			"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(), "groups": []string{actA},
		},
		IssuerClaim: "iss", AudienceClaim: "aud", CheckExp: true,
	}
	post := func(tok string) int {
		req, _ := http.NewRequest(http.MethodPost, m.URL+"/session", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		return res.StatusCode
	}
	if got := post(jwtbcp.Valid(t, p)); got != http.StatusNoContent {
		t.Fatalf("the valid token was refused: %d", got)
	}
	n := 0
	for _, v := range jwtbcp.Negative(t, p) {
		n++
		t.Run(string(v.Rule)+"/"+v.Name, func(t *testing.T) {
			if got := post(v.Token); got != http.StatusUnauthorized {
				t.Fatalf("accepted a token with this defect: %d", got)
			}
		})
	}
	if n < 30 {
		t.Fatalf("only %d vectors ran", n)
	}
}

func TestMimicRefusesToStartOutsideDevel(t *testing.T) {
	for _, env := range []string{"", "stage", "prod", "dev", "DEVEL", "devel "} {
		t.Run(env, func(t *testing.T) {
			c := mimictest.Config(t, "http://127.0.0.1:9", "http://127.0.0.1:9")
			c.Environment = env
			if _, err := mimic.New(c, mimic.Options{}); err == nil || !strings.Contains(err.Error(), `"devel" only`) {
				t.Fatalf("started with environment %q: %v", env, err)
			}
		})
	}
}

func TestTheConfigRefusesARealDomainAndOtherMistakes(t *testing.T) {
	for name, edit := range map[string]func(*mimic.Config){
		"a real domain":              func(c *mimic.Config) { c.Tenants[0].Domain = "example-a.com" },
		"a domain that only ends so": func(c *mimic.Config) { c.Tenants[0].Domain = "example-a.test.com" },
		"a bare test name":           func(c *mimic.Config) { c.Tenants[0].Domain = "test" },
		"an unknown profile":         func(c *mimic.Config) { c.Tenants[0].Profile = "okta" },
		"two tenants on one path":    func(c *mimic.Config) { c.Tenants[1] = c.Tenants[0] },
		"two identities of one name": func(c *mimic.Config) { c.Tenants[0].Identities[1].Username = "alice" },
		"no identities":              func(c *mimic.Config) { c.Tenants[0].Identities = nil },
		"public url over http":       func(c *mimic.Config) { c.PublicURL = "http://mimic.example.com" },
		"sluis over http":            func(c *mimic.Config) { c.Sluis.Issuer = "http://sluis.example.com" },
		"a redirect over http":       func(c *mimic.Config) { c.Tenants[0].Clients[0].RedirectURIs = []string{"http://rp.example.com/cb"} },
		"a key that is not a source": func(c *mimic.Config) { c.Keys.Signing = "/etc/mimic/key.pem" },
		"an unknown algorithm":       func(c *mimic.Config) { c.Sluis.Algorithms = []string{"HS256"} },
		"a short secret": func(c *mimic.Config) {
			c.Keys.Secret = "env:MIMIC_TEST_SHORT_SECRET"
			t.Setenv("MIMIC_TEST_SHORT_SECRET", "too-short")
		},
		"a missing env source": func(c *mimic.Config) { c.Keys.Secret = "env:MIMIC_TEST_NOT_SET_ANYWHERE" },
	} {
		t.Run(name, func(t *testing.T) {
			c := mimictest.Config(t, "http://127.0.0.1:9", "http://127.0.0.1:9")
			edit(c)
			if _, err := mimic.New(c, mimic.Options{}); err == nil {
				t.Fatalf("started")
			}
		})
	}
}

func TestParseConfig(t *testing.T) {
	doc := `
environment: devel
public_url: https://mimic.devel.example.com
keys: {signing: "file:/run/mimic/signing.pem", secret: "env:MIMIC_SECRET"}
sluis: {issuer: "https://sluis.example.com", client_id: mimic}
tenants:
  - name: example-a
    profile: entra
    domain: example-a.test
    clients: [{id: idp-staff-example-a, redirect_uris: ["https://idp.devel.example.com/realms/staff-example-a/broker/mimic/endpoint"]}]
    identities: [{username: alice, name: Alice Example, groups: [staff]}]
`
	c, err := mimic.ParseConfig([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if c.Tenants[0].ActAs != actA || c.Sluis.Audience != "mimic" || !slices.Equal(c.Sluis.Algorithms, []string{"ES384"}) ||
		c.SessionTTL != time.Hour || c.Tenants[0].Identities[0].Email != "alice" {
		t.Fatalf("the defaults are not applied: %+v", c)
	}
	if _, err := mimic.ParseConfig([]byte(doc + "unknown_key: 1\n")); err == nil {
		t.Fatalf("an unknown key was taken")
	}
	if _, err := mimic.ParseConfig([]byte(strings.Replace(doc, "environment: devel", "environment: prod", 1))); err == nil {
		t.Fatalf("a prod config parsed")
	}
}

func TestTheExampleConfigParses(t *testing.T) {
	c, err := mimic.LoadConfig("mimic.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tenants) != 2 || c.Tenants[0].ActAs != actA || c.Tenants[1].Profile != mimic.ProfileGoogle {
		t.Fatalf("the example is not the one documented: %+v", c.Tenants)
	}
}
