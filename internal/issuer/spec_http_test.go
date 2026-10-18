package issuer_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
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
		"email", "email_verified", "name", "given_name", "family_name", "preferred_username", "client_id", "groups",
	} {
		if !slices.Contains(claims, want) {
			t.Errorf("claims_supported lacks %q, which a token carries: %v", want, claims)
		}
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
