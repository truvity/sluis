package issuer_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/policy"
)

// checksServer is the demo policy, with extra appended, behind the full
// handler and a sign-in page that signs ada in.
func checksServer(t *testing.T, extra string) *httptest.Server {
	t.Helper()
	declared, err := policy.Parse([]byte(demo.Policy + extra))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set,
		&fakeDirectory{standing: map[string]issuer.Standing{
			"ada@north.example": {Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}, GivenName: "Ada", FamilyName: "Lovelace"},
		}}, issuer.NewMemoryState())
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, lookup{"argocd": clientcreds.Secrets{Current: "right"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	h, err := handlerWithSignIn(iss, storage, issuer.SignInDeps{
		Providers: []issuer.SignIn{oneProvider{email: "ada@north.example"}},
		State:     access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

// checksCode signs ada in at local-dev and returns the browser and the code.
func checksCode(t *testing.T, s *httptest.Server, scope, resource string) (*browser, string) {
	t.Helper()
	return checksCodeAt(t, s, "local-dev", "http://localhost:8000/callback", scope, resource)
}

// checksCodeAt signs ada in at a client and returns the browser and the code.
func checksCodeAt(t *testing.T, s *httptest.Server, client, redirect, scope, resource string) (*browser, string) {
	t.Helper()
	b := newBrowser(t, s)
	sum := sha256.Sum256([]byte(pkceVerifier))
	q := url.Values{
		"client_id": {client}, "redirect_uri": {redirect},
		"response_type": {"code"}, "scope": {scope}, "state": {"x"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	if resource != "" {
		q.Set("resource", resource)
	}
	status, where, body := b.do(http.MethodGet, "/authorize?"+q.Encode())
	if status != http.StatusFound {
		t.Fatalf("authorize %d %s", status, body)
	}
	_, next, _ := b.do(http.MethodGet, where)
	if strings.Contains(next, "/login/google/start") {
		_, toProvider, _ := b.do(http.MethodGet, next)
		state := toProvider[strings.Index(toProvider, "state=")+len("state="):]
		_, next, _ = b.do(http.MethodGet, "/login/google/callback?code=x&state="+state)
	}
	for strings.HasPrefix(next, "/") {
		_, next, _ = b.do(http.MethodGet, next)
	}
	back, err := url.Parse(next)
	if err != nil || back.Query().Get("code") == "" {
		t.Fatalf("sent to %q", next)
	}
	return b, back.Query().Get("code")
}

func checksRedeem(code string) url.Values {
	return url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://localhost:8000/callback"},
		"client_id": {"local-dev"}, "code_verifier": {pkceVerifier},
	}
}

// checksPost posts a token request, with HTTP Basic when user is not empty.
func checksPost(t *testing.T, s *httptest.Server, form url.Values, user, password string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	r, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	return r.StatusCode, body
}

func checksGet(t *testing.T, s *httptest.Server, path, bearer string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	r, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	return r.StatusCode
}

func audiencesOf(t *testing.T, token string) []string {
	t.Helper()
	switch aud := payloadOf(t, token)["aud"].(type) {
	case string:
		return []string{aud}
	case []any:
		out := make([]string, 0, len(aud))
		for _, a := range aud {
			s, _ := a.(string)
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}

// An ID token is for the client that asked for it. The resource a request
// named is the access token's audience and never the ID token's, and an ID
// token is not a bearer at `/.access/grants`, which reads access tokens only.
func TestAnIDTokenIsForTheClientAloneAndIsNoBearer(t *testing.T) {
	t.Parallel()
	s := checksServer(t, "resources:\n  'https://api.example/': { requires: [devel:k8s:viewer] }\n")
	_, code := checksCode(t, s, "openid email", "https://api.example/")
	form := checksRedeem(code)
	form.Set("resource", "https://api.example/")
	status, body := checksPost(t, s, form, "", "")
	if status != http.StatusOK {
		t.Fatalf("redeem %d %v", status, body)
	}
	id, _ := body["id_token"].(string)
	access, _ := body["access_token"].(string)

	if got := audiencesOf(t, id); !slices.Equal(got, []string{"local-dev"}) {
		t.Errorf("ID token aud = %v, want the client alone", got)
	}
	if got := audiencesOf(t, access); !slices.Equal(got, []string{"https://api.example/"}) {
		t.Errorf("access token aud = %v, want the resource", got)
	}

	if got := checksGet(t, s, issuer.GrantsPath, id); got != http.StatusUnauthorized {
		t.Errorf("an ID token at grants = %d, want 401", got)
	}
	if got := checksGet(t, s, issuer.GrantsPath, access); got != http.StatusOK {
		t.Errorf("the access token at grants = %d, want 200", got)
	}
}

// Signing out stops the access tokens minted under the sign-in at the
// endpoints that hold their record, with a session (`offline_access`) and
// without one (`openid` alone). The token's signature and `exp` are still
// good; what ended is what it was issued under.
func TestSigningOutStopsTheTokensAtGrantsAndUserinfo(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"openid", "openid offline_access"} {
		s := checksServer(t, "")
		b, code := checksCode(t, s, scope, "")
		status, body := checksPost(t, s, checksRedeem(code), "", "")
		if status != http.StatusOK {
			t.Fatalf("[%s] redeem %d %v", scope, status, body)
		}
		access, _ := body["access_token"].(string)
		if u, g := checksGet(t, s, "/userinfo", access), checksGet(t, s, issuer.GrantsPath, access); u != http.StatusOK || g != http.StatusOK {
			t.Fatalf("[%s] before signing out: userinfo %d, grants %d", scope, u, g)
		}

		b.logout()

		if exp, _ := payloadOf(t, access)["exp"].(float64); time.Unix(int64(exp), 0).Before(time.Now()) {
			t.Fatalf("[%s] the token expired during the test", scope)
		}
		if got := checksGet(t, s, "/userinfo", access); got != http.StatusUnauthorized {
			t.Errorf("[%s] userinfo after signing out = %d, want 401", scope, got)
		}
		if got := checksGet(t, s, issuer.GrantsPath, access); got != http.StatusUnauthorized {
			t.Errorf("[%s] grants after signing out = %d, want 401", scope, got)
		}
	}
}

// A revoked access token's record is gone, and the grants endpoint reads the
// record: revocation reaches it at once rather than at `exp`.
func TestARevokedAccessTokenIsRefusedAtGrants(t *testing.T) {
	t.Parallel()
	s := checksServer(t, "")
	_, code := checksCode(t, s, "openid offline_access", "")
	status, body := checksPost(t, s, checksRedeem(code), "", "")
	if status != http.StatusOK {
		t.Fatalf("redeem %d %v", status, body)
	}
	access, _ := body["access_token"].(string)
	if got := checksGet(t, s, issuer.GrantsPath, access); got != http.StatusOK {
		t.Fatalf("grants before revoking = %d", got)
	}
	jti, _ := payloadOf(t, access)["jti"].(string)
	revoke, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL+"/revoke",
		strings.NewReader(url.Values{"token": {jti}, "client_id": {"local-dev"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	revoke.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, err := s.Client().Do(revoke)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d", r.StatusCode)
	}
	if got := checksGet(t, s, issuer.GrantsPath, access); got != http.StatusUnauthorized {
		t.Errorf("grants after revoking = %d, want 401", got)
	}
}

// The client secrets endpoint admits the access token of a person's live
// sign-in at the CLI or the console, and nothing else this issuer signed: not
// the ID token of that same sign-in, and not its access token once the session
// has ended, although the token has not expired.
func TestTheClientSecretsEndpointNeedsALiveSignIn(t *testing.T) {
	t.Parallel()
	declared, err := policy.Parse([]byte("version: 1\n" +
		"groups:\n  " + policy.GroupOperators + ": { members: [ops@north.example] }\n" +
		"clients:\n" +
		"  accessctl: { kind: public, requires: [" + policy.GroupOperators + "], redirects: ['http://127.0.0.1/cb'] }\n" +
		"  console:   { kind: public, requires: [" + policy.GroupOperators + "], redirects: ['https://console.example/cb'] }\n" +
		"  grafana:   { kind: public, requires: [" + policy.GroupOperators + "], redirects: ['https://grafana.example/cb'] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	dir := &fakeDirectory{standing: map[string]issuer.Standing{"ada@north.example": live("ops@north.example")}}
	iss := issuer.New(issuer.Config{URL: "https://issuer.example", TokenLifetime: time.Hour}, set, dir, issuer.NewMemoryState())
	store := memory.NewSecrets()
	clientcreds.Reconcile(context.Background(), []string{"grafana"}, store, nil, time.Now(), nil, clientcreds.Hooks{})
	iss.UseClientSecrets(&clientcreds.Manager{Store: store, Generated: func(id string) bool { return id == "grafana" }})
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := handlerWithSignIn(iss, storage, issuer.SignInDeps{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	show := func(bearer string) int {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+issuer.ClientSecretsPath+"/show", strings.NewReader(`{"client":"grafana"}`))
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	tokens := signedInTokens(t, server, iss, "accessctl")
	access, _ := tokens["access_token"].(string)
	id, _ := tokens["id_token"].(string)
	if id == "" {
		t.Fatal("the sign-in returned no ID token")
	}
	if got := show(access); got != http.StatusOK {
		t.Fatalf("the access token of a live sign-in = %d, want 200", got)
	}
	if got := show(id); got != http.StatusUnauthorized {
		t.Errorf("the ID token of that sign-in = %d, want 401", got)
	}

	session, found, err := iss.Sessions().ByRefreshToken(t.Context(), "refresh-accessctl")
	if err != nil || !found {
		t.Fatalf("the session: %v %v", found, err)
	}
	if _, err = iss.Sessions().RevokeID(t.Context(), session.ID); err != nil {
		t.Fatal(err)
	}
	// The window this closes: without the record check the token would have
	// been good here until its exp, the client's lifetime after it was minted.
	claims := payloadOf(t, access)
	exp, _ := claims["exp"].(float64)
	iat, _ := claims["iat"].(float64)
	if left := time.Until(time.Unix(int64(exp), 0)); left < 50*time.Minute || time.Duration(exp-iat)*time.Second > time.Hour+time.Minute {
		t.Fatalf("the token has %v left of %v; the test assumes a one-hour lifetime", left, time.Duration(exp-iat)*time.Second)
	}
	if got := show(access); got != http.StatusUnauthorized {
		t.Errorf("the access token after its session ended = %d, want 401", got)
	}
}

// A public client authenticates with nothing, so a secret it presents is a
// mistake to refuse rather than a credential to ignore: in the body or in HTTP
// Basic, at the code and at the refresh grant alike.
func TestAPublicClientPresentingASecretIsRefused(t *testing.T) {
	t.Parallel()
	s := checksServer(t, "")

	_, code := checksCode(t, s, "openid offline_access", "")
	form := checksRedeem(code)
	form.Set("client_secret", "not-a-secret")
	if status, body := checksPost(t, s, form, "", ""); body["error"] != "invalid_client" {
		t.Errorf("code grant with a secret in the body = %d %v, want invalid_client", status, body)
	}

	_, code = checksCode(t, s, "openid offline_access", "")
	form = checksRedeem(code)
	form.Del("client_id")
	if status, body := checksPost(t, s, form, "local-dev", "not-a-secret"); body["error"] != "invalid_client" {
		t.Errorf("code grant with a secret in Basic = %d %v, want invalid_client", status, body)
	}

	_, code = checksCode(t, s, "openid offline_access", "")
	status, body := checksPost(t, s, checksRedeem(code), "", "")
	if status != http.StatusOK {
		t.Fatalf("code grant with no secret = %d %v", status, body)
	}
	refresh, _ := body["refresh_token"].(string)
	if refresh == "" {
		t.Fatalf("no refresh token: %v", body)
	}
	refreshForm := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"local-dev"}}

	withSecret := url.Values{}
	for k, v := range refreshForm {
		withSecret[k] = v
	}
	withSecret.Set("client_secret", "not-a-secret")
	if status, body := checksPost(t, s, withSecret, "", ""); body["error"] != "invalid_client" {
		t.Errorf("refresh grant with a secret in the body = %d %v, want invalid_client", status, body)
	}
	withBasic := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}
	if status, body := checksPost(t, s, withBasic, "local-dev", "not-a-secret"); body["error"] != "invalid_client" {
		t.Errorf("refresh grant with a secret in Basic = %d %v, want invalid_client", status, body)
	}
	if status, body := checksPost(t, s, refreshForm, "", ""); status != http.StatusOK {
		t.Errorf("refresh grant with no secret = %d %v, want 200", status, body)
	}
}

// An exchange has one principal, its subject. A request that names an actor is
// refused, whether or not the actor token is one this issuer would accept.
func TestAnExchangeNamingAnActorIsRefused(t *testing.T) {
	t.Parallel()
	s := checksServer(t, "")
	_, code := checksCode(t, s, "openid", "")
	status, body := checksPost(t, s, checksRedeem(code), "", "")
	if status != http.StatusOK {
		t.Fatalf("redeem %d %v", status, body)
	}
	own, _ := body["access_token"].(string)

	base := func() url.Values {
		return url.Values{
			"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
			"subject_token":      {"github:example-org/gitops@refs/heads/master"},
			"subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"},
			"audience":           {"aws:1111:deployer"},
		}
	}
	if status, body := checksPost(t, s, base(), "local-dev", ""); status != http.StatusOK {
		t.Fatalf("an exchange with no actor = %d %v, want 200", status, body)
	}

	both := base()
	both.Set("actor_token", own)
	both.Set("actor_token_type", "urn:ietf:params:oauth:token-type:access_token")
	typeOnly := base()
	typeOnly.Set("actor_token_type", "urn:ietf:params:oauth:token-type:access_token")
	for name, form := range map[string]url.Values{"an accepted actor token": both, "an actor token type alone": typeOnly} {
		status, body := checksPost(t, s, form, "local-dev", "")
		if status != http.StatusBadRequest || body["error"] != "invalid_request" {
			t.Errorf("%s = %d %v, want 400 invalid_request", name, status, body)
		}
	}
}

// A confidential client's code is redeemed only with its secret: a wrong one
// and none at all are invalid_client, in the body or in HTTP Basic, and the
// right one is served.
func TestAConfidentialClientsCodeNeedsItsSecret(t *testing.T) {
	t.Parallel()
	s := checksServer(t, "")
	const redirect = "https://argocd.demo.example/auth/callback"
	redeem := func(code string) url.Values {
		return url.Values{
			"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
			"code_verifier": {pkceVerifier},
		}
	}

	for name, present := range map[string]func(url.Values) (string, string){
		"no secret": func(f url.Values) (string, string) { f.Set("client_id", "argocd"); return "", "" },
		"a wrong secret in body": func(f url.Values) (string, string) {
			f.Set("client_id", "argocd")
			f.Set("client_secret", "wrong")
			return "", ""
		},
		"a wrong secret in Basic": func(url.Values) (string, string) { return "argocd", "wrong" },
	} {
		_, code := checksCodeAt(t, s, "argocd", redirect, "openid", "")
		form := redeem(code)
		user, password := present(form)
		if status, body := checksPost(t, s, form, user, password); body["error"] != "invalid_client" || body["access_token"] != nil {
			t.Errorf("%s: %d %v, want invalid_client", name, status, body)
		}
	}

	_, code := checksCodeAt(t, s, "argocd", redirect, "openid", "")
	if status, body := checksPost(t, s, redeem(code), "argocd", "right"); status != http.StatusOK || body["access_token"] == nil {
		t.Errorf("the right secret in Basic: %d %v, want 200", status, body)
	}
}
