package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

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
	got, err := console.GetSlackStatus(operator(), connect.NewRequest(&directoryrosterv1.GetSlackStatusRequest{}))
	if err != nil {
		t.Fatalf("GetSlackStatus: %v", err)
	}
	if len(got.Msg.GetWorkspaces()) != 3 || !got.Msg.GetConnectingAvailable() {
		t.Errorf("the page = %v", got.Msg)
	}
	if _, err = console.GetSlackStatus(context.Background(), connect.NewRequest(&directoryrosterv1.GetSlackStatusRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("an anonymous page = %v", err)
	}
}
