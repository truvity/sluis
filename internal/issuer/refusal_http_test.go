package issuer_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// refusalServer is the demonstration issuer with a secret held for its one
// confidential client, so that a wrong secret has something to be wrong
// about.
func refusalServer(t *testing.T) *httptest.Server {
	t.Helper()
	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatalf("parse the demonstration policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, &fakeDirectory{}, issuer.NewMemoryState())
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, lookup{"argocd": clientcreds.Secrets{Current: "right"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	h, err := handler(iss, storage)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return server
}

// refusedAt is one refusedAt from the token endpoint.
type refusedAt struct {
	status int
	header http.Header
	raw    string
	body   map[string]any
}

func (r refusedAt) description() string { s, _ := r.body["error_description"].(string); return s }

// postForm sends a form to /token, as a client with these credentials in the
// Authorization header, or as none when user is empty.
func postForm(t *testing.T, server *httptest.Server, path string, form url.Values, user, pass string, extra map[string]string) refusedAt {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	for name, value := range extra {
		req.Header.Set(name, value)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := refusedAt{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

const exchangeGrant = "urn:ietf:params:oauth:grant-type:token-exchange"

func exchangeForm(subject, audience string) url.Values {
	return url.Values{
		"grant_type": {exchangeGrant}, "subject_token": {subject},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"}, "audience": {audience},
	}
}

// Every refusal the token endpoint gives is the fixed words and a request
// id, and the log under that id holds the reason. The reasons are the ones
// a caller used to be handed, which is what makes them worth keeping.
func TestEveryTokenRefusalIsUniform(t *testing.T) {
	server := refusalServer(t)
	deployer := exchangeForm("github:example-org/gitops@refs/heads/master", "aws:1111:deployer")

	for _, tc := range []struct {
		name          string
		form          url.Values
		user, pass    string
		status        int
		code, phrase  string
		authenticated bool
		reasons       []string
	}{
		{"an unknown client", deployer, "nobody", "", 401, "invalid_client", "not authorised", true, []string{"nobody"}},
		{"a wrong secret", deployer, "argocd", "wrong", 401, "invalid_client", "not authorised", true, []string{"does not match", "argocd"}},
		{"an unknown client in the form", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {"nobody"}},
			"", "", 400, "invalid_client", "not authorised", false, []string{"nobody"}},
		{"a public client sending a secret", deployer, "local-dev", "secret", 401, "invalid_client", "not authorised", true, []string{"public client presents no secret"}},
		{"a code that was never issued", url.Values{
			"grant_type": {"authorization_code"}, "code": {"never"}, "client_id": {"local-dev"},
			"redirect_uri": {"http://localhost:8000/callback"}, "code_verifier": {strings.Repeat("a", 43)},
		}, "", "", 400, "invalid_grant", "not authorised", false, []string{"no such authorization code"}},
		{"a refresh token that was never issued", url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {"never"}, "client_id": {"local-dev"},
		}, "", "", 400, "invalid_grant", "not authorised", false, nil},
		{"an audience that is not declared", exchangeForm("github:example-org/gitops@refs/heads/master", "aws:9999:nobody"),
			"local-dev", "", 400, "invalid_target", "not authorised", true, []string{"aws:9999:nobody", "not a declared client"}},
		{"an audience the proof does not open", exchangeForm("github:example-org/gitops@refs/heads/master", "aws:1111:power"),
			"local-dev", "", 400, "invalid_target", "not authorised", true, []string{"aws:1111:power", "mgmt:k8s:admin"}},
		{"two audiences", url.Values{
			"grant_type": {exchangeGrant}, "subject_token": {"x"}, "subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"},
			"audience": {"a", "b"},
		}, "local-dev", "", 400, "invalid_grant", "not authorised", true, nil},
		{"a grant the server does not serve", url.Values{"grant_type": {"client_credentials"}},
			"argocd", "right", 400, "unsupported_grant_type", "invalid request", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := postForm(t, server, "/token", tc.form, tc.user, tc.pass, nil)
			if got.status != tc.status || got.body["error"] != tc.code {
				t.Fatalf("answered %d %s, want %d %s", got.status, got.raw, tc.status, tc.code)
			}
			wantRefusedAs(t, got.header, got.description(), tc.phrase, tc.reasons...)
			// Only the code, the words and the id: no field that could carry more.
			for field := range got.body {
				if field != "error" && field != "error_description" {
					t.Errorf("the refusedAt carries %q: %s", field, got.raw)
				}
			}
			if tc.code == "invalid_client" {
				challenge := got.header.Get("WWW-Authenticate")
				if tc.authenticated != strings.HasPrefix(challenge, "Basic ") {
					t.Errorf("WWW-Authenticate = %q for a request that sent Authorization = %v", challenge, tc.authenticated)
				}
			}
		})
	}
}

// A client that does not exist and a client whose secret is wrong are the
// same refusedAt, byte for byte but for the request id: otherwise the token
// endpoint is a list of the clients that exist.
func TestAnUnknownClientAndAWrongSecretAreIdentical(t *testing.T) {
	server := refusalServer(t)
	form := exchangeForm("github:example-org/gitops@refs/heads/master", "aws:1111:deployer")

	for name, send := range map[string]func(user string) refusedAt{
		"in the Authorization header": func(user string) refusedAt { return postForm(t, server, "/token", form, user, "wrong", nil) },
		"in the form": func(user string) refusedAt {
			return postForm(t, server, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {user}}, "", "", nil)
		},
	} {
		unknown, wrong := send("nobody"), send("argocd")
		for _, got := range []refusedAt{unknown, wrong} {
			if got.status == http.StatusOK {
				t.Fatalf("%s: a refused client was served: %s", name, got.raw)
			}
		}
		// The form carries no secret, so a known confidential client is
		// refused for presenting none -- the same refusedAt, too.
		if unknown.status != wrong.status {
			t.Errorf("%s: status %d vs %d", name, unknown.status, wrong.status)
		}
		for _, header := range []string{"Content-Type", "WWW-Authenticate", "Cache-Control"} {
			if unknown.header.Get(header) != wrong.header.Get(header) {
				t.Errorf("%s: %s %q vs %q", name, header, unknown.header.Get(header), wrong.header.Get(header))
			}
		}
		a := withoutID(unknown.raw, unknown.header.Get("X-Request-Id"))
		b := withoutID(wrong.raw, wrong.header.Get("X-Request-Id"))
		if a != b {
			t.Errorf("%s: the bodies differ:\n%s\n%s", name, a, b)
		}
		if unknown.header.Get("X-Request-Id") == wrong.header.Get("X-Request-Id") {
			t.Errorf("%s: two requests share an id", name)
		}
	}
}

// An unknown audience and one the proof is not admitted to are one code and
// one body, apart from the id; what differs is in the log, and in the audit
// record the exchange wrote.
func TestAnUnknownAudienceAndARefusedOneAreIdentical(t *testing.T) {
	server := refusalServer(t)
	unknown := postForm(t, server, "/token", exchangeForm("github:example-org/gitops@refs/heads/master", "aws:9999:nobody"), "local-dev", "", nil)
	refused := postForm(t, server, "/token", exchangeForm("github:example-org/gitops@refs/heads/master", "aws:1111:power"), "local-dev", "", nil)
	if unknown.body["error"] != "invalid_target" || refused.body["error"] != "invalid_target" {
		t.Fatalf("codes %v and %v, want both invalid_target", unknown.body["error"], refused.body["error"])
	}
	if unknown.status != refused.status {
		t.Errorf("status %d vs %d", unknown.status, refused.status)
	}
	a := withoutID(unknown.raw, unknown.header.Get("X-Request-Id"))
	b := withoutID(refused.raw, refused.header.Get("X-Request-Id"))
	if a != b {
		t.Errorf("the bodies differ:\n%s\n%s", a, b)
	}
}

// A request id a caller sends is the id the refusedAt quotes, when it is fit
// to quote; otherwise the service chooses.
func TestARefusalQuotesTheCallersRequestIDWhenFit(t *testing.T) {
	server := refusalServer(t)
	form := exchangeForm("x", "aws:9999:nobody")

	fit := postForm(t, server, "/token", form, "local-dev", "", map[string]string{"X-Request-Id": "gateway-0123456789"})
	wantRefusedAs(t, fit.header, fit.description(), "not authorised", "subject_token is invalid")
	if fit.header.Get("X-Request-Id") != "gateway-0123456789" {
		t.Errorf("X-Request-Id = %q, want the caller's", fit.header.Get("X-Request-Id"))
	}

	unfit := postForm(t, server, "/token", form, "local-dev", "", map[string]string{"X-Request-Id": "<b>not</b> fit to quote"})
	if id := unfit.header.Get("X-Request-Id"); strings.Contains(id, "<") || strings.Contains(unfit.raw, "<b>") {
		t.Errorf("an unfit id was quoted: %q in %s", id, unfit.raw)
	}
	wantRefusedAs(t, unfit.header, unfit.description(), "not authorised")
}

// get sends a GET to the issuer without following redirects.
func get(t *testing.T, server *httptest.Server, path, accept string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	client := *server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(raw)
}

const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

// authorizeQuery is a well-formed request from the public client, with
// extra parameters appended and, when the name is repeated, the later one
// winning.
func authorizeQuery(overrides url.Values) string {
	form := url.Values{
		"response_type": {"code"}, "scope": {"openid"}, "client_id": {"local-dev"},
		"redirect_uri": {"http://localhost:8000/callback"}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "state": {"s1"},
	}
	for name, values := range overrides {
		form[name] = values
	}
	return "/authorize?" + form.Encode()
}

// refusalAt is what a refused /authorize request told its caller, however it
// said it: a JSON body, a plain sentence, or an error in a redirect.
func refusalAt(t *testing.T, status int, header http.Header, body string) (code, description string) {
	t.Helper()
	if status == http.StatusFound {
		to, err := url.Parse(header.Get("Location"))
		if err != nil {
			t.Fatalf("Location: %v", err)
		}
		values := to.Query()
		if to.Fragment != "" {
			values, _ = url.ParseQuery(to.Fragment)
		}
		if values.Get("error") == "" {
			t.Fatalf("redirected to %q, which carries no error", to)
		}
		return values.Get("error"), values.Get("error_description")
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(body), &parsed) == nil {
		code, _ = parsed["error"].(string)
		description, _ = parsed["error_description"].(string)
		return code, description
	}
	return "", strings.TrimSpace(body)
}

// Every refusal at /authorize is the fixed words and a request id, in
// whichever form the caller reads, and the reason is in the log.
func TestEveryAuthorizeRefusalIsUniform(t *testing.T) {
	server := refusalServer(t)
	noPKCE := "/authorize?response_type=code&scope=openid&client_id=local-dev&redirect_uri=" +
		url.QueryEscape("http://localhost:8000/callback")

	for _, tc := range []struct {
		name, path string
		code       string
		phrase     string
		reasons    []string
	}{
		{"a public client without PKCE", noPKCE, "invalid_request", "invalid request", []string{"code_challenge"}},
		{"a plain challenge method", authorizeQuery(url.Values{"code_challenge_method": {"plain"}}), "invalid_request", "invalid request", []string{"S256"}},
		{"a resource nobody declared", authorizeQuery(url.Values{"resource": {"https://mcp.elsewhere.example/"}}), "invalid_target", "not authorised", []string{"mcp.elsewhere.example", "declares"}},
		{"a resource that is not a URI", authorizeQuery(url.Values{"resource": {"/mcp"}}), "invalid_target", "not authorised", []string{"absolute URI"}},
		{"a response type the client lacks", authorizeQuery(url.Values{"response_type": {"token"}}), "unauthorized_client", "not authorised", nil},
		{"a client nobody declared", "/authorize?response_type=code&scope=openid&client_id=nobody&redirect_uri=" + url.QueryEscape("http://localhost:8000/callback"), "", "invalid request", []string{"nobody"}},
	} {
		t.Run(tc.name+" as JSON", func(t *testing.T) {
			status, header, body := get(t, server, tc.path, "application/json")
			if status < 300 {
				t.Fatalf("answered %d %s, want a refusal", status, body)
			}
			code, description := refusalAt(t, status, header, body)
			if tc.code != "" && code != tc.code {
				t.Errorf("error = %q, want %s", code, tc.code)
			}
			wantRefusedAs(t, header, description, tc.phrase, tc.reasons...)
		})
		if tc.code == "unauthorized_client" {
			continue
		}
		t.Run(tc.name+" as a page", func(t *testing.T) {
			status, header, body := get(t, server, tc.path, "text/html")
			if status < 300 {
				t.Fatalf("answered %d, want a refusal", status)
			}
			if status == http.StatusFound {
				// The client registered where to be told, and is told.
				_, description := refusalAt(t, status, header, body)
				wantRefusedAs(t, header, description, tc.phrase, tc.reasons...)
				return
			}
			if id := requestIDOf(t, header); !strings.Contains(body, "Reference: "+id) {
				t.Errorf("the page does not quote %q:\n%s", id, body)
			}
			for _, reason := range tc.reasons {
				if strings.Contains(body, reason) {
					t.Errorf("the page gives away %q", reason)
				}
				if !strings.Contains(loggedFor(header.Get("X-Request-Id")), reason) {
					t.Errorf("the log holds no %q under %s", reason, header.Get("X-Request-Id"))
				}
			}
		})
	}
}

// A refusal that has an audit action leaves its record under the id the
// caller was given: the caller quotes the id, the operator finds the record
// by it, and the record holds the real reason.
func TestARefusedExchangeIsAuditedUnderTheCallersRequestID(t *testing.T) {
	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, &fakeDirectory{}, issuer.NewMemoryState())
	trail := audittest.New(t)
	iss.UseAudit(trail)
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := handler(iss, storage)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	for name, tc := range map[string]struct{ audience, reason, code string }{
		"an audience nobody declared":          {"aws:9999:nobody", "not a declared client", "unknown_audience"},
		"an audience the proof is shut out of": {"aws:1111:power", "holds no group this client requires", "not_admitted"},
	} {
		before := len(trail.Records())
		got := postForm(t, server, "/token", exchangeForm("github:example-org/gitops@refs/heads/master", tc.audience),
			"local-dev", "", nil)
		id := requestIDOf(t, got.header)
		records := trail.Records()[before:]
		if len(records) != 1 {
			t.Fatalf("%s: %d records, want one", name, len(records))
		}
		r := records[0]
		if r.GetContext().GetRequestId() != id {
			t.Errorf("%s: the record carries request id %q, the caller was told %q", name, r.GetContext().GetRequestId(), id)
		}
		if r.GetOutcome().GetResult() != auditv1.Outcome_RESULT_DENIED || !strings.Contains(r.GetOutcome().GetReason(), tc.reason) {
			t.Errorf("%s: outcome %v %q, want a denial that says %q", name, r.GetOutcome().GetResult(), r.GetOutcome().GetReason(), tc.reason)
		}
		if strings.Contains(got.raw, tc.reason) {
			t.Errorf("%s: the answer gives the reason away: %s", name, got.raw)
		}
		// The evaluation behind it, for the trail and nobody else.
		data := r.GetData().AsMap()
		if data["reason_code"] != tc.code || data["audience"] != tc.audience || data["directory"] != "not_applicable" {
			t.Errorf("%s: data = %v, want reason_code %s, audience %s, a machine proof", name, data, tc.code, tc.audience)
		}
		held, _ := data["held_groups"].([]any)
		if !slices.Contains(held, any("all:gitops:deployer")) {
			t.Errorf("%s: held_groups = %v, want the groups the proof holds", name, held)
		}
		missing, _ := data["missing_requires"].([]any)
		if (len(missing) != 0) != (tc.code == "not_admitted") || (tc.code == "not_admitted" && !slices.Contains(missing, any("mgmt:k8s:admin"))) {
			t.Errorf("%s: missing_requires = %v", name, missing)
		}
		if admitted, present := data["admitted"]; !present || admitted != false {
			t.Errorf("%s: admitted = %v, want false", name, admitted)
		}
		if tc.code == "not_admitted" {
			if proof := data["proof"]; proof != "ci" {
				t.Errorf("%s: proof = %v, want ci", name, proof)
			}
		}
	}
}
