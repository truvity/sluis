package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/audit"
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
	f.retried = f.retried || modcall.Retryable(ctx)
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

// The catalogue pages answer the same through the module in this process and
// through the module behind an HTTP Service, and the install's authorize URL
// comes back carrying the console's state either way.
func TestTheSlackCataloguePagesAnswerTheSameLocallyAndOverTheTransport(t *testing.T) {
	h := newSlackHarness(t)
	ctx := operator()
	local := h.app(t, "sync")

	module := slackconsole.NewServer(h.console.slackSvc())
	ts := httptest.NewServer(module.Handler(func(_ context.Context, bearer string) (string, error) { return bearer, nil }))
	defer ts.Close()
	h.server.UseSlack(&modcall.HTTPCaller{
		URLs:     map[string]string{"slack": ts.URL},
		Token:    func(context.Context, string) (string, error) { return consolecall.Class, nil },
		MaxBytes: consolecall.ListMaxBytes,
	})
	if remote := h.app(t, "sync"); !proto.Equal(local, remote) {
		t.Errorf("the App differs over the transport:\nlocal  %v\nremote %v", local, remote)
	}
	if err := h.create(ctx, "sync", accepted); err != nil {
		t.Fatalf("Create over the transport: %v", err)
	}
	begun, done := h.install(t, "sync", "", acmeTeam)
	if mustQuery(t, begun.Msg.GetUrl(), "state") == "" || mustQuery(t, begun.Msg.GetUrl(), "scope") != "channels:read,users:read" {
		t.Errorf("authorize URL = %s", begun.Msg.GetUrl())
	}
	if code, location, body := done(); code != 302 || !strings.HasSuffix(location, "/#/slack-apps") {
		t.Errorf("callback over the transport = %d %s %s", code, location, body)
	}
	if got := h.app(t, "sync"); got.GetState() != slackAppInstalled {
		t.Errorf("after the install over the transport = %+v", got)
	}
}

