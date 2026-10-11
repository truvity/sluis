package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
)

// A Slack page answers the same through the module in this process and through
// the module behind an HTTP Service, called as the console class: the proxy
// does not know the difference.
func TestTheSlackPagesAnswerTheSameLocallyAndOverTheTransport(t *testing.T) {
	h := newConnectedWorkspaceHarness(t)
	ctx := operator()
	local := h.status(ctx, t)
	if len(local.GetWorkspaces()) == 0 {
		t.Fatal("the local page is empty")
	}

	module := slackconsole.NewServer(h.console.slackSvc())
	ts := httptest.NewServer(module.Handler(
		func(_ context.Context, bearer string) (string, error) { return bearer, nil }))
	defer ts.Close()
	h.server.UseSlack(&modcall.HTTPCaller{
		URLs:     map[string]string{"slack": ts.URL},
		Token:    func(context.Context, string) (string, error) { return consolecall.Class, nil },
		MaxBytes: consolecall.ListMaxBytes,
	})
	remote := h.status(ctx, t)
	if !proto.Equal(local, remote) {
		t.Errorf("the page differs over the transport:\nlocal  %v\nremote %v", local, remote)
	}

	// A write goes the same way, and what is told comes back as the Connect error.
	if _, err := h.disconnect(ctx, "no where", false); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("disconnect of a key that is not one = %v, want invalid argument", err)
	}
	if _, err := h.begin(ctx, "nowhere", accepted); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("connect to a workspace the policy does not name = %v, want failed precondition", err)
	}
	// A caller that is not signed in is refused before the module is asked.
	_, err := h.console.GetSlackStatus(context.Background(), connect.NewRequest(&directoryrosterv1.GetSlackStatusRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("an anonymous page = %v", err)
	}
}

// The module answers the class `console` and no other: the issuer, a
// controller, an operator's tool and a call the transport cannot name are all
// refused before a method runs, whatever the payload claims.
func TestTheSlackConsoleMethodsRefuseEveryOtherClass(t *testing.T) {
	h := newConnectedWorkspaceHarness(t)
	module := slackconsole.NewServer(h.console.slackSvc())
	if v := consolecall.Violations(module); len(v) != 0 {
		t.Fatalf("the module's methods break the console rule: %q", v)
	}
	methods := module.Methods()
	if len(methods) < 7 {
		t.Fatalf("the module registers %d console methods", len(methods))
	}
	local := modcall.Local{slackconsole.Module: module}
	for _, class := range []string{"issuer", "github", "admin", "breakglass", ""} {
		for _, m := range methods {
			_, err := local.As(class).Call(context.Background(), slackconsole.Module, m.Name,
				[]byte(`{"caller":{"actor":"root@example.test","roles":["admin","operator"]}}`))
			var coded *modcall.Error
			if !errors.As(err, &coded) || coded.Code != modcall.CodeForbidden {
				t.Errorf("%s as %q = %v, want forbidden", m.Name, class, err)
			}
		}
	}
}

// The module asks the role question of its own records, on the snapshot: a
// snapshot with no role, or the role over another directory, is refused even
// though only the console class can send it.
func TestTheSlackModuleRefusesACallerWhoseSnapshotLacksTheRole(t *testing.T) {
	h := newConnectedWorkspaceHarness(t)
	module := h.console.slackModule()
	for name, caller := range map[string]consolecall.Caller{
		"no actor":               {},
		"no role":                {Actor: "eve@north.example"},
		"a viewer disconnecting": {Actor: "eve@north.example", Roles: []string{"viewer"}},
		"the other directory's operator": {
			Actor: "sam@south.example", Workspaces: map[string]string{"C0south": "operator"},
		},
	} {
		_, err := consolecall.Proxy[*directoryrosterv1.DisconnectSlackWorkspaceRequest, *directoryrosterv1.DisconnectSlackWorkspaceResponse](
			context.Background(), module, slackconsole.Module, slackconsole.MethodDisconnectWorkspace, caller,
			&directoryrosterv1.DisconnectSlackWorkspaceRequest{Workspace: "acme"})
		var coded *modcall.Error
		if !errors.As(err, &coded) || (coded.Code != modcall.CodeForbidden && coded.Code != "unauthenticated") {
			t.Errorf("%s = %v, want a refusal", name, err)
		}
		if _, _, found, _ := h.workspaces.Get(context.Background(), "acme"); !found {
			t.Fatalf("%s: the workspace was disconnected", name)
		}
	}
}

// failing is a module that cannot be reached: it counts the calls and notes
// whether the transport was told not to repeat them.
type failing struct {
	mu       sync.Mutex
	calls    int
	retried  bool
	returned error
}

func (f *failing) Call(ctx context.Context, _, _ string, _ []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.retried = f.retried || !modcall.NoRetry(ctx)
	return nil, f.returned
}

// A callback spends Slack's single-use code. When the module cannot answer, the
// page says so in words and a 4xx, the call is sent once and marked as one the
// transport must not repeat, and nothing is stored.
func TestACallbackTheModuleCannotAnswerIsRefusedOnceAndNeverRepeated(t *testing.T) {
	h := newWorkspaceHarness(t)
	ctx := operator()
	begun, err := h.beginOwned(ctx, "acme", accepted, "C0north")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	cookie, state := cookieFrom(t, begun.Header()), mustQuery(t, begun.Msg.GetUrl(), "state")
	_, credential, _, _ := h.workspaces.Get(context.Background(), "acme")
	code := h.slack.Install(credential.AppID, acmeTeam)

	lost := &failing{returned: modcall.ErrTransport}
	h.server.UseSlack(lost)
	got := redirect(h.server.slackWorkspaceCallback, slackconsole.WorkspaceCallbackPath, url.Values{"code": {code}, "state": {state}}, cookie)
	if got.Code < 400 || got.Code >= 500 {
		t.Errorf("status = %d, want a 4xx", got.Code)
	}
	if body := got.Body.String(); !strings.Contains(body, "did not answer") || !strings.Contains(body, "start the install again") {
		t.Errorf("page = %s", body)
	}
	if strings.Contains(got.Body.String(), code) {
		t.Error("the page carries the code")
	}
	if lost.calls != 1 || lost.retried {
		t.Errorf("the module was called %d times, retryable %v; want once and not retryable", lost.calls, lost.retried)
	}
	if record, _, _, _ := h.workspaces.Get(context.Background(), "acme"); record.Installed() {
		t.Error("the workspace was installed by a call nobody answered")
	}
	if !strings.Contains(h.logs.String(), "a Slack install callback was refused") {
		t.Errorf("the refusal left no log line: %s", h.logs.String())
	}
}
