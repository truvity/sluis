package issuer_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/refusal"
	"github.com/truvity/sluis/storage/logtest"
)

// refusedRecord is the one "refused" line logged under a request id.
func refusedRecord(t *testing.T, id string) logtest.Record {
	t.Helper()
	var found []logtest.Record
	for _, record := range refusalLog.Records() {
		if record.Message == "refused" && record.String(refusal.LogKey) == id {
			found = append(found, record)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d refused lines under request %s, want one:\n%s", len(found), id, loggedFor(id))
	}
	return found[0]
}

// wantNoneOf fails when any attribute of the record carries one of the
// values, which are handles or secrets the log must not hold.
func wantNoneOf(t *testing.T, record logtest.Record, values ...string) {
	t.Helper()
	for _, attr := range record.Attrs {
		for _, value := range values {
			if strings.Contains(attr.Value.String(), value) {
				t.Errorf("%s=%q carries %q, which is not for the log", attr.Key, attr.Value.String(), value)
			}
		}
	}
}

// A refusal says which request it refused -- the method, the path, the
// client and the user agent -- so that an operator can tell one broken
// client from a link going round. The authorization request's id, which
// is a handle on somebody's sign-in, and the query it travels in are not
// among them.
func TestARefusalNamesTheRequestItRefused(t *testing.T) {
	t.Parallel()

	server, _ := signInServer(t, "ada@north.example")
	attacker := newBrowser(t, server)
	victim := newBrowser(t, server)

	link := startRequest(t, attacker)
	request := strings.TrimPrefix(link, "/login?auth=")

	for name, tc := range map[string]struct{ path, want string }{
		"the login link":     {link, "/login"},
		"the provider start": {"/login/google/start?auth=" + url.QueryEscape(request), "/login/google/start"},
		"the code hand-over": {"/authorize/callback?id=" + url.QueryEscape(request), "/authorize/callback"},
	} {
		t.Run(name, func(t *testing.T) {
			status, _, _, header := victim.send(http.MethodGet, tc.path, nil)
			if status != http.StatusBadRequest {
				t.Fatalf("%s = %d, want the 400 page", tc.path, status)
			}
			record := refusedRecord(t, requestIDOf(t, header))

			for key, want := range map[string]string{
				"reason":    "the authorization request was not started in this browser",
				"status":    "400",
				"method":    http.MethodGet,
				"path":      tc.want,
				"client_id": "local-dev",
			} {
				if got := record.String(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			if ua := record.String("user_agent"); !strings.HasPrefix(ua, "Go-http-client/") {
				t.Errorf("user_agent = %q, want the caller's", ua)
			}
			wantNoneOf(t, record, request, "?", "auth=", "id=")
		})
	}
}

// A refusal from the token endpoint, which the library makes from a context
// and no request, names the request too, and holds nothing of the form: not
// the code, not its verifier.
func TestATokenRefusalNamesTheRequestAndNotTheForm(t *testing.T) {
	server := refusalServer(t)
	code, verifier := "never-issued-code-0123", strings.Repeat("v", 43)

	got := postForm(t, server, "/token?state=query-state-0123", url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {"local-dev"},
		"redirect_uri": {"http://localhost:8000/callback"}, "code_verifier": {verifier},
	}, "", "", map[string]string{"User-Agent": "client/1.0 " + strings.Repeat("x", 500)})
	if got.status != http.StatusBadRequest {
		t.Fatalf("answered %d %s, want a refusal", got.status, got.raw)
	}
	record := refusedRecord(t, requestIDOf(t, got.header))

	if record.String("method") != http.MethodPost || record.String("path") != "/token" {
		t.Errorf("method, path = %q, %q, want POST /token", record.String("method"), record.String("path"))
	}
	if record.String("client_id") != "local-dev" && record.String("client") != "local-dev" {
		t.Errorf("the refusal names no client:\n%s", loggedFor(requestIDOf(t, got.header)))
	}
	ua := record.String("user_agent")
	if !strings.HasPrefix(ua, "client/1.0 ") || len([]rune(ua)) > 201 {
		t.Errorf("user_agent = %d runes %q, want the caller's cut to 200 and a marker", len([]rune(ua)), ua)
	}
	wantNoneOf(t, record, code, verifier, "query-state-0123", "state=")
}
