package issuer_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/gen/accessissuer/v1/accessissuerv1connect"
)

// The session service lists and ends sessions, so a bearer there is held to
// what the other operator endpoints hold it to: the access token of a live
// sign-in at the console or the CLI. A token this issuer signed for somebody
// else (a relying party's, an ID token, a workload's exchange) is a valid
// signature and no more, and must not act as the person it names.

func listWith(t *testing.T, rig *sluisctlRig, bearer string) error {
	t.Helper()
	client := accessissuerv1connect.NewSessionServiceClient(rig.server.Client(), rig.server.URL)
	req := connect.NewRequest(&accessissuerv1.ListSessionsRequest{Identity: "ada@north.example"})
	req.Header().Set("Authorization", "Bearer "+bearer)
	_, err := client.ListSessions(context.Background(), req)
	return err
}

func TestTheSessionServiceAcceptsTheAccessTokenOfALiveCLISignIn(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	if err := listWith(t, rig, rig.access(t, "sluisctl")); err != nil {
		t.Errorf("a live CLI sign-in's access token: %v", err)
	}
}

func TestTheSessionServiceRefusesATokenThatIsNotALiveCLIOrConsoleSignIn(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	_, exchanged := exchange(t, rig.server, "k8s:devel/identity-system/authorization-webhook", "directory-roster")
	workload, _ := exchanged["access_token"].(string)

	for name, bearer := range map[string]string{
		"the CLI's ID token":        func() string { s, _ := rig.tokens(t, "sluisctl")["id_token"].(string); return s }(),
		"another client's ID token": func() string { s, _ := rig.tokens(t, "local-dev")["id_token"].(string); return s }(),
		"another client's access":   rig.access(t, "local-dev"),
		"a workload's exchange":     workload,
		"a made-up token":           "not-a-token",
		"a forged unsigned token":   "eyJhbGciOiJub25lIn0.e30.",
		"a one-letter bearer":       "x",
	} {
		if bearer == "" {
			t.Fatalf("%s: the rig produced no token", name)
		}
		err := listWith(t, rig, bearer)
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s: %v, want unauthenticated", name, err)
		}
	}
}

func TestTheSessionServiceStopsAcceptingATokenWhenItsSessionEnds(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	token := rig.access(t, "sluisctl")
	if err := listWith(t, rig, token); err != nil {
		t.Fatalf("before: %v", err)
	}
	rig.revokeAll(t)
	if err := listWith(t, rig, token); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("after the session ended: %v, want unauthenticated", err)
	}
}
