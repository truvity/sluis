package issuer_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/issuer"
)

// Discovery says what this issuer does: the claims it issues, the one
// response mode it answers in, and no surface it does not serve.
func TestDiscoveryStatesWhatThisIssuerDoes(t *testing.T) {
	t.Parallel()
	server, _ := signInServer(t, "ada@north.example")

	resp, err := http.Get(server.URL + "/.well-known/openid-configuration") //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var doc map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}

	list := func(name string) []string {
		raw, _ := doc[name].([]any)
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			s, _ := v.(string)
			out = append(out, s)
		}

		return out
	}

	if got := list("response_modes_supported"); !slices.Equal(got, []string{"query"}) {
		t.Errorf("response_modes_supported = %v, want [query]", got)
	}

	claims := list("claims_supported")
	for _, want := range []string{
		"iss", "sub", "aud", "exp", "iat", "auth_time", "nonce", "acr", "amr", "azp", "at_hash", "sid",
		"email", "email_verified", "name", "given_name", "family_name", "preferred_username", "groups",
	} {
		if !slices.Contains(claims, want) {
			t.Errorf("claims_supported lacks %q, which a token carries: %v", want, claims)
		}
	}
	// claims_supported lists ID token and userinfo claims; client_id is on
	// the access token only.
	if slices.Contains(claims, "client_id") {
		t.Errorf("claims_supported lists client_id, which no ID token or userinfo response carries: %v", claims)
	}

	for _, name := range []string{"introspection_endpoint", "device_authorization_endpoint", "registration_endpoint"} {
		if _, ok := doc[name]; ok {
			t.Errorf("discovery advertises %s, which is not served", name)
		}
	}
}

// OpenID Connect Core 15.1: RS256 is listed whenever an RS256 key is
// published, and an installation that publishes none does not claim it.
func TestRS256IsListedWhileAnRSAKeyIsPublished(t *testing.T) {
	t.Parallel()

	algs := func(server string) []string {
		resp, err := http.Get(server + "/.well-known/openid-configuration") //nolint:noctx // a test
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()

		var doc struct {
			Algs []string `json:"id_token_signing_alg_values_supported"`
		}
		if err = json.NewDecoder(resp.Body).Decode(&doc); err != nil {
			t.Fatal(err)
		}

		return doc.Algs
	}

	with := newMultiAlgServerWith(t, mustSoftKey(t, false), mustSoftKey(t, true))
	if got := algs(with.URL); !slices.Contains(got, string(jose.RS256)) || !slices.Contains(got, string(jose.ES384)) {
		t.Errorf("with an RSA key published: id_token_signing_alg_values_supported = %v, want RS256 and ES384", got)
	}
}

// The library's introspection and device-authorization routes answer nothing:
// neither is offered.
func TestIntrospectionAndDeviceRoutesAreNotServed(t *testing.T) {
	t.Parallel()
	server := refusalServer(t)

	for _, path := range []string{"/oauth/introspect", "/introspect", "/device_authorization", "/oauth/device_authorization"} {
		got := postForm(t, server, path, url.Values{"token": {"x"}, "client_id": {"argocd"}}, "argocd", "right", nil)
		if got.status != http.StatusNotFound {
			t.Errorf("POST %s = %d %s, want 404", path, got.status, got.raw)
		}
	}
}

// An authorization request for something this issuer does not do goes back
// to the client as the error its specification names, with `state` and `iss`,
// and starts nothing.
func TestUnservedAuthorizationFeaturesAreRefusedToTheClient(t *testing.T) {
	t.Parallel()
	server, _ := signInServer(t, "ada@north.example")

	for name, tc := range map[string]struct {
		extra string
		code  string
	}{
		"form_post":    {"&response_mode=form_post", "invalid_request"},
		"fragment":     {"&response_mode=fragment", "invalid_request"},
		"request_uri":  {"&request_uri=" + url.QueryEscape("https://client.example/request.jwt"), "request_uri_not_supported"},
		"registration": {"&registration=" + url.QueryEscape(`{"client_name":"x"}`), "registration_not_supported"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			b := newBrowser(t, server)
			status, location, body := b.do(http.MethodGet, "/authorize?"+url.Values{
				"client_id":             {"local-dev"},
				"redirect_uri":          {"http://localhost:8000/callback"},
				"response_type":         {"code"},
				"scope":                 {"openid"},
				"state":                 {"s1"},
				"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
				"code_challenge_method": {"S256"},
			}.Encode()+tc.extra)
			if status != http.StatusFound {
				t.Fatalf("status = %d %s, want a redirect to the client", status, body)
			}

			to, err := url.Parse(location)
			if err != nil || to.Host != "localhost:8000" || to.Path != "/callback" {
				t.Fatalf("Location = %q, want the client's callback", location)
			}

			if got := to.Query(); got.Get("error") != tc.code || got.Get("state") != "s1" ||
				got.Get("iss") != "http://issuer.example" || got.Has("code") {
				t.Errorf("redirect parameters = %v, want %s with state and iss and no code", got, tc.code)
			}
		})
	}
}

