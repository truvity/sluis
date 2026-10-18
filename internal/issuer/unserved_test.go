package issuer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/policy"
)

func surfaceClients(_ context.Context, id string) (op.Client, error) {
	if id != "an-app" {
		return nil, errors.New("unknown client")
	}

	return &client{id: id, declared: policy.Client{
		Kind:      policy.KindPublic,
		Redirects: []string{"https://app.example/cb"},
	}}, nil
}

const registered = "client_id=an-app&redirect_uri=https://app.example/cb&state=s1&response_type=code"

// OpenID Connect Core 6.2, 7.2.1 and the response modes: a request that asks
// for something this issuer does not do is refused, to a registered redirect
// URI as an error and to a page otherwise, and never reaches the library.
func TestAnAuthorizationRequestForAnUnservedFeatureIsRefused(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		query string
		code  string
	}{
		"request_uri":              {registered + "&request_uri=https://x.example/r", "request_uri_not_supported"},
		"registration":             {registered + "&registration=%7B%7D", "registration_not_supported"},
		"form_post":                {registered + "&response_mode=form_post", "invalid_request"},
		"fragment":                 {registered + "&response_mode=fragment", "invalid_request"},
		"an unknown mode":          {registered + "&response_mode=jwt", "invalid_request"},
		"query, then form_post":    {registered + "&response_mode=query&response_mode=form_post", "invalid_request"},
		"form_post, then query":    {registered + "&response_mode=form_post&response_mode=query", "invalid_request"},
		"request_uri before mode":  {registered + "&response_mode=form_post&request_uri=x", "request_uri_not_supported"},
		"registration before mode": {registered + "&response_mode=form_post&registration=x", "registration_not_supported"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := &reached{}
			handler := authorizeSurface(surfaceClients, got.handler())

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, authorizePath+"?"+tc.query, nil))

			if got.called {
				t.Fatal("the request reached the library")
			}

			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want a redirect to the registered URI", rec.Code)
			}

			to, err := url.Parse(rec.Header().Get("Location"))
			if err != nil || to.Host != "app.example" || to.Path != "/cb" {
				t.Fatalf("Location = %q", rec.Header().Get("Location"))
			}

			params := to.Query()
			if params.Get("error") != tc.code || params.Get("state") != "s1" || params.Get("error_description") == "" {
				t.Errorf("redirect parameters = %v, want %s with the state echoed", params, tc.code)
			}
		})
	}
}

// Nothing is redirected to an address the client did not register.
func TestAnUnservedFeatureIsNotRedirectedToAnUnregisteredURI(t *testing.T) {
	t.Parallel()

	for name, query := range map[string]string{
		"unregistered redirect": "client_id=an-app&redirect_uri=https://evil.example/cb&request_uri=x",
		"unknown client":        "client_id=nobody&redirect_uri=https://app.example/cb&response_mode=form_post",
		"no client":             "redirect_uri=https://app.example/cb&registration=x",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := &reached{}
			rec := httptest.NewRecorder()
			authorizeSurface(surfaceClients, got.handler()).
				ServeHTTP(rec, httptest.NewRequest(http.MethodGet, authorizePath+"?"+query, nil))

			if got.called || rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
				t.Errorf("called = %v, status = %d, Location = %q; want the 400 and no redirect",
					got.called, rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

// What conformance and every ordinary client send is let through untouched.
func TestARequestWithoutUnservedFeaturesIsLeftAlone(t *testing.T) {
	t.Parallel()

	for name, query := range map[string]string{
		"no mode":     registered,
		"query":       registered + "&response_mode=query",
		"empty mode":  registered + "&response_mode=",
		"a request":   registered + "&request=eyJ",
		"other paths": "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := &reached{}
			path := authorizePath
			if name == "other paths" {
				path = "/token"
				query = registered + "&response_mode=form_post&request_uri=x"
			}

			rec := httptest.NewRecorder()
			authorizeSurface(surfaceClients, got.handler()).
				ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?"+query, nil))

			if !got.called {
				t.Errorf("the request was refused: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
			}
		})
	}
}

// The routes the library mounts for features this issuer does not offer are
// not routes.
func TestUnservedRoutesAreNotFound(t *testing.T) {
	t.Parallel()

	got := &reached{}
	handler := withoutUnserved(got.handler())

	for _, path := range []string{"/oauth/introspect", "/introspect", "/device_authorization", "/oauth/device_authorization"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))

			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", method, path, rec.Code)
			}
		}
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/token", nil))

	if !got.called {
		t.Error("a served route was answered 404")
	}
}