// An operator of one directory may neither create nor install an App in a
// workspace another directory owns: the module refuses on its own records.
func TestTheSlackModuleRefusesAnAppOfAnotherDirectoryAndListsOnlyWhatIsVisible(t *testing.T) {
	h := newSlackHarness(t)
	foreign := asSlackIdentity(southOp)
	if err := h.create(foreign, "sync", accepted); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("Create by another directory's operator = %v, want permission denied", err)
	}
	if _, err := h.console.InstallSlackApp(foreign, connect.NewRequest(&directoryrosterv1.InstallSlackAppRequest{Id: "sync"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("Install by another directory's operator = %v, want permission denied", err)
	}
	if h.slack.Count("apps.manifest.create") != 0 {
		t.Error("a refused create reached Slack")
	}
	listed, err := h.console.ListSlackApps(foreign, connect.NewRequest(&directoryrosterv1.ListSlackAppsRequest{}))
	if err == nil {
		for _, app := range listed.Msg.GetApps() {
			if app.GetWorkspace() == "acme" {
				t.Errorf("another directory sees %+v", app)
			}
		}
	}
}

// A catalogue callback the module cannot answer is refused once, in words, and
// the call is marked as one the transport must not repeat.
func TestACatalogueCallbackTheModuleCannotAnswerIsRefusedOnceAndNeverRepeated(t *testing.T) {
	h := newSlackHarness(t)
	if err := h.create(operator(), "sync", accepted); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, done := h.install(t, "sync", "", acmeTeam)
	lost := &failing{returned: modcall.ErrTransport}
	h.server.UseSlack(lost)
	code, _, body := done()
	if code < 400 || code >= 500 || !strings.Contains(body, "did not answer") {
		t.Errorf("callback = %d %s", code, body)
	}
	if lost.calls != 1 || lost.retried {
		t.Errorf("the module was called %d times, retryable %v; want once and not retryable", lost.calls, lost.retried)
	}
	h.server.UseSlack(nil)
	if got := h.app(t, "sync"); got.GetState() != slackAppCreated {
		t.Errorf("an install nobody answered left the App %s", got.GetState())
	}
}

// controllerTrigger notes who is asked for a tick, and by which controller.
type controllerTrigger struct {
	mu   sync.Mutex
	asks []string
}

func (c *controllerTrigger) Notify(_ context.Context, target string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asks = append(c.asks, "plain:"+target)
	return nil
}

func (c *controllerTrigger) NotifyController(_ context.Context, controller, target string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asks = append(c.asks, controller+":"+target)
	return nil
}

func (*controllerTrigger) Subscribe(func(string)) func() { return func() {} }

// The channel pages answer the same through the module in this process and
// through the module behind an HTTP Service; a write is the module's own tick
// request, naming the Slack controller; and the trail names each listed person
// by a minted identifier, never by address.
func TestTheSlackChannelPagesWorkOverTheTransportTickTheModuleAndMintTheTrail(t *testing.T) {
	h := newConnectHarness(t)
	ticks := &controllerTrigger{}
	h.console.deps.Trigger = ticks
	ctx := as(northOp)

	module := slackconsole.NewServer(h.console.slackSvc())
	ts := httptest.NewServer(module.Handler(func(_ context.Context, bearer string) (string, error) { return bearer, nil }))
	defer ts.Close()
	h.console.deps.Modules.Slack = &modcall.HTTPCaller{
		URLs:     map[string]string{"slack": ts.URL},
		Token:    func(context.Context, string) (string, error) { return consolecall.Class, nil },
		MaxBytes: consolecall.ListMaxBytes,
	}

	if err := h.createChannel(ctx, withMembers(chdef("acme", "eng", "partners@north.example"), "cy@north.example")); err != nil {
		t.Fatalf("create over the transport: %v", err)
	}
	shared := def("partners", "acme", []string{"globex"}, []string{"partners@north.example"})
	shared.Members = []string{"cy@north.example"}
	if err := h.create(ctx, shared); err != nil {
		t.Fatalf("create a shared channel over the transport: %v", err)
	}
	if listed := h.listChannels(ctx, t); len(listed.GetChannels()) != 1 || listed.GetChannels()[0].GetChannel().GetName() != "eng" {
		t.Errorf("listed over the transport = %v", listed.GetChannels())
	}
	if got := h.list(ctx, t); len(got.GetChannels()) != 1 || got.GetChannels()[0].GetChannel().GetName() != "partners" {
		t.Errorf("listed shared channels over the transport = %v", got.GetChannels())
	}

	// The module asked for the tick itself, naming the controller: the host
	// and the guest of the shared channel, and the channel's workspace.
	ticks.mu.Lock()
	asks := slices.Clone(ticks.asks)
	ticks.mu.Unlock()
	for _, want := range []string{"slack:acme", "slack:globex"} {
		if !slices.Contains(asks, want) {
			t.Errorf("no tick was asked for as %q: %v", want, asks)
		}
	}
	for _, ask := range asks {
		if strings.HasPrefix(ask, "plain:") {
			t.Errorf("a tick was asked for without naming the controller: %v", asks)
		}
	}

	// The trail names the listed person by the minted identifier, and the
	// address is nowhere in the record.
	minted := audit.DirectoryUserID("cy@north.example")
	for _, action := range []string{"roster.slack_console_channel.created", "roster.slack_shared_channel.created"} {
		found := h.recorded.Find(action)
		if len(found) != 1 {
			t.Fatalf("%s: %d records", action, len(found))
		}
		var users []string
		for _, tg := range found[0].GetTargets() {
			if tg.GetType() == "directory_user" {
				users = append(users, tg.GetId())
			}
		}
		if !slices.Equal(users, []string{minted}) {
			t.Errorf("%s: directory_user targets %v, want the minted %s", action, users, minted)
		}
		if strings.Contains(found[0].String(), "cy@north.example") {
			t.Errorf("%s: the record carries the address: %v", action, found[0])
		}
	}
}

// The module asks the owner question of the channel's workspace on the snapshot:
// an operator of another directory is refused a channel and a shared channel in
// a workspace it does not own, though it is the console class that asks.
func TestTheSlackModuleRefusesAChannelOfAWorkspaceTheCallerDoesNotOwn(t *testing.T) {
	h := newConnectHarness(t)
	module := h.console.slackModule()
	south := consolecall.Caller{Actor: "sam@south.example", Workspaces: map[string]string{"C0south": "operator"}}
	_, err := consolecall.Proxy[*directoryrosterv1.CreateSlackChannelRequest, *directoryrosterv1.CreateSlackChannelResponse](
		context.Background(), module, slackconsole.Module, slackconsole.MethodCreateChannel, south,
		&directoryrosterv1.CreateSlackChannelRequest{Channel: chdef("acme", "eng", "partners@north.example")})
	var coded *modcall.Error
	if !errors.As(err, &coded) || coded.Code != modcall.CodeForbidden {
		t.Errorf("a channel in another directory's workspace = %v, want forbidden", err)
	}
	_, err = consolecall.Proxy[*directoryrosterv1.CreateSlackSharedChannelRequest, *directoryrosterv1.CreateSlackSharedChannelResponse](
		context.Background(), module, slackconsole.Module, slackconsole.MethodCreateSharedChannel, south,
		&directoryrosterv1.CreateSlackSharedChannelRequest{Channel: def("partners", "acme", []string{"globex"}, nil)})
	if !errors.As(err, &coded) || coded.Code != modcall.CodeForbidden {
		t.Errorf("a shared channel hosted in another directory's workspace = %v, want forbidden", err)
	}
	if len(h.storedChannels(t)) != 0 {
		t.Error("a refused call wrote a record")
	}
}
