package issuer_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// pkceClientsPolicy declares a confidential client of each PKCE policy and
// a public one: `strict` says `pkce: required`, `lax` says nothing (the
// default, which the OpenID Basic OP conformance clients rely on) and `cli`
// is public.
const pkceClientsPolicy = `
version: 1
groups:
  everyone:
    matchers:
      - email: ada@north.example
clients:
  strict:
    kind: confidential
    secret: strict-secret
    pkce: required
    redirects: ["https://strict.example/cb"]
    requires: ["everyone"]
  lax:
    kind: confidential
    secret: lax-secret
    redirects: ["https://lax.example/cb"]
    requires: ["everyone"]
  explicit:
    kind: confidential
    secret: explicit-secret
    pkce: optional
    redirects: ["https://explicit.example/cb"]
    requires: ["everyone"]
  cli:
    kind: public
    redirects: ["http://localhost:8000/callback"]
    requires: ["everyone"]
`

func pkceServer(t *testing.T) *httptest.Server {
	t.Helper()

	declared, err := policy.Parse([]byte(pkceClientsPolicy))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set,
		&fakeDirectory{standing: map[string]issuer.Standing{
			"ada@north.example": {Found: true, Authoritative: true},
		}}, issuer.NewMemoryState())

	secrets := lookup{
		"strict":   clientcreds.Secrets{Current: "strict-s"},
		"lax":      clientcreds.Secrets{Current: "lax-s"},
		"explicit": clientcreds.Secrets{Current: "explicit-s"},
	}
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, secrets, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	handler, err := handlerWithSignIn(iss, storage, issuer.SignInDeps{
		Providers: []issuer.SignIn{oneProvider{email: "ada@north.example"}},
		State:     access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return server
}

// pkceAuthorize is an authorization request for a client, with a challenge
// or without one.
func pkceAuthorize(client, redirect string, challenge bool) string {
	query := url.Values{
		"client_id":     {client},
		"redirect_uri":  {redirect},
		"response_type": {"code"},
		"scope":         {"openid"},
		"state":         {"pkce"},
	}
	if challenge {
		sum := sha256.Sum256([]byte(pkceVerifier))
		query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
		query.Set("code_challenge_method", "S256")
	}

	return "/authorize?" + query.Encode()
}

// pkceCode signs a person in for a confidential client's request and returns
// the code the browser is sent back with.
func pkceCode(t *testing.T, b *browser, client, redirect string, challenge bool) string {
	t.Helper()

	status, where, body := b.do(http.MethodGet, pkceAuthorize(client, redirect, challenge))
	if status != http.StatusFound {
		t.Fatalf("/authorize for %s = %d %s, want a redirect to the sign-in", client, status, body)
	}
	status, start, _ := b.do(http.MethodGet, where)
	if status != http.StatusFound {
		t.Fatalf("login page for %s = %d", client, status)
	}
	if strings.Contains(start, "/login/google/start") {
		_, toProvider, _ := b.do(http.MethodGet, start)
		state := toProvider[strings.Index(toProvider, "state=")+len("state="):]
		status, start, _ = b.do(http.MethodGet, "/login/google/callback?code=x&state="+state)
		if status != http.StatusFound {
			t.Fatalf("provider callback for %s = %d", client, status)
		}
	}
	for strings.HasPrefix(start, "/") {
		_, start, _ = b.do(http.MethodGet, start)
	}
	back, err := url.Parse(start)
	if err != nil || back.Query().Get("code") == "" {
		t.Fatalf("the browser was sent to %q, want the callback with a code", start)
	}

	return back.Query().Get("code")
}

// pkceRedeem redeems a code with the client's secret, presenting the
// verifier or not, and returns the status and the body.
func pkceRedeem(t *testing.T, b *browser, client, secret, redirect, code string, verifier bool) (int, map[string]any) {
	t.Helper()

	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirect},
	}
	if verifier {
		form.Set("code_verifier", pkceVerifier)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, b.server.URL+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(client, secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	var body map[string]any
	if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("token response: %v", err)
	}

	return response.StatusCode, body
}

