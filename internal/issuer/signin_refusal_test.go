package issuer_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// brokenProvider sends the browser out and cannot say who came back.
type brokenProvider struct{ err error }

func (brokenProvider) Kind() string { return "google" }

func (brokenProvider) URL(state string) (string, error) {
	return "https://idp.example/authorize?state=" + url.QueryEscape(state), nil
}

func (p brokenProvider) Identify(context.Context, string) (string, error) { return "", p.err }

// refusalSignIn is the issuer's sign-in over a directory and a provider of
// the test's choosing.
func refusalSignIn(t *testing.T, dir issuer.Directory, provider issuer.SignIn) (*httptest.Server, *audittest.Recorder) {
	t.Helper()
	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, dir, issuer.NewMemoryState())
	trail := audittest.New(t)
	iss.UseAudit(trail)
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	h, err := handlerWithSignIn(iss, storage, issuer.SignInDeps{
		Providers: []issuer.SignIn{provider},
		State:     access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return server, trail
}

// callback walks the provider round trip as far as the callback and returns
// what the callback answered.
func callback(t *testing.T, server *httptest.Server) (int, string, http.Header) {
	t.Helper()
	b := newBrowser(t, server)
	where := b.authorize("")
	_, toProvider, _ := b.do(http.MethodGet, where)
	state := toProvider[strings.Index(toProvider, "state=")+len("state="):]
	status, _, body, header := b.send(http.MethodGet, "/login/google/callback?code=x&state="+state, nil)
	return status, body, header
}

// A page that refuses a person says it did, and gives a reference. It does
// not give back the address they signed in with, the domain it is on, what
// the directory said, or what the provider's error was: whoever reads the
// screen is not necessarily the person, and none of it is theirs to act on.
// All of it is in the log, under the reference.
func TestASignInRefusalPageNamesNobodyAndNothing(t *testing.T) {
	t.Parallel()
	const email = "ada@north.example"

	for _, tc := range []struct {
		name     string
		dir      issuer.Directory
		provider issuer.SignIn
		status   int
		phrase   string
		reasons  []string
		code     string
	}{
		{"a directory that says the account is not live",
			&fakeDirectory{standing: map[string]issuer.Standing{email: {Found: false, Authoritative: true}}},
			oneProvider{email: email}, http.StatusForbidden, "not authorised", []string{"not live"}, "directory_refused"},
		{"a directory that cannot be reached", &fakeDirectory{err: errors.New("dial tcp 10.9.8.7:443: refused")},
			oneProvider{email: email}, http.StatusServiceUnavailable, "server error", []string{"could not be reached", "10.9.8.7"}, "directory_unavailable"},
		{"a provider that fails", &fakeDirectory{}, brokenProvider{err: errors.New("idp said: code expired for north.example")},
			http.StatusBadGateway, "server error", []string{"idp said: code expired"}, "provider_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server, trail := refusalSignIn(t, tc.dir, tc.provider)
			status, body, header := callback(t, server)
			if status != tc.status {
				t.Fatalf("callback = %d, want %d: %s", status, tc.status, body)
			}
			if id := requestIDOf(t, header); !strings.Contains(body, "Reference: "+id) {
				t.Errorf("the page does not quote the response's request id %q", id)
			}
			wantRefusedPage(t, body, tc.phrase, tc.reasons...)
			// One record, under the page's reference, with the evaluation.
			records := trail.Records()
			if len(records) != 1 {
				t.Fatalf("%d audit records, want one: %v", len(records), trail.Actions())
			}
			if got := records[0].GetContext().GetRequestId(); got != requestIDOf(t, header) {
				t.Errorf("the record carries request id %q, the page quotes %q", got, header.Get("X-Request-Id"))
			}
			if got := records[0].GetData().AsMap()["reason_code"]; got != tc.code {
				t.Errorf("reason_code = %v, want %s", got, tc.code)
			}
			for _, leaked := range []string{email, "north.example", "ada", "dial tcp", "idp said", "not live", "directory"} {
				if strings.Contains(body, leaked) {
					t.Errorf("the page gives away %q:\n%s", leaked, body)
				}
			}
		})
	}
}
