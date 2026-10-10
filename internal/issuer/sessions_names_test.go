package issuer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/gen/accessissuer/v1/accessissuerv1connect"
	sluisv1 "github.com/truvity/sluis/gen/sluis/v1"
	"github.com/truvity/sluis/gen/sluis/v1/sluisv1connect"
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
