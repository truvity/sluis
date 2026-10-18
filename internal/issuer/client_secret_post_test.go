package issuer_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// RFC 6749 2.3.1: discovery advertises `client_secret_post`, so a confidential
// client may send its id and secret in the body of the token request instead
// of in the Authorization header. Over the real endpoint: the right secret is
// served, a wrong or missing one is `invalid_client`, and the same secret in
// the header is served too.
func TestAConfidentialClientAuthenticatesWithClientSecretPost(t *testing.T) {
	t.Parallel()

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatal(err)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, adaDirectory(), issuer.NewMemoryState())

	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, lookup{"argocd": clientcreds.Secrets{Current: "right"}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	h, err := handler(iss, storage)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	// A session for the client to refresh. A fresh refresh token each time:
	// a used one is rotated away.
	open := func(refresh string) {
		t.Helper()

		if _, err := iss.Sessions().Record(t.Context(), issuer.Opened{
			Identity: "ada@north.example", ClientID: "argocd", How: issuer.HowCode,
			Token: refresh, Scopes: []string{"openid", "email"},
		}); err != nil {
			t.Fatalf("record the session: %v", err)
		}
	}
	refreshForm := func(refresh string, extra url.Values) url.Values {
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "scope": {"openid email"}}
		for name, values := range extra {
			form[name] = values
		}

		return form
	}

	open("rt-post")
	got := postForm(t, server, "/token", refreshForm("rt-post", url.Values{"client_id": {"argocd"}, "client_secret": {"right"}}), "", "", nil)
	if got.status != http.StatusOK || got.body["access_token"] == nil {
		t.Fatalf("client_secret_post with the right secret: %d %s", got.status, got.raw)
	}

	for name, extra := range map[string]url.Values{
		"the wrong secret": {"client_id": {"argocd"}, "client_secret": {"wrong"}},
		"an empty secret":  {"client_id": {"argocd"}, "client_secret": {""}},
		"no secret at all": {"client_id": {"argocd"}},
	} {
		open("rt-" + name)
		got = postForm(t, server, "/token", refreshForm("rt-"+name, extra), "", "", nil)

		if got.status < 400 || got.body["error"] != "invalid_client" || got.body["access_token"] != nil {
			t.Errorf("client_secret_post with %s: %d %s, want invalid_client", name, got.status, got.raw)
		}
	}

	// The header still works beside it.
	open("rt-basic")
	got = postForm(t, server, "/token", refreshForm("rt-basic", nil), "argocd", "right", nil)

	if got.status != http.StatusOK || got.body["access_token"] == nil {
		t.Errorf("client_secret_basic with the right secret: %d %s", got.status, got.raw)
	}
}
