package server

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
)

// A console made by NewConsole, as the service makes it, calls the Slack module
// in its own process: nothing is wired for it beyond the stores.
func TestANewConsoleCallsTheSlackModuleInItsOwnProcess(t *testing.T) {
	built := newBareSlackHarness(t)
	console, err := NewConsole(context.Background(), ConsoleDeps{
		Authorizer: built.console.deps.Authorizer, SlackWorkspaces: built.workspaces, Hub: built.console.deps.Hub,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := console.GetSlackStatus(operator(), &directoryrosterv1.GetSlackStatusRequest{})
	if err != nil {
		t.Fatalf("GetSlackStatus: %v", err)
	}
	if len(got.GetWorkspaces()) != 3 || !got.GetConnectingAvailable() {
		t.Errorf("the page = %v", got)
	}
	if _, err = console.GetSlackStatus(
		context.Background(), &directoryrosterv1.GetSlackStatusRequest{},
	); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("an anonymous page = %v", err)
	}
}
