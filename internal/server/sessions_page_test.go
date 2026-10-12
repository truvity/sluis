package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/access"
)

type asked struct {
	identity string
	operator bool
}

type calledKey struct{}

// sessionsConsole is a console connected to a session service that records
// who it was told the caller is.
func sessionsConsole(got *[]asked) *ConsoleServer {
	s := &ConsoleServer{}
	s.UseSessions(&SessionsService{
		As: func(ctx context.Context, identity string, operator bool) context.Context {
			return context.WithValue(ctx, calledKey{}, asked{identity, operator})
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if a, ok := r.Context().Value(calledKey{}).(asked); ok {
				*got = append(*got, a)
			}
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
				http.Error(w, "the console's credentials reached the session service", http.StatusTeapot)
				return
			}
			w.WriteHeader(http.StatusOK)
		}),
	})
	return s
}

func callSessions(s *ConsoleServer, method, path string, id *access.Identity) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	r.Header.Set("Cookie", "access_session=the-console-session")
	if id != nil {
		r = r.WithContext(WithIdentity(r.Context(), *id))
	}
	w := httptest.NewRecorder()
	s.sessionsCall(w, r)
	return w
}

// A recovery sign-in has no issuer session, and is the operator the console
// says it is: its calls are made as that, by its subject.
func TestARecoverySignInListsSessionsThroughTheConsole(t *testing.T) {
	t.Parallel()
	var got []asked
	s := sessionsConsole(&got)
	recovery := &access.Identity{Email: "Recovery@Sluis", Source: access.SourceRecovery, Role: access.RoleOperator}
	if w := callSessions(s, http.MethodPost, "/sluis.v1.SessionService/ListSessions", recovery); w.Code != http.StatusOK {
		t.Fatalf("= %d %s", w.Code, w.Body)
	}
	if len(got) != 1 || got[0] != (asked{"recovery@sluis", true}) {
		t.Errorf("the service was told %v, want the recovery as an operator", got)
	}
}

func TestAViewerListsOnlyAsAViewer(t *testing.T) {
	t.Parallel()
	var got []asked
	s := sessionsConsole(&got)
	ada := &access.Identity{Email: "ada@north.example", Role: access.RoleViewer}
	if w := callSessions(s, http.MethodPost, "/accessissuer.v1.SessionService/ListSessions", ada); w.Code != http.StatusOK {
		t.Fatalf("= %d", w.Code)
	}
	if len(got) != 1 || got[0].operator {
		t.Errorf("the service was told %v, want a non-operator", got)
	}
}

func TestSessionsAreRefusedToNobodyAndToWhatIsNotACall(t *testing.T) {
	t.Parallel()
	var got []asked
	s := sessionsConsole(&got)
	ada := &access.Identity{Email: "ada@north.example", Role: access.RoleOperator}
	for _, tc := range []struct {
		name, method, path string
		id                 *access.Identity
		want               int
	}{
		{"nobody", http.MethodPost, "/sluis.v1.SessionService/ListSessions", nil, http.StatusUnauthorized},
		{"a GET", http.MethodGet, "/sluis.v1.SessionService/ListSessions", ada, http.StatusNotFound},
		{"a climb", http.MethodPost, "/sluis.v1.SessionService/../x", ada, http.StatusNotFound},
	} {
		if w := callSessions(s, tc.method, tc.path, tc.id); w.Code != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, w.Code, tc.want)
		}
	}
	if len(got) != 0 {
		t.Errorf("refused calls reached the service: %v", got)
	}
	if w := callSessions(&ConsoleServer{}, http.MethodPost, "/sluis.v1.SessionService/ListSessions", ada); w.Code != http.StatusNotFound {
		t.Errorf("unconnected = %d, want 404", w.Code)
	}
}
