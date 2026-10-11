package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func reached(t *testing.T) (http.Handler, *bool) {
	t.Helper()
	got := false
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { got = true }), &got
}

func call(handler http.Handler, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/directory.v1.DirectoryService/Describe", nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// The API listener answers everything this hub knows about every company
// it serves, so reachable must not mean allowed. A NetworkPolicy is a
// second layer and one misapplied label away from admitting a namespace
// nobody meant to.
func TestOnlyDeclaredConsumersReachTheAPI(t *testing.T) {
	t.Parallel()

	next, arrived := reached(t)
	guard := &Consumers{
		Audience: "directory-roster",
		Allowed:  []string{"system:serviceaccount:identity:mapper"},
		Log:      slog.New(slog.DiscardHandler),
		Review: func(_ context.Context, token string, audiences []string) (string, error) {
			if len(audiences) != 1 || audiences[0] != "directory-roster" {
				t.Errorf("reviewed for %v, want the hub's own audience", audiences)
			}
			switch token {
			case "the-mapper":
				return "system:serviceaccount:identity:mapper", nil
			case "somebody-else":
				return "system:serviceaccount:business:web", nil
			default:
				return "", errors.New("not authenticated")
			}
		},
	}
	handler := guard.Middleware(next)

	for _, tc := range []struct {
		name, authorization string
		want                int
	}{
		{"a declared consumer", "Bearer the-mapper", http.StatusOK},
		{"lower-case scheme", "bearer the-mapper", http.StatusOK},
		{"another workload's token", "Bearer somebody-else", http.StatusUnauthorized},
		{"a token the cluster rejects", "Bearer forged", http.StatusUnauthorized},
		{"no header at all", "", http.StatusUnauthorized},
		// Deliberately not a base64 credential: the scheme is the whole
		// point of the case, and a literal shaped like a Basic credential
		// is a secret scanner's alert on every clone for ever.
		{"not a bearer", "Basic not-the-scheme-this-listener-takes", http.StatusUnauthorized},
		{"the word bearer and nothing else", "Bearer ", http.StatusUnauthorized},
	} {
		*arrived = false
		got := call(handler, tc.authorization)
		if got.Code != tc.want {
			t.Errorf("%s = %d, want %d (%s)", tc.name, got.Code, tc.want, got.Body.String())
		}
		if reachedIt := *arrived; reachedIt != (tc.want == http.StatusOK) {
			t.Errorf("%s reached the service = %v", tc.name, reachedIt)
		}
		if tc.want == http.StatusUnauthorized && got.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: no WWW-Authenticate, so a client reports a bare transport failure", tc.name)
		}
	}
}

// A deployment that named no consumers admits nobody. Answering everyone
// by default would be one forgotten value away from serving a company's
// directory to the whole cluster.
func TestNoDeclaredConsumersAdmitsNobody(t *testing.T) {
	t.Parallel()

	next, arrived := reached(t)
	guard := &Consumers{
		Audience: "directory-roster",
		Log:      slog.New(slog.DiscardHandler),
		Review: func(context.Context, string, []string) (string, error) {
			return "system:serviceaccount:identity:mapper", nil
		},
	}
	if got := call(guard.Middleware(next), "Bearer perfectly-good"); got.Code != http.StatusUnauthorized {
		t.Errorf("with no consumers declared = %d, want it refused", got.Code)
	}
	if *arrived {
		t.Error("a caller reached the service with no consumers declared")
	}
}

// Outside a cluster there is nothing to verify a token against, so the
// listener is open — a development posture, and the process says so at
// start rather than leaving it to be discovered.
func TestWithNoReviewerTheListenerIsOpen(t *testing.T) {
	t.Parallel()

	next, arrived := reached(t)
	for name, guard := range map[string]*Consumers{
		"no guard at all": nil,
		"no reviewer":     {Audience: "directory-roster"},
	} {
		*arrived = false
		if got := call(guard.Middleware(next), ""); got.Code != http.StatusOK || !*arrived {
			t.Errorf("%s = %d, reached = %v; want it open", name, got.Code, *arrived)
		}
	}
}

// The listener tells a refused caller "not authorised" and a request id,
// the same words whether the token was missing, rejected, from a workload
// that is not a consumer, or outside its grant. Which of those it was -- and
// who the workload was -- is in the log under the id.
func TestTheListenerRefusesInFixedWords(t *testing.T) {
	t.Parallel()

	next, _ := reached(t)
	guard := &Consumers{
		Audience: "directory-roster",
		Allowed:  []string{"system:serviceaccount:identity:mapper"},
		Grants:   map[string]*Grant{"system:serviceaccount:identity:mapper": {}},
		Log:      capturedLogger(),
		Review: func(_ context.Context, token string, _ []string) (string, error) {
			switch token {
			case "somebody-else":
				return "system:serviceaccount:business:web", nil
			case "the-mapper":
				return "system:serviceaccount:identity:mapper", nil
			default:
				return "", errors.New("tokenreview: the token is expired")
			}
		},
	}
	handler := guard.Middleware(next)

	for _, tc := range []struct {
		name, authorization, path string
		status                    int
		reasons                   []string
	}{
		{"no token", "", "/directory.v1.DirectoryService/Describe", http.StatusUnauthorized, []string{"no ServiceAccount token", "directory-roster"}},
		{"a token the cluster rejects", "Bearer forged", "/directory.v1.DirectoryService/Describe", http.StatusUnauthorized, []string{"the token is expired"}},
		{"a workload that is not a consumer", "Bearer somebody-else", "/directory.v1.DirectoryService/Describe", http.StatusUnauthorized, []string{"not a consumer", "business:web"}},
		{"a path that is not a procedure", "Bearer the-mapper", "/elsewhere", http.StatusForbidden, []string{"not a procedure"}},
	} {
		request := httptest.NewRequest(http.MethodPost, tc.path, nil)
		if tc.authorization != "" {
			request.Header.Set("Authorization", tc.authorization)
		}
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, request)
		if got.Code != tc.status {
			t.Errorf("%s = %d, want %d", tc.name, got.Code, tc.status)
		}
		id := got.Header().Get("X-Request-Id")
		if want := "not authorised (request " + id + ")\n"; got.Body.String() != want || id == "" {
			t.Errorf("%s: body %q, want exactly %q", tc.name, got.Body.String(), want)
		}
		logged := loggedFor(id)
		for _, reason := range tc.reasons {
			if !strings.Contains(logged, reason) {
				t.Errorf("%s: the log under %s does not carry %q:\n%s", tc.name, id, reason, logged)
			}
		}
	}
}
