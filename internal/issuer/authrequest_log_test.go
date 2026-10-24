package issuer_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/storage/logtest"
)

// logsWhere are the records the test binary has logged that the test accepts.
func logsWhere(keep func(logtest.Record) bool) []logtest.Record {
	var found []logtest.Record
	for _, record := range refusalLog.Records() {
		if keep(record) {
			found = append(found, record)
		}
	}

	return found
}

// getWithoutFollowing is a GET whose redirect is left for the test to read.
func getWithoutFollowing(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()

	client := *server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	return response
}

// authRequestLine is the library's "auth request" line for a client id the
// test made up, which no other test uses.
func authRequestLine(t *testing.T, clientID string) logtest.Record {
	t.Helper()

	found := logsWhere(func(r logtest.Record) bool {
		return strings.HasPrefix(r.Message, "auth request") && r.String("client_id") == clientID
	})
	if len(found) != 1 {
		t.Fatalf("%d \"auth request\" lines name client %q, want 1", len(found), clientID)
	}

	return found[0]
}

// An authorization request with no client_id is the caller's mistake, and
// usually a scanner's: it is logged as a warning, and the line says what was
// wrong and where, so it can be read without the request.
func TestAnAuthorizeRequestWithoutAClientIsAWarningWithItsFields(t *testing.T) {
	t.Parallel()

	server, _ := signInServer(t, "ada@north.example")
	query := url.Values{
		"redirect_uri":          {"http://localhost:8000/callback"},
		"response_type":         {"code"},
		"state":                 {"state-secret-no-client"},
		"code_challenge":        {"challenge-secret-no-client"},
		"code_challenge_method": {"S256"},
	}
	response := getWithoutFollowing(t, server, "/authorize?"+query.Encode())
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}

	found := logsWhere(func(r logtest.Record) bool {
		return r.Message == "auth request" && strings.Contains(r.String("reason"), "missing client_id") &&
			r.String("path") == "/authorize"
	})
	if len(found) == 0 {
		t.Fatalf("no \"auth request\" line carries the reason and the path: %v", refusalLog.Messages())
	}
	for _, record := range found {
		if record.Level != slog.LevelWarn {
			t.Errorf("a request without a client_id is logged at %s, want WARN", record.Level)
		}
		if got := record.String("error"); got != "invalid_request" {
			t.Errorf("error = %q, want invalid_request", got)
		}
		if _, ok := record.Attr("client_id"); ok {
			t.Errorf("a request without a client_id logs one: %q", record.String("client_id"))
		}
	}

	if refusalLog.Mentions("state-secret-no-client") || refusalLog.Mentions("challenge-secret-no-client") {
		t.Error("the log carries a state or a PKCE challenge from the query")
	}
}

// A client_id the issuer does not know is a client error too, and the line
// names it. The query's secrets stay out of the log.
func TestAnAuthorizeRequestForAnUnknownClientIsAWarningThatNamesIt(t *testing.T) {
	t.Parallel()

	server, _ := signInServer(t, "ada@north.example")
	const id = "no-such-client-for-the-log-test"
	query := url.Values{
		"client_id":             {id},
		"redirect_uri":          {"https://stranger.example/cb"},
		"response_type":         {"code"},
		"state":                 {"state-secret-unknown"},
		"code_challenge":        {"challenge-secret-unknown"},
		"code_challenge_method": {"S256"},
	}
	getWithoutFollowing(t, server, "/authorize?"+query.Encode())

	record := authRequestLine(t, id)
	if record.Level != slog.LevelWarn {
		t.Errorf("an unknown client is logged at %s, want WARN", record.Level)
	}
	if record.String("path") != "/authorize" || record.String("error") == "" {
		t.Errorf("path = %q, error = %q, want both", record.String("path"), record.String("error"))
	}
	if refusalLog.Mentions("state-secret-unknown") || refusalLog.Mentions("challenge-secret-unknown") {
		t.Error("the log carries a state or a PKCE challenge from the query")
	}
	// The client is not declared, so the address is somebody else's text.
	for _, record := range logsWhere(func(r logtest.Record) bool { return r.String("client_id") == id }) {
		if _, ok := record.Attr("redirect_uri"); ok {
			t.Errorf("%q names the redirect_uri of a client nobody declared", record.Message)
		}
	}
}