// A confidential client that says `pkce: required` is turned away without
// a challenge, as a public one is; with one it goes on to the sign-in. A
// confidential client that says nothing, or `optional`, is as it always was.
func TestAuthorizePKCEFollowsTheClientsPolicy(t *testing.T) {
	t.Parallel()
	b := newBrowser(t, pkceServer(t))

	for name, tc := range map[string]struct {
		client, redirect string
		challenge        bool
		refused          bool
	}{
		"required, no challenge":     {"strict", "https://strict.example/cb", false, true},
		"required, with a challenge": {"strict", "https://strict.example/cb", true, false},
		"default, no challenge":      {"lax", "https://lax.example/cb", false, false},
		"default, with a challenge":  {"lax", "https://lax.example/cb", true, false},
		"optional, no challenge":     {"explicit", "https://explicit.example/cb", false, false},
		"public, no challenge":       {"cli", "http://localhost:8000/callback", false, true},
		"public, with a challenge":   {"cli", "http://localhost:8000/callback", true, false},
	} {
		status, where, body := b.do(http.MethodGet, pkceAuthorize(tc.client, tc.redirect, tc.challenge))
		if tc.refused {
			if status != http.StatusBadRequest || !strings.Contains(body, "invalid_request") {
				t.Errorf("%s: /authorize = %d %s, want 400 invalid_request", name, status, body)
			}

			continue
		}
		if status != http.StatusFound || !strings.HasPrefix(where, "/login") {
			t.Errorf("%s: /authorize = %d %q %s, want a redirect to /login", name, status, where, body)
		}
	}
}

// A plain challenge stays refused for a client that requires PKCE.
func TestRequiredPKCEStillRefusesPlain(t *testing.T) {
	t.Parallel()
	b := newBrowser(t, pkceServer(t))

	status, _, body := b.do(http.MethodGet, pkceAuthorize("strict", "https://strict.example/cb", true)+"&code_challenge_method=plain")
	if status != http.StatusBadRequest || !strings.Contains(body, "invalid_request") {
		t.Errorf("/authorize with plain = %d %s, want 400 invalid_request", status, body)
	}
}

// A code issued against a challenge is redeemed with its verifier. A client
// that requires PKCE and one that merely sent a challenge are refused
// `invalid_grant` without it -- the library would have let the secret alone
// redeem the second -- and a client that sent no challenge is unchanged.
func TestRedemptionNeedsTheVerifierThePolicyAndTheChallengeImply(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		client, secret, redirect string
		challenge, verifier      bool
		want                     int
		wantError                string
	}{
		"required, challenge, verifier":    {"strict", "strict-s", "https://strict.example/cb", true, true, http.StatusOK, ""},
		"required, challenge, no verifier": {"strict", "strict-s", "https://strict.example/cb", true, false, http.StatusBadRequest, "invalid_grant"},
		"default, no challenge":            {"lax", "lax-s", "https://lax.example/cb", false, false, http.StatusOK, ""},
		"default, challenge, verifier":     {"lax", "lax-s", "https://lax.example/cb", true, true, http.StatusOK, ""},
		"default, challenge, no verifier":  {"lax", "lax-s", "https://lax.example/cb", true, false, http.StatusBadRequest, "invalid_grant"},
		"optional, no challenge":           {"explicit", "explicit-s", "https://explicit.example/cb", false, false, http.StatusOK, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBrowser(t, pkceServer(t))
			code := pkceCode(t, b, tc.client, tc.redirect, tc.challenge)

			status, body := pkceRedeem(t, b, tc.client, tc.secret, tc.redirect, code, tc.verifier)
			if status != tc.want || (tc.wantError != "" && body["error"] != tc.wantError) {
				t.Errorf("/token = %d %v, want %d %s", status, body, tc.want, tc.wantError)
			}
		})
	}
}

// A refused redemption leaves the code usable by whoever holds the verifier.
func TestARefusedRedemptionDoesNotSpendTheCode(t *testing.T) {
	t.Parallel()
	b := newBrowser(t, pkceServer(t))
	code := pkceCode(t, b, "strict", "https://strict.example/cb", true)

	if status, _ := pkceRedeem(t, b, "strict", "strict-s", "https://strict.example/cb", code, false); status != http.StatusBadRequest {
		t.Fatalf("without the verifier = %d, want 400", status)
	}
	if status, body := pkceRedeem(t, b, "strict", "strict-s", "https://strict.example/cb", code, true); status != http.StatusOK {
		t.Errorf("with the verifier = %d %v, want 200", status, body)
	}
}
