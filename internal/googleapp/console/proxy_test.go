package console_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/backend/fake"
	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/consolecall"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/modcall"
)

// moduleAs is the module's server called as a caller class, in process, through
// the same JSON a Lambda invoke carries.
func moduleAs(svc *googleconsole.Service, class string) modcall.Caller {
	return modcall.Local{googleconsole.Module: svc.Server()}.As(class)
}

func adopted(t *testing.T) *googleconsole.Service {
	t.Helper()
	h := quietHub()
	b := fake.New("C0north", "north.example").WithAccount("ada@north.example", "Ada", "North")
	if _, err := h.Adopt(context.Background(), hub.Workspace{Admin: "admin@north.example"}, b); err != nil {
		t.Fatal(err)
	}
	return googleconsole.NewService(h, []googleconsole.Connector{stubConsent{}}, nil, slog.New(slog.DiscardHandler))
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var coded *modcall.Error
	if !errors.As(err, &coded) {
		t.Fatalf("not a coded error: %v", err)
	}
	return coded.Code
}

// The proxy round trip: the console's call, as a snapshot, reaches the module's
// service and the answer comes back as the contract's message.
func TestTheConsoleAsksTheModuleAndGetsTheContractsAnswer(t *testing.T) {
	t.Parallel()
	svc := adopted(t)
	client := googleconsole.NewClient(moduleAs(svc, consolecall.Class))
	ctx := context.Background()

	list, err := client.ListWorkspaces(ctx, operator)
	if err != nil || len(list.GetWorkspaces()) != 1 || list.GetWorkspaces()[0].GetId() != "C0north" {
		t.Fatalf("%v, %v", list, err)
	}
	set, err := client.SetServedDomains(ctx, operator, &directoryrosterv1.SetServedDomainsRequest{
		WorkspaceId: "C0north", Domains: []string{"north.example"}})
	if err != nil || len(set.GetWorkspace().GetDomains()) == 0 {
		t.Fatalf("%v, %v", set, err)
	}
	if _, err = client.Probe(ctx, operator, &directoryrosterv1.ProbeRequest{WorkspaceId: "C0north"}); err != nil {
		t.Fatal(err)
	}
	// A workspace that is not there is the module's answer, with its code.
	_, err = client.Refresh(ctx, operator, &directoryrosterv1.RefreshRequest{WorkspaceId: "C0nowhere"})
	if got := connect.CodeOf(googleconsole.ConnectError(err)); got != connect.CodeNotFound {
		t.Errorf("a refresh of an unknown workspace = %v (%v), want NotFound", got, err)
	}
	if _, err = client.Disconnect(ctx, operator, &directoryrosterv1.DisconnectRequest{WorkspaceId: "C0north"}); err != nil {
		t.Fatal(err)
	}
	list, err = client.ListWorkspaces(ctx, operator)
	if err != nil || len(list.GetWorkspaces()) != 0 {
		t.Errorf("a disconnected workspace is still listed: %v, %v", list, err)
	}
}

// The module re-checks the snapshot it is given, in fixed words: a viewer lists
// but does not act, and a role over one workspace does not reach another.
func TestTheModuleChecksTheSnapshotAndRefusesInFixedWords(t *testing.T) {
	t.Parallel()
	client := googleconsole.NewClient(moduleAs(adopted(t), consolecall.Class))
	ctx := context.Background()

	if _, err := client.ListWorkspaces(ctx, viewer); err != nil {
		t.Errorf("a viewer cannot list: %v", err)
	}
	_, err := client.Disconnect(ctx, viewer, &directoryrosterv1.DisconnectRequest{WorkspaceId: "C0north"})
	if got := connect.CodeOf(googleconsole.ConnectError(err)); got != connect.CodePermissionDenied {
		t.Errorf("a viewer's disconnect = %v (%v), want PermissionDenied", got, err)
	}
	scoped := consolecall.Caller{Actor: "oz@other.example", Workspaces: map[string]string{"C0other": "operator"}}
	_, err = client.Disconnect(ctx, scoped, &directoryrosterv1.DisconnectRequest{WorkspaceId: "C0north"})
	var coded *modcall.Error
	if !errors.As(err, &coded) || coded.Message != googleconsole.RefusalText {
		t.Errorf("another directory's operator was refused in words that name why: %v", err)
	}
}

// The class is the transport's: a console method answers the console alone, and
// the sign-in answers the issuer alone.
func TestEachMethodAnswersItsOwnClassOnly(t *testing.T) {
	t.Parallel()
	svc := adopted(t)
	ctx := context.Background()

	if v := consolecall.Violations(svc.Server()); len(v) != 0 {
		t.Errorf("the module breaks the console rule: %v", v)
	}
	// A forbidden class: the issuer calls a console method.
	issuer := googleconsole.NewClient(moduleAs(svc, "issuer"))
	_, err := issuer.ListWorkspaces(ctx, operator)
	if got := codeOf(t, err); got != modcall.CodeForbidden {
		t.Errorf("a console method called as the issuer = %q, want forbidden", got)
	}
	// And a caller with no class at all.
	_, err = googleconsole.NewClient(moduleAs(svc, "")).ListWorkspaces(ctx, operator)
	if got := codeOf(t, err); got != modcall.CodeForbidden {
		t.Errorf("a console method called with no class = %q, want forbidden", got)
	}
	// identify is refused to the console class.
	conn := googleconsole.NewRemoteConnector(moduleAs(svc, consolecall.Class))
	if _, err = conn.Identify(ctx, "code"); codeOf(t, err) != modcall.CodeForbidden {
		t.Errorf("identify called as the console = %v, want forbidden", err)
	}
	if _, err = conn.SignInURL("state"); codeOf(t, err) != modcall.CodeForbidden {
		t.Errorf("sign_in_url called as the console = %v, want forbidden", err)
	}
}

