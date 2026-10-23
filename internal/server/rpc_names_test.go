package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/gen/directoryroster/v1/directoryrosterv1connect"
	sluisv1 "github.com/truvity/sluis/gen/sluis/v1"
	"github.com/truvity/sluis/gen/sluis/v1/sluisv1connect"
	"github.com/truvity/sluis/internal/refusal"
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
	directoryrosterv1connect.UnimplementedBackupServiceHandler
	directoryrosterv1connect.UnimplementedOutlineServiceHandler
}

func (named) ListHolders(
	_ context.Context, req *directoryrosterv1.ListHoldersRequest,
) (*directoryrosterv1.ListHoldersResponse, error) {
	return &directoryrosterv1.ListHoldersResponse{PolicyDigest: "for " + req.GetGroup()}, nil
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

	current, err := sluisv1connect.NewAccessServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL))).
		ListHolders(t.Context(), &sluisv1.ListHoldersRequest{Group: "g"})
	if err != nil {
		t.Fatalf("sluis.v1.AccessService/ListHolders: %v", err)
	}
	legacy, err := directoryrosterv1connect.NewAccessServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL))).
		ListHolders(t.Context(), &directoryrosterv1.ListHoldersRequest{Group: "g"})
	if err != nil {
		t.Fatalf("directoryroster.v1.AccessService/ListHolders: %v", err)
	}
	if current.GetPolicyDigest() != "for g" || legacy.GetPolicyDigest() != "for g" {
		t.Errorf("answers = %q (sluis.v1), %q (legacy), want %q from both",
			current.GetPolicyDigest(), legacy.GetPolicyDigest(), "for g")
	}

	// A method the handler leaves unimplemented says so under the new
	// name, which is what a client's fallback keys on.
	_, err = sluisv1connect.NewWorkspaceServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL))).
		ListWorkspaces(t.Context(), &sluisv1.ListWorkspacesRequest{})
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
		directoryrosterv1connect.BackupServiceName:             sluisv1connect.BackupServiceName,
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

// refusing answers with the reasons a maintainer writes where a call is
// refused: a role named, a store's error.
type refusing struct{ named }

func (refusing) ListHolders(
	context.Context, *directoryrosterv1.ListHoldersRequest,
) (*directoryrosterv1.ListHoldersResponse, error) {
	return nil, connect.NewError(connect.CodePermissionDenied,
		"this needs the operator role over acme's directory")
}

func (refusing) ListWorkspaces(
	context.Context, *directoryrosterv1.ListWorkspacesRequest,
) (*directoryrosterv1.ListWorkspacesResponse, error) {
	return nil, connect.NewError(connect.CodeInternal, "pq: connection to 10.1.2.3 refused")
}

// Every operator service is behind the interceptor, under both names: what
// a handler wrote as the reason is not what the caller reads.
func TestTheOperatorServicesRefuseInFixedWords(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	registerRPC(mux, refusing{})
	server := httptest.NewServer(refusal.Middleware(mux))
	t.Cleanup(server.Close)

	for name, clients := range map[string]struct {
		denied   func() error
		internal func() error
	}{
		"sluis.v1": {
			func() error {
				_, err := sluisv1connect.NewAccessServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL))).
					ListHolders(t.Context(), &sluisv1.ListHoldersRequest{Group: "g"})
				return err
			},
			func() error {
				_, err := sluisv1connect.NewWorkspaceServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL))).
					ListWorkspaces(t.Context(), &sluisv1.ListWorkspacesRequest{})
				return err
			},
		},
		"directoryroster.v1": {
			func() error {
				_, err := directoryrosterv1connect.NewAccessServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL))).
					ListHolders(t.Context(), &directoryrosterv1.ListHoldersRequest{Group: "g"})
				return err
			},
			func() error {
				_, err := directoryrosterv1connect.NewWorkspaceServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL))).
					ListWorkspaces(t.Context(), &directoryrosterv1.ListWorkspacesRequest{})
				return err
			},
		},
	} {
		var denied, failed *connect.Error
		if !errors.As(clients.denied(), &denied) || denied.Code() != connect.CodePermissionDenied ||
			!strings.HasPrefix(denied.Message(), "not authorised (request ") || strings.Contains(denied.Message(), "acme") {
			t.Errorf("%s: a denied call = %v, want permission_denied \"not authorised (request <id>)\"", name, denied)
		}
		if !errors.As(clients.internal(), &failed) || failed.Code() != connect.CodeInternal ||
			!strings.HasPrefix(failed.Message(), "server error (request ") || strings.Contains(failed.Message(), "10.1.2.3") {
			t.Errorf("%s: a failed call = %v, want internal \"server error (request <id>)\"", name, failed)
		}
	}
}
