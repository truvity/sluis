package issuer_test

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// The standard claims follow the scopes granted, in the ID token and in
// userinfo alike (OpenID Connect Core 5.4): `email` and `email_verified`
// with `email`; `name`, `given_name`, `family_name` and `preferred_username`
// with `profile`. `sub` is always there, and so are the policy's claims
// (`groups`, `tailnet`), which belong to no standard scope.

var (
	emailClaims   = []string{"email", "email_verified"}
	profileClaims = []string{"name", "given_name", "family_name", "preferred_username"}
	policyClaims  = []string{"groups", "tailnet"}
)

// scopeServer is the issuer with its sign-in pages, signing Ada in through
// a directory that knows her names and her group.
func scopeServer(t *testing.T) *httptest.Server {
	t.Helper()

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set,
		&fakeDirectory{standing: map[string]issuer.Standing{
			"ada@north.example": {
				Found: true, Authoritative: true, Groups: []string{"engineering@north.example"},
				GivenName: "Ada", FamilyName: "Lovelace",
			},
		}}, issuer.NewMemoryState())
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
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

// signInFor signs Ada in at `local-dev` asking for scope, through the
// authorization endpoint and the provider, and redeems the code.
func signInFor(t *testing.T, server *httptest.Server, scope string) map[string]any {
	t.Helper()

	b := newBrowser(t, server)
	sum := sha256.Sum256([]byte(pkceVerifier))
	query := url.Values{
		"client_id": {"local-dev"}, "redirect_uri": {"http://localhost:8000/callback"},
		"response_type": {"code"}, "scope": {scope}, "state": {"scopes"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	status, where, body := b.do(http.MethodGet, "/authorize?"+query.Encode())
	if status != http.StatusFound {
		t.Fatalf("/authorize with %q = %d %s, want a redirect to the sign-in", scope, status, body)
	}
	_, start, _ := b.do(http.MethodGet, where)
	if strings.Contains(start, "/login/google/start") {
		_, toProvider, _ := b.do(http.MethodGet, start)
		state := toProvider[strings.Index(toProvider, "state=")+len("state="):]
		_, start, _ = b.do(http.MethodGet, "/login/google/callback?code=x&state="+state)
	}

	return redeemAs(t, b, "local-dev", start)
}

// claimsAre fails unless got carries each of want and none of the other
// standard claims a scope gates.
func claimsAre(t *testing.T, where string, got map[string]any, want ...string) {
	t.Helper()

	for _, name := range append(append([]string{"sub"}, policyClaims...), want...) {
		if _, present := got[name]; !present {
			t.Errorf("%s: no %s, want it", where, name)
		}
	}
	for _, name := range append(slices.Clone(emailClaims), profileClaims...) {
		if _, present := got[name]; present && !slices.Contains(want, name) {
			t.Errorf("%s: carries %s = %v, which no granted scope asks for", where, name, got[name])
		}
	}
}

func TestTheIDTokenAndUserinfoFollowTheScopesGranted(t *testing.T) {
	t.Parallel()

	both := append(slices.Clone(emailClaims), profileClaims...)
	for _, c := range []struct {
		scope string
		want  []string
	}{
		{"openid", nil},
		{"openid email", emailClaims},
		{"openid profile", profileClaims},
		{"openid email profile", both},
		{"openid email profile offline_access", both},
		// `groups` is no scope here. It is dropped, never refused, and the
		// `groups` claim comes from the policy whatever is asked for.
		{"openid groups", nil},
		{"openid email profile groups offline_access", both},
	} {
		t.Run(c.scope, func(t *testing.T) {
			t.Parallel()
			// A server each: the fake directory counts its calls unguarded.
			server := scopeServer(t)
			tokens := signInFor(t, server, c.scope)

			idToken, _ := tokens["id_token"].(string)
			if idToken == "" {
				t.Fatalf("no ID token in %v", tokens)
			}
			claimsAre(t, "id_token", payloadOf(t, idToken), c.want...)
			claimsAre(t, "userinfo", userinfoOf(t, server, accessOf(t, tokens)), c.want...)

			_, refresh := tokens["refresh_token"]
			if offline := strings.Contains(c.scope, "offline_access"); refresh != offline {
				t.Errorf("refresh token issued = %v, want %v", refresh, offline)
			}
		})
	}
}

// What a gateway, Grafana, Kargo, Headlamp, ArgoCD and sluisctl do: refresh
// without naming a scope. The refreshed ID token and the new access token's
// userinfo answer as the sign-in's grant did (RFC 6749 6), or a gateway
// reading the address from the ID token would lose it after the first hour.
func TestARefreshNamingNoScopeKeepsTheSignInsClaims(t *testing.T) {
	t.Parallel()
	server := scopeServer(t)
	tokens := signInFor(t, server, "openid email profile offline_access")

	refreshed := postToken(t, server, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tokens["refresh_token"].(string)},
		"client_id":     {"local-dev"},
	}, "local-dev", http.StatusOK)

	idToken := payloadOf(t, refreshed["id_token"].(string))
	if idToken["email"] != "ada@north.example" || idToken["email_verified"] != true || idToken["name"] != "Ada Lovelace" {
		t.Errorf("refreshed ID token email %v email_verified %v name %v, want the sign-in's",
			idToken["email"], idToken["email_verified"], idToken["name"])
	}
	both := append(slices.Clone(emailClaims), profileClaims...)
	claimsAre(t, "refreshed id_token", idToken, both...)
	claimsAre(t, "refreshed userinfo", userinfoOf(t, server, accessOf(t, refreshed)), both...)
}