// userinfoCall sends one request to /userinfo.
func userinfoCall(t *testing.T, server, method, query string, header http.Header, form url.Values) (int, http.Header, string) {
	t.Helper()

	var content io.Reader
	if form != nil {
		content = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(t.Context(), method, server+"/userinfo"+query, content)
	if err != nil {
		t.Fatal(err)
	}

	for name, values := range header {
		req.Header[name] = values
	}

	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, resp.Header, string(body)
}

// RFC 6750 over the real endpoint: a request with no token is challenged with
// no error; a token in the URL is refused; the header and the POST body work;
// and a token whose session ended is a 401 `invalid_token`, where the library
// said 403 and `{}`.
func TestUserinfoSpeaksRFC6750(t *testing.T) {
	t.Parallel()
	server, iss := signInServer(t, "ada@north.example")

	access, _ := signedInTokens(t, server, iss, "local-dev")["access_token"].(string)
	if access == "" {
		t.Fatal("no access token")
	}

	bearer := http.Header{"Authorization": {"Bearer " + access}}
	const realm = `Bearer realm="http://issuer.example"`

	// No token at all: a challenge that names no error.
	status, header, _ := userinfoCall(t, server.URL, http.MethodGet, "", nil, nil)
	if status != http.StatusUnauthorized || header.Get("WWW-Authenticate") != realm {
		t.Errorf("no token: %d %q, want 401 and %q", status, header.Get("WWW-Authenticate"), realm)
	}

	// A token that is nothing.
	status, header, _ = userinfoCall(t, server.URL, http.MethodGet, "", http.Header{"Authorization": {"Bearer nonsense"}}, nil)
	if status != http.StatusUnauthorized || header.Get("WWW-Authenticate") != realm+`, error="invalid_token"` {
		t.Errorf("a bad token: %d %q, want 401 invalid_token", status, header.Get("WWW-Authenticate"))
	}

	// The header and the body of a POST are what OpenID Connect Core allows.
	if status, _, body := userinfoCall(t, server.URL, http.MethodGet, "", bearer, nil); status != http.StatusOK {
		t.Errorf("GET with the header: %d %s", status, body)
	}

	if status, _, body := userinfoCall(t, server.URL, http.MethodPost, "", nil, url.Values{"access_token": {access}}); status != http.StatusOK {
		t.Errorf("POST with the token in the body: %d %s", status, body)
	}

	// The URL is not: it ends up in logs, history and Referer.
	for name, call := range map[string]func() (int, http.Header, string){
		"GET": func() (int, http.Header, string) {
			return userinfoCall(t, server.URL, http.MethodGet, "?access_token="+url.QueryEscape(access), nil, nil)
		},
		"GET beside a header": func() (int, http.Header, string) {
			return userinfoCall(t, server.URL, http.MethodGet, "?access_token="+url.QueryEscape(access), bearer, nil)
		},
		"POST": func() (int, http.Header, string) {
			return userinfoCall(t, server.URL, http.MethodPost, "?access_token="+url.QueryEscape(access), nil, url.Values{})
		},
	} {
		status, header, body := call()
		if status != http.StatusBadRequest || header.Get("WWW-Authenticate") != realm+`, error="invalid_request"` ||
			strings.Contains(body, access) {
			t.Errorf("%s with the token in the query: %d %q %s, want 400 invalid_request", name, status, header.Get("WWW-Authenticate"), body)
		}
	}

	// And a revoked session's token is simply no longer a token.
	sessions, err := iss.Sessions().List(t.Context(), issuer.Query{Identity: "ada@north.example"})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("list the sessions: %v %v", sessions, err)
	}

	for _, session := range sessions {
		if _, err = iss.Sessions().RevokeID(t.Context(), session.ID); err != nil {
			t.Fatalf("revoke: %v", err)
		}
	}

	status, header, body := userinfoCall(t, server.URL, http.MethodGet, "", bearer, nil)
	if status != http.StatusUnauthorized || header.Get("WWW-Authenticate") != realm+`, error="invalid_token"` ||
		!strings.Contains(body, `"error":"invalid_token"`) {
		t.Errorf("after revocation: %d %q %s, want 401 invalid_token", status, header.Get("WWW-Authenticate"), body)
	}
}