// A refusal at /authorize names the redirect_uri when the client is one the
// installation declares.
func TestARefusalAtAuthorizeNamesTheRedirectURIOfAKnownClient(t *testing.T) {
	t.Parallel()

	server, _ := signInServer(t, "ada@north.example")
	query := url.Values{
		"client_id":     {"local-dev"},
		"redirect_uri":  {"http://localhost:8000/callback"},
		"response_type": {"code"},
		"state":         {"s-redirect"},
		"request_uri":   {"https://client.example/request.jwt?unique=redirect-uri-log-test"},
	}
	getWithoutFollowing(t, server, "/authorize?"+query.Encode())

	found := logsWhere(func(r logtest.Record) bool {
		return r.Message == "refused" && strings.Contains(r.String("reason"), "`request_uri` is not supported") &&
			strings.HasPrefix(r.String("redirect_uri"), "http://localhost:8000/callback")
	})
	if len(found) == 0 {
		t.Fatal("no refusal at /authorize names the redirect_uri of the declared client")
	}
	for _, record := range found {
		if got := record.String("redirect_uri"); got != "http://localhost:8000/callback" {
			t.Errorf("redirect_uri = %q, want the request's", got)
		}
	}
}

// lookupFailed is the error the library gives a failed client lookup.
func lookupFailed(cause error) error {
	return oidc.ErrInvalidRequestRedirectURI().WithDescription("unable to retrieve client by id").WithParent(cause)
}

// The level follows the fault: the caller's mistakes are warnings, the
// server's own failures stay errors, and a failure of ours that reached the
// library wrapped in an invalid request is still one.
func TestAnAuthRequestErrorIsLoggedAtTheLevelOfItsFault(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		want slog.Level
	}{
		"a client error":             {oidc.ErrInvalidRequest().WithDescription("auth request is missing client_id"), slog.LevelWarn},
		"an unregistered redirect":   {oidc.ErrInvalidRequestRedirectURI().WithDescription("nope"), slog.LevelWarn},
		"a server error":             {oidc.ErrServerError().WithDescription("the store is down"), slog.LevelError},
		"an error of no type":        {errors.New("something broke"), slog.LevelError},
		"a fault behind a mistake":   {lookupFailed(oidc.ErrServerError().WithDescription("the store is down")), slog.LevelError},
		"a lookup that found none":   {lookupFailed(oidc.ErrInvalidClient()), slog.LevelWarn},
		"a plain cause of a mistake": {lookupFailed(errors.New("unknown")), slog.LevelWarn},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			const prefix = "/log-level-test/"
			path := prefix + strings.ReplaceAll(name, " ", "-")
			request := httptest.NewRequest(http.MethodGet, path+"?client_id=level-"+strings.ReplaceAll(name, " ", "-"), nil)
			op.AuthRequestError(httptest.NewRecorder(), request, nil, tc.err, nil)

			found := logsWhere(func(r logtest.Record) bool { return r.Message == "auth request" && r.String("path") == path })
			if len(found) != 1 {
				t.Fatalf("%d lines for %s, want 1", len(found), path)
			}
			if found[0].Level != tc.want {
				t.Errorf("level = %s, want %s", found[0].Level, tc.want)
			}
			if got := found[0].String("client_id"); got != "level-"+strings.ReplaceAll(name, " ", "-") {
				t.Errorf("client_id = %q, want the request's", got)
			}
			if found[0].String("error") == "" {
				t.Error("the line has no error field")
			}
		})
	}
}

// What a caller puts in a client_id is text in the log, not control of it:
// control and bidirectional characters are removed and the length is bounded.
func TestAnAuthRequestLineKeepsACallersControlCharactersOut(t *testing.T) {
	t.Parallel()

	const path = "/log-sanitise-test"
	id := "sanitise-\x1b[31m\u202e" + strings.Repeat("x", 1000)
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Form = url.Values{"client_id": {id}}
	op.AuthRequestError(httptest.NewRecorder(), request, nil, oidc.ErrInvalidRequest().WithDescription("bad"), nil)

	found := logsWhere(func(r logtest.Record) bool { return r.Message == "auth request" && r.String("path") == path })
	if len(found) != 1 {
		t.Fatalf("%d lines for %s, want 1", len(found), path)
	}
	got := found[0].String("client_id")
	if !strings.HasPrefix(got, "sanitise-[31m") || strings.ContainsAny(got, "\x1b\u202e") {
		t.Errorf("client_id = %q, want the control characters gone", got)
	}
	if len([]rune(got)) > 260 {
		t.Errorf("client_id is %d runes, want it bounded", len([]rune(got)))
	}
}