// A refresh that names fewer scopes gets the claims of those, in the ID
// token and in the new access token's userinfo.
func TestARefreshNamingFewerScopesGetsTheirClaims(t *testing.T) {
	t.Parallel()
	server := scopeServer(t)
	tokens := signInFor(t, server, "openid email profile offline_access")

	refreshed := postToken(t, server, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tokens["refresh_token"].(string)},
		"client_id":     {"local-dev"},
		"scope":         {"openid email"},
	}, "local-dev", http.StatusOK)

	claimsAre(t, "narrowed id_token", payloadOf(t, refreshed["id_token"].(string)), emailClaims...)
	claimsAre(t, "narrowed userinfo", userinfoOf(t, server, accessOf(t, refreshed)), emailClaims...)
}

// Userinfo answers the access token's grant. A scope named on the userinfo
// request itself widens nothing.
func TestUserinfoAnswersTheAccessTokensGrantNotTheRequest(t *testing.T) {
	t.Parallel()
	server := scopeServer(t)
	access := accessOf(t, signInFor(t, server, "openid"))

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/userinfo",
		strings.NewReader(url.Values{"scope": {"openid email profile"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer "+access)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("userinfo: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("userinfo with a scope in the body = %d", response.StatusCode)
	}

	claimsAre(t, "userinfo", userinfoOf(t, server, access))
}

// The access token is not gated: a gateway and OpenBao read the address and
// the names from it, and it carries them for `openid` alone.
func TestTheAccessTokenStillNamesThePersonForOpenIDAlone(t *testing.T) {
	t.Parallel()
	server := scopeServer(t)
	claims := payloadOf(t, accessOf(t, signInFor(t, server, "openid")))

	for name, want := range map[string]string{"name": "Ada Lovelace", "given_name": "Ada", "family_name": "Lovelace"} {
		if claims[name] != want {
			t.Errorf("access token %s = %v, want %q", name, claims[name], want)
		}
	}
}

// An exchange mints an access token and nothing else, even asked for an ID
// token, so no exchange carries claims gated by scope. Its callers read the
// access token, which is not gated.
func TestAnExchangeMintsNoIDToken(t *testing.T) {
	t.Parallel()
	server, _ := serveIssuer(t)

	status, body := post(t, server, url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":        {"github:example-org/gitops@refs/heads/master"},
		"subject_token_type":   {"urn:ietf:params:oauth:token-type:jwt"},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:id_token"},
		"audience":             {"aws:1111:deployer"},
		"scope":                {"openid email profile"},
	}, "local-dev")
	if status != http.StatusOK {
		t.Fatalf("exchange: %d %v", status, body)
	}
	if body["issued_token_type"] != "urn:ietf:params:oauth:token-type:access_token" {
		t.Errorf("issued_token_type = %v, want an access token", body["issued_token_type"])
	}
	if _, present := body["id_token"]; present {
		t.Errorf("an exchange returned an ID token: %v", body)
	}
}
