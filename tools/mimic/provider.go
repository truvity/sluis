package mimic

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// tokenTTL is the lifetime of an ID token and an access token; codeTTL that of an authorization code.
const (
	tokenTTL = 10 * time.Minute
	codeTTL  = time.Minute
)

// tenant is one issuer.
type tenant struct {
	s       *Server
	cfg     *Tenant
	issuer  string
	secrets map[string]string // client id -> secret; absent for a public client
}

// grant is an authorization code: what it was issued for, and to whom.
type grant struct {
	tenant    *tenant
	client    string
	redirect  string
	challenge string
	nonce     string
	scope     string
	identity  *Identity
	actor     *session
	exp       time.Time
}

func newTenant(s *Server, c *Tenant) (*tenant, error) {
	t := &tenant{s: s, cfg: c, issuer: s.cfg.PublicURL + c.path(), secrets: map[string]string{}}
	for _, cl := range c.Clients {
		if cl.Secret == "" {
			continue
		}
		b, err := readSource(cl.Secret)
		if err != nil {
			return nil, errors.New("clients " + cl.ID + ": " + err.Error())
		}
		t.secrets[cl.ID] = string(b)
	}
	return t, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// oauthError is an error of the token or userinfo endpoint (RFC 6749 section 5.2).
func oauthError(w http.ResponseWriter, status int, code, description string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="mimic"`)
	}
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func (t *tenant) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	claims := []string{"iss", "sub", "aud", "exp", "iat", "auth_time", "nonce", "email", "name", "groups", "act"}
	switch t.cfg.Profile {
	case ProfileEntra:
		claims = append(claims, "tid", "oid", "ver", "preferred_username")
	case ProfileGoogle:
		claims = append(claims, "hd", "email_verified")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         t.issuer,
		"authorization_endpoint":                         t.issuer + "/authorize",
		"token_endpoint":                                 t.issuer + "/token",
		"userinfo_endpoint":                              t.issuer + "/userinfo",
		"jwks_uri":                                       t.issuer + "/jwks",
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code"},
		"subject_types_supported":                        []string{"public"},
		"id_token_signing_alg_values_supported":          []string{string(t.s.keys.alg)},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"client_secret_basic", "client_secret_post", "none"},
		"scopes_supported":                               []string{"openid", "email", "profile"},
		"claims_supported":                               claims,
		"authorization_response_iss_parameter_supported": true,
	})
}

func (t *tenant) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{t.s.keys.public}})
}

func (t *tenant) client(id string) *Client {
	for i := range t.cfg.Clients {
		if t.cfg.Clients[i].ID == id {
			return &t.cfg.Clients[i]
		}
	}
	return nil
}

func (t *tenant) identity(username string) *Identity {
	for i := range t.cfg.Identities {
		if t.cfg.Identities[i].Username == username {
			return &t.cfg.Identities[i]
		}
	}
	return nil
}

// handleAuthorize is the authorization endpoint: the code flow with PKCE S256 only. The client and the redirect URI
// are checked first, and an error before that is shown here, never sent to an unchecked address (RFC 6749 section
// 4.1.2.1). Without a session, the person signs in at the stable sluis first. The identity is the login_hint, or the
// tenant's one identity, or the one the person picks on the chooser (chooser.go); the session's groups must hold the
// tenant's act-as group.
func (t *tenant) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cl := t.client(q.Get("client_id"))
	if cl == nil {
		http.Error(w, "client_id: not a client of this issuer", http.StatusBadRequest)
		return
	}
	redirect := q.Get("redirect_uri")
	if !slices.Contains(cl.RedirectURIs, redirect) {
		http.Error(w, "redirect_uri: not registered for the client", http.StatusBadRequest)
		return
	}
	state := q.Get("state")
	fail := func(code, description string) {
		v := url.Values{"error": {code}, "error_description": {description}, "iss": {t.issuer}}
		if state != "" {
			v.Set("state", state)
		}
		http.Redirect(w, r, withQuery(redirect, v), http.StatusSeeOther) //nolint:gosec // redirect is one of the client's registered URIs, checked above
	}
	switch {
	case q.Get("response_type") != "code":
		fail("unsupported_response_type", "response_type: code")
		return
	case !slices.Contains(strings.Fields(q.Get("scope")), "openid"):
		fail("invalid_scope", "scope: openid is required")
		return
	case q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43:
		fail("invalid_request", "PKCE: an S256 code_challenge is required")
		return
	}
	ses := t.s.sessionOf(r)
	if ses == nil {
		if q.Get("prompt") == "none" {
			fail("login_required", "no session at mimic")
			return
		}
		http.Redirect(w, r, "/login?"+url.Values{"next": {r.URL.RequestURI()}}.Encode(), http.StatusSeeOther)
		return
	}
	if !slices.Contains(ses.Groups, t.cfg.ActAs) {
		t.s.log.Info("mimic: refused, the subject may not act in this tenant", "tenant", t.cfg.path(), "sluis_sub", ses.Sub)
		fail("access_denied", "the subject may not act as an identity of this tenant")
		return
	}
	var id *Identity
	switch hint := q.Get("login_hint"); {
	case hint != "":
		id = t.identity(hint)
		if id == nil {
			fail("access_denied", "login_hint: no such identity in this tenant")
			return
		}
	case len(t.cfg.Identities) == 1:
		id = &t.cfg.Identities[0]
	case q.Get("prompt") == "none":
		// A silent request cannot show the chooser. The person is signed in, so this is not login_required; the
		// request lacks the choice of an account (OIDC Core section 3.1.2.6).
		fail("account_selection_required", "login_hint: name the identity")
		return
	default:
		t.showChooser(w, r, ses)
		return
	}
	code := random(32)
	g := &grant{
		tenant: t, client: cl.ID, redirect: redirect, challenge: q.Get("code_challenge"), nonce: q.Get("nonce"),
		scope: q.Get("scope"), identity: id, actor: ses, exp: t.s.now().Add(codeTTL),
	}
	t.s.mu.Lock()
	for k, old := range t.s.codes {
		if t.s.now().After(old.exp) {
			delete(t.s.codes, k)
		}
	}
	t.s.codes[code] = g
	t.s.mu.Unlock()
	t.s.log.Info("mimic: code issued", "tenant", t.cfg.path(), "identity", id.Username, "sluis_sub", ses.Sub, "via", ses.Via)
	v := url.Values{"code": {code}, "iss": {t.issuer}}
	if state != "" {
		v.Set("state", state)
	}
	http.Redirect(w, r, withQuery(redirect, v), http.StatusSeeOther) //nolint:gosec // redirect is one of the client's registered URIs, checked above
}

func withQuery(u string, v url.Values) string {
	if strings.Contains(u, "?") {
		return u + "&" + v.Encode()
	}
	return u + "?" + v.Encode()
}

// authenticate reads the client of a token request: client_secret_basic, client_secret_post, or none for a public
// client. It returns the client id, or "" when the client is not authenticated.
func (t *tenant) authenticate(r *http.Request) string {
	id, secret, basic := r.BasicAuth()
	if basic {
		var err1, err2 error
		id, err1 = url.QueryUnescape(id)
		secret, err2 = url.QueryUnescape(secret)
		if err1 != nil || err2 != nil {
			return ""
		}
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if t.client(id) == nil {
		return ""
	}
	want, confidential := t.secrets[id]
	switch {
	case !confidential && secret == "":
		return id
	case confidential && subtle.ConstantTimeCompare([]byte(secret), []byte(want)) == 1:
		return id
	}
	return ""
}

// handleToken is the token endpoint: one authorization code, once, for the client, the redirect URI and the PKCE
// verifier it was issued for.
func (t *tenant) handleToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "the form does not parse")
		return
	}
	client := t.authenticate(r)
	if client == "" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "the client is not authenticated")
		return
	}
	f := r.PostForm
	if f.Get("grant_type") != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type: authorization_code")
		return
	}
	t.s.mu.Lock()
	g := t.s.codes[f.Get("code")]
	delete(t.s.codes, f.Get("code")) // a code is used once, even when this request fails
	t.s.mu.Unlock()
	switch {
	case g == nil || g.tenant != t || t.s.now().After(g.exp):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code: unknown, used or expired")
		return
	case g.client != client:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code: issued to another client")
		return
	case f.Get("redirect_uri") != g.redirect:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri: not the one of the code")
		return
	case f.Get("code_verifier") == "" || subtle.ConstantTimeCompare([]byte(s256(f.Get("code_verifier"))), []byte(g.challenge)) != 1:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier: does not match the challenge")
		return
	}
	now := t.s.now()
	claims := t.claims(g.identity, g.actor)
	id := map[string]any{"aud": client, "iat": now.Unix(), "exp": now.Add(tokenTTL).Unix(), "auth_time": g.actor.Auth}
	if g.nonce != "" {
		id["nonce"] = g.nonce
	}
	maps.Copy(id, claims)
	idToken, err := t.s.keys.sign("JWT", id)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "signing failed")
		return
	}
	at := map[string]any{"aud": client, "client_id": client, "iat": now.Unix(), "exp": now.Add(tokenTTL).Unix(), "jti": random(16), "scope": g.scope}
	maps.Copy(at, claims)
	access, err := t.s.keys.sign("at+jwt", at)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "signing failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(tokenTTL.Seconds()), "id_token": idToken, "scope": g.scope,
	})
}

// claims are the claims of an identity for the subject acting as it, shaped by the tenant's profile (ADR 0054, "One
// issuer per profile and tenant").
func (t *tenant) claims(id *Identity, actor *session) map[string]any {
	k := t.s.keys
	email := id.Email + "@" + t.cfg.Domain
	groups := id.Groups
	if groups == nil {
		groups = []string{}
	}
	c := map[string]any{
		"iss":    t.issuer,
		"sub":    k.subjectOf(actor.Sub, t.cfg.path(), id.Username),
		"email":  email,
		"name":   id.Name,
		"groups": groups,
		"act":    map[string]any{"sub": actor.Sub},
	}
	switch t.cfg.Profile {
	case ProfileEntra:
		c["tid"] = k.uuidOf("tid", t.cfg.path())
		c["oid"] = k.uuidOf("oid", actor.Sub, t.cfg.path(), id.Username)
		c["ver"] = "2.0"
		c["preferred_username"] = email
	case ProfileGoogle:
		c["hd"] = t.cfg.Domain
		c["email_verified"] = true
	}
	return c
}

// handleUserinfo answers for an access token of this issuer (OIDC Core section 5.3).
func (t *tenant) handleUserinfo(w http.ResponseWriter, r *http.Request) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	c, err := t.verifyAccess(strings.TrimSpace(raw))
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	for _, k := range []string{"iss", "aud", "client_id", "iat", "exp", "jti", "scope"} {
		delete(c, k)
	}
	writeJSON(w, http.StatusOK, c)
}

// verifyAccess checks an access token that this issuer signed.
func (t *tenant) verifyAccess(raw string) (map[string]any, error) {
	tok, err := jose.ParseSignedCompact(raw, []jose.SignatureAlgorithm{t.s.keys.alg})
	if err != nil || len(tok.Signatures) != 1 {
		return nil, errors.New("not a token of this issuer")
	}
	if typ, _ := tok.Signatures[0].Protected.ExtraHeaders[jose.HeaderType].(string); typ != "at+jwt" {
		return nil, errors.New("not an access token")
	}
	payload, err := tok.Verify(t.s.keys.signer.Public())
	if err != nil {
		return nil, err
	}
	var c map[string]any
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, err
	}
	exp, _ := c["exp"].(float64)
	if c["iss"] != t.issuer || t.s.now().After(unix(exp)) {
		return nil, errors.New("another issuer, or expired")
	}
	return c, nil
}
