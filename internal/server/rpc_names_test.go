package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/gen/directoryroster/v1/directoryrosterv1connect"
	sluisv1 "github.com/truvity/sluis/gen/sluis/v1"
	"github.com/truvity/sluis/gen/sluis/v1/sluisv1connect"
)

// named is every operator service, answering ListHolders with a marker
// that says which request it saw and every other call as unimplemented.
type named struct {
	directoryrosterv1connect.UnimplementedWorkspaceServiceHandler
	directoryrosterv1connect.UnimplementedSettingsServiceHandler
	directoryrosterv1connect.UnimplementedAccessServiceHandler
	directoryrosterv1connect.UnimplementedGitHubServiceHandler
	directoryrosterv1connect.UnimplementedSlackAppServiceHandler
	directoryrosterv1connect.UnimplementedSlackSharedChannelServiceHandler
	directoryrosterv1connect.UnimplementedSlackChannelServiceHandler
	directoryrosterv1connect.UnimplementedSlackServiceHandler
	directoryrosterv1connect.UnimplementedCloudflareServiceHandler
}

func (named) ListHolders(
	_ context.Context, req *connect.Request[directoryrosterv1.ListHoldersRequest],
) (*connect.Response[directoryrosterv1.ListHoldersResponse], error) {
	return connect.NewResponse(&directoryrosterv1.ListHoldersResponse{PolicyDigest: "for " + req.Msg.GetGroup()}), nil
}

// Every operator service answers under sluis.v1 and under the legacy
// directoryroster.v1, from the same handler, and a call to one that has
// no such method is refused as unimplemented on both.
func TestTheOperatorServicesAnswerUnderBothNames(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	registerRPC(mux, named{})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	current, err := sluisv1connect.NewAccessServiceClient(server.Client(), server.URL).
		ListHolders(t.Context(), connect.NewRequest(&sluisv1.ListHoldersRequest{Group: "g"}))
	if err != nil {
		t.Fatalf("sluis.v1.AccessService/ListHolders: %v", err)
	}
	legacy, err := directoryrosterv1connect.NewAccessServiceClient(server.Client(), server.URL).
		ListHolders(t.Context(), connect.NewRequest(&directoryrosterv1.ListHoldersRequest{Group: "g"}))
	if err != nil {
		t.Fatalf("directoryroster.v1.AccessService/ListHolders: %v", err)
	}
	if current.Msg.GetPolicyDigest() != "for g" || legacy.Msg.GetPolicyDigest() != "for g" {
		t.Errorf("answers = %q (sluis.v1), %q (legacy), want %q from both",
			current.Msg.GetPolicyDigest(), legacy.Msg.GetPolicyDigest(), "for g")
	}

	// A method the handler leaves unimplemented says so under the new
	// name, which is what a client's fallback keys on.
	_, err = sluisv1connect.NewWorkspaceServiceClient(server.Client(), server.URL).
		ListWorkspaces(t.Context(), connect.NewRequest(&sluisv1.ListWorkspacesRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("an unimplemented method under sluis.v1 = %v, want unimplemented", err)
	}
}

// Each legacy service has its sluis.v1 twin on the mux: a service added to
// one list and not the other would be reachable by half its callers.
func TestEveryLegacyServiceHasANewNameOnTheMux(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	registerRPC(mux, named{})

	pairs := map[string]string{
		directoryrosterv1connect.WorkspaceServiceName:          sluisv1connect.WorkspaceServiceName,
		directoryrosterv1connect.SettingsServiceName:           sluisv1connect.SettingsServiceName,
		directoryrosterv1connect.AccessServiceName:             sluisv1connect.AccessServiceName,
		directoryrosterv1connect.GitHubServiceName:             sluisv1connect.GitHubServiceName,
		directoryrosterv1connect.SlackAppServiceName:           sluisv1connect.SlackAppServiceName,
		directoryrosterv1connect.SlackSharedChannelServiceName: sluisv1connect.SlackSharedChannelServiceName,
		directoryrosterv1connect.SlackChannelServiceName:       sluisv1connect.SlackChannelServiceName,
		directoryrosterv1connect.SlackServiceName:              sluisv1connect.SlackServiceName,
		directoryrosterv1connect.CloudflareServiceName:         sluisv1connect.CloudflareServiceName,
	}
	for legacy, current := range pairs {
		for _, name := range []string{legacy, current} {
			request := httptest.NewRequest(http.MethodPost, "/"+name+"/NoSuchMethod", nil)
			request.Header.Set("Content-Type", "application/json")
			_, pattern := mux.Handler(request)
			if pattern != "/"+name+"/" {
				t.Errorf("%s is not on the mux (pattern %q)", name, pattern)
			}
		}
	}
}
