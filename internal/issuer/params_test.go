package issuer_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// RFC 6749 section 3.1: a request parameter is included once. A request that
// repeats one the issuer decides on is refused with invalid_request before
// any layer reads it, because the layers in front of the library take the
// first value and the library takes the last.

var repeatedParameters = []string{
	"client_id", "redirect_uri", "response_type", "scope", "code_challenge",
	"code_challenge_method", "resource", "state", "nonce",
}

func TestAnAuthorizationRequestThatRepeatsAParameterIsRefused(t *testing.T) {
	t.Parallel()
	server := refusalServer(t)

	// The control: the same request, once each, is not refused for this.
	if status, header, body := get(t, server, authorizeQuery(url.Values{"nonce": {"n1"}}), "application/json"); status >= 400 {
		t.Fatalf("the well-formed request: %d %s", status, body)
	} else if header.Get("Location") == "" {
		t.Fatalf("the well-formed request was not sent on to sign in: %d", status)
	}

	base := url.Values{
		"response_type": {"code"}, "scope": {"openid"}, "client_id": {"local-dev"},
		"redirect_uri": {"http://localhost:8000/callback"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "state": {"s1"}, "nonce": {"n1"},
		"resource": {"https://mcp.example/"},
	}
	for _, name := range repeatedParameters {
		query := url.Values{}
		for k, v := range base {
			query[k] = v
		}
		// A second value, different from the first: what a downgrade or a
		// confusion needs. For the method it is the one this issuer refuses.
		query.Add(name, "second-"+name)
		if name == "code_challenge_method" {
			query.Set(name, "S256")
			query.Add(name, "plain")
		}

		for _, accept := range []string{"application/json", "text/html"} {
			status, header, body := get(t, server, "/authorize?"+query.Encode(), accept)
			if status != http.StatusBadRequest {
				t.Errorf("%s repeated (%s): status %d, want 400", name, accept, status)
				continue
			}
			if header.Get("Location") != "" {
				t.Errorf("%s repeated (%s): the refusal redirected", name, accept)
			}
			if accept == "application/json" {
				if code, _ := refusalAt(t, status, header, body); code != "invalid_request" {
					t.Errorf("%s repeated: error %q, want invalid_request", name, code)
				}
			}
		}
	}
}

func TestAnAuthorizationRequestRefusesARepeatAcrossTheQueryAndTheBody(t *testing.T) {
	t.Parallel()
	server := refusalServer(t)
	form := url.Values{"client_id": {"nobody"}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		server.URL+authorizeQuery(url.Values{})+"&client_id=other", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
}

func TestATokenRequestThatRepeatsAParameterIsRefused(t *testing.T) {
	t.Parallel()
	server := refusalServer(t)

	for _, name := range []string{"client_id", "scope", "resource", "redirect_uri"} {
		form := url.Values{
			"grant_type": {"authorization_code"}, "code": {"c"}, "client_id": {"local-dev"},
			"redirect_uri": {"http://localhost:8000/callback"}, "code_verifier": {"v"},
			"scope": {"openid"}, "resource": {"https://mcp.example/"},
		}
		form.Add(name, "second-"+name)
		status, body := post(t, server, form, "local-dev")
		if status != http.StatusBadRequest || body["error"] != "invalid_request" {
			t.Errorf("%s repeated: %d %v, want 400 invalid_request", name, status, body)
		}
	}
}

func TestAnEndSessionRequestThatRepeatsAParameterIsRefused(t *testing.T) {
	t.Parallel()
	server := refusalServer(t)

	for _, query := range []string{
		"client_id=local-dev&client_id=argocd",
		"client_id=local-dev&post_logout_redirect_uri=http://localhost:8000/&post_logout_redirect_uri=https://evil.example/",
		"client_id=local-dev&id_token_hint=a.b.c&id_token_hint=d.e.f",
		"client_id=local-dev&state=1&state=2",
	} {
		status, header, body := get(t, server, "/end_session?"+query, "application/json")
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", query, status)
			continue
		}
		if header.Get("Location") != "" {
			t.Errorf("%s: the refusal redirected", query)
		}
		if code, _ := refusalAt(t, status, header, body); code != "invalid_request" {
			t.Errorf("%s: error %q, want invalid_request", query, code)
		}
	}
}
