package issuer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/gen/accessissuer/v1/accessissuerv1connect"
	sluisv1 "github.com/truvity/sluis/gen/sluis/v1"
	"github.com/truvity/sluis/gen/sluis/v1/sluisv1connect"
	"github.com/truvity/sluis/internal/refusal"
)

type namedSessions struct {
	accessissuerv1connect.UnimplementedSessionServiceHandler
}

func (namedSessions) ListSessions(
	_ context.Context, req *connect.Request[accessissuerv1.ListSessionsRequest],
) (*connect.Response[accessissuerv1.ListSessionsResponse], error) {
	return connect.NewResponse(&accessissuerv1.ListSessionsResponse{
		Sessions: []*accessissuerv1.Session{{Identity: req.Msg.GetIdentity()}},
	}), nil
}

// The session service answers under sluis.v1 and under the legacy
// accessissuer.v1, from the one handler: a new console works against it,
// and so does an old one.
func TestTheSessionServiceAnswersUnderBothNames(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mountSessions(mux, namedSessions{}, "")
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	current, err := sluisv1connect.NewSessionServiceClient(server.Client(), server.URL).
		ListSessions(t.Context(), connect.NewRequest(&sluisv1.ListSessionsRequest{Identity: "ada"}))
	if err != nil {
		t.Fatalf("sluis.v1.SessionService/ListSessions: %v", err)
	}
	if got := current.Msg.GetSessions(); len(got) != 1 || got[0].GetIdentity() != "ada" {
		t.Errorf("sluis.v1 answered %v, want one session for ada", got)
	}

	legacy, err := accessissuerv1connect.NewSessionServiceClient(server.Client(), server.URL).
		ListSessions(t.Context(), connect.NewRequest(&accessissuerv1.ListSessionsRequest{Identity: "ada"}))
	if err != nil {
		t.Fatalf("accessissuer.v1.SessionService/ListSessions: %v", err)
	}
	if got := legacy.Msg.GetSessions(); len(got) != 1 || got[0].GetIdentity() != "ada" {
		t.Errorf("accessissuer.v1 answered %v, want one session for ada", got)
	}
}

// The browser's preflight for the new path is answered as it is for the
// old one, or a console at another origin could not call it.
func TestThePreflightIsAnsweredOnTheNewNameToo(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mountSessions(mux, namedSessions{}, "https://console.example")

	for _, name := range []string{sluisv1connect.SessionServiceName, accessissuerv1connect.SessionServiceName} {
		request := httptest.NewRequest(http.MethodOptions, "/"+name+"/ListSessions", nil)
		request.Header.Set("Origin", "https://console.example")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusNoContent ||
			recorder.Header().Get("Access-Control-Allow-Origin") != "https://console.example" {
			t.Errorf("preflight on %s = %d, allow-origin %q", name, recorder.Code,
				recorder.Header().Get("Access-Control-Allow-Origin"))
		}
	}
}

type refusingSessions struct {
	accessissuerv1connect.UnimplementedSessionServiceHandler
}

func (refusingSessions) ListSessions(
	context.Context, *connect.Request[accessissuerv1.ListSessionsRequest],
) (*connect.Response[accessissuerv1.ListSessionsResponse], error) {
	return nil, connect.NewError(connect.CodePermissionDenied, errors.New("listing every session is an operator's"))
}

// The session service is behind the same interceptor as the console's: the
// reason a handler wrote for a refusal is in the log, not in the answer.
func TestTheSessionServiceRefusesInFixedWords(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mountSessions(mux, refusingSessions{}, "")
	server := httptest.NewServer(refusal.Middleware(mux))
	t.Cleanup(server.Close)

	_, err := sluisv1connect.NewSessionServiceClient(server.Client(), server.URL).
		ListSessions(t.Context(), connect.NewRequest(&sluisv1.ListSessionsRequest{}))
	var denied *connect.Error
	if !errors.As(err, &denied) || denied.Code() != connect.CodePermissionDenied ||
		!strings.HasPrefix(denied.Message(), "not authorised (request ") || strings.Contains(denied.Message(), "operator") {
		t.Errorf("a refused listing = %v, want permission_denied \"not authorised (request <id>)\"", err)
	}
}