// capture is a logger that keeps everything it is told, at every level.
func capture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// keyConnector takes a key and refuses it, naming the cause as the real one does.
type keyConnector struct{ stubConsent }

func (keyConnector) FromKey(_ context.Context, _ []byte, _ string) (hub.Workspace, backend.Backend, error) {
	return hub.Workspace{}, nil, errors.New("the key is not a service-account key")
}

// A service-account key is the one secret that transits the console on its way
// in. It is bounded at 64 KB, held in memory for the call, and written nowhere:
// not in the log of the console, not in the module's, and not in the error the
// person is shown.
func TestAKeyUploadIsBoundedAndNeverLogged(t *testing.T) {
	t.Parallel()
	log, logged := capture()
	slog.SetDefault(log)
	svc := googleconsole.NewService(quietHub(), []googleconsole.Connector{keyConnector{}}, nil, log)
	client := googleconsole.NewClient(moduleAs(svc, consolecall.Class))
	ctx := context.Background()

	secret := `{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----SECRETMATERIAL"}`
	_, err := client.UploadKey(ctx, operator, &directoryrosterv1.UploadKeyRequest{
		Backend: directoryrosterv1.Backend_BACKEND_GOOGLE, Admin: "admin@north.example", Key: []byte(secret)})
	if got := connect.CodeOf(googleconsole.ConnectError(err)); got != connect.CodeInvalidArgument {
		t.Fatalf("a refused key = %v (%v), want InvalidArgument", got, err)
	}
	if strings.Contains(err.Error(), "SECRETMATERIAL") || strings.Contains(googleconsole.ConnectError(err).Error(), "SECRETMATERIAL") {
		t.Errorf("the error carries the key: %v", err)
	}

	// Over the bound: refused before it is sent, and by the module too.
	big := bytes.Repeat([]byte("K"), googleconsole.MaxKeyBytes+1)
	req := &directoryrosterv1.UploadKeyRequest{Backend: directoryrosterv1.Backend_BACKEND_GOOGLE, Admin: "a@b.example", Key: big}
	_, err = client.UploadKey(ctx, operator, req)
	if got := codeOf(t, err); got != googleconsole.CodeInvalidArgument {
		t.Errorf("a key over 64 KB = %q, want invalid_argument", got)
	}
	_, err = svc.UploadKey(ctx, operator, req)
	if got := codeOf(t, err); got != googleconsole.CodeInvalidArgument {
		t.Errorf("the module accepted a key over 64 KB: %q", got)
	}
	exact := &directoryrosterv1.UploadKeyRequest{Backend: directoryrosterv1.Backend_BACKEND_GOOGLE, Admin: "a@b.example",
		Key: bytes.Repeat([]byte("K"), googleconsole.MaxKeyBytes)}
	if _, err = client.UploadKey(ctx, operator, exact); codeOf(t, err) != googleconsole.CodeInvalidArgument ||
		strings.Contains(err.Error(), "at most") {
		t.Errorf("a key of exactly 64 KB was refused as too large: %v", err)
	}

	if out := logged.String(); strings.Contains(out, "SECRETMATERIAL") || strings.Contains(out, "KKKKKKKK") {
		t.Errorf("a key reached the log:\n%.400s", out)
	}
}

// failing is a transport that never answers, and records how it was called.
type failing struct {
	err      error
	noRetry  bool
	deadline time.Duration
	method   string
}

func (f *failing) Call(ctx context.Context, _, method string, _ []byte) ([]byte, error) {
	f.noRetry = modcall.NoRetry(ctx)
	f.method = method
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	return nil, f.err
}

// The consent's code is single-use, so the forward is never retried by the
// transport, is bounded at about 25 s, and a failure to deliver it is the
// transport's error and not a success.
func TestTheConsentForwardIsNeverRetriedAndIsBounded(t *testing.T) {
	t.Parallel()
	f := &failing{err: modcall.ErrTransport}
	client := googleconsole.NewClient(f)
	_, err := client.CompleteConsent(context.Background(), operator, googleconsole.CompleteRequest{Backend: "google", Code: "x"})
	if !errors.Is(err, modcall.ErrTransport) {
		t.Fatalf("a lost forward = %v, want the transport's error", err)
	}
	if f.method != googleconsole.MethodCompleteConsent || !f.noRetry {
		t.Errorf("the forward was sent as %q, no-retry %v", f.method, f.noRetry)
	}
	if f.deadline <= 20*time.Second || f.deadline > googleconsole.CompleteDeadline {
		t.Errorf("the forward's deadline = %v, want about 25 s", f.deadline)
	}
	if got := connect.CodeOf(googleconsole.ConnectError(err)); got != connect.CodeUnavailable {
		t.Errorf("a lost forward reaches the person as %v, want Unavailable", got)
	}
	// And the sign-in's identify is single-use too.
	f = &failing{err: modcall.ErrTransport}
	if _, err = googleconsole.NewRemoteConnector(f).Identify(context.Background(), "c"); err == nil || !f.noRetry {
		t.Errorf("identify: %v, no-retry %v", err, f.noRetry)
	}
}
