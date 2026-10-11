package googlefn_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/lambdacontext"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	googleapp "github.com/truvity/sluis/internal/googleroster/app"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/lambdaapp/googlefn"
	"github.com/truvity/sluis/internal/modcall"
)

// puller is the assembled module, in place of one over Google.
type puller struct {
	out    hub.RefreshOutcome
	err    error
	all    hub.RefreshResult
	passed []string
}

func (p *puller) Pass(_ context.Context, ws string, unsafeLocal bool) (hub.RefreshOutcome, error) {
	p.passed = append(p.passed, ws)
	if unsafeLocal {
		return "", errors.New("a function must not opt in to a lease held in memory")
	}
	return p.out, p.err
}

func (p *puller) RefreshPass(context.Context) (hub.RefreshResult, error) {
	p.passed = append(p.passed, "*")
	return p.all, p.err
}

func handler(p *puller) *lambdaapp.HTTP {
	return googlefn.Handler(nil, func(ctx context.Context, ws string) (lambdaapp.RefreshResult, error) {
		return googlefn.Refresh(ctx, p, ws)
	}, func(context.Context) (deploycheck.Report, error) { return deploycheck.Report{OK: true}, nil })
}

func handle(t *testing.T, h *lambdaapp.HTTP, payload string) (lambdaapp.RefreshResult, error) {
	t.Helper()
	out, err := h.Handle(context.Background(), json.RawMessage(payload))
	if err != nil {
		return lambdaapp.RefreshResult{}, err
	}
	return out.(lambdaapp.RefreshResult), nil
}

func TestARefreshEventRefreshesEveryWorkspace(t *testing.T) {
	p := &puller{all: hub.RefreshResult{Workspaces: 3, Ran: 2, Contended: 1}}
	res, err := handle(t, handler(p), `{"kind":"refresh"}`)
	want := lambdaapp.RefreshResult{Kind: "refresh", Workspaces: 3, Ran: 2, Contended: 1}
	if err != nil || res != want {
		t.Errorf("%+v, %v; want %+v", res, err, want)
	}
	if !slices.Equal(p.passed, []string{"*"}) {
		t.Errorf("passes: %v", p.passed)
	}
}

func TestARefreshEventCanNameOneWorkspace(t *testing.T) {
	for out, want := range map[hub.RefreshOutcome]lambdaapp.RefreshResult{
		hub.RefreshRan:       {Kind: "refresh", Workspaces: 1, Ran: 1},
		hub.RefreshContended: {Kind: "refresh", Workspaces: 1, Contended: 1},
		hub.RefreshPaused:    {Kind: "refresh", Workspaces: 1, Paused: 1},
	} {
		p := &puller{out: out}
		res, err := handle(t, handler(p), `{"kind":"refresh","workspace":"C0123abcd"}`)
		if err != nil || res != want {
			t.Errorf("%s: %+v, %v; want %+v", out, res, err, want)
		}
		if !slices.Equal(p.passed, []string{"C0123abcd"}) {
			t.Errorf("%s: passes: %v", out, p.passed)
		}
	}
}

func TestAFailedOrUnknownRefreshIsAnError(t *testing.T) {
	failed := &puller{out: hub.RefreshFailed, err: errors.New("the refresh of C0123abcd failed")}
	if _, err := handle(t, handler(failed), `{"kind":"refresh","workspace":"C0123abcd"}`); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Errorf("a failed refresh: %v", err)
	}
	if _, err := handle(t, handler(&puller{all: hub.RefreshResult{Workspaces: 2, Ran: 1, Failed: 1}}), `{"kind":"refresh"}`); err == nil {
		t.Error("a pass with a failed workspace succeeded")
	}
	unknown := &puller{err: googleapp.ErrUnknownWorkspace}
	if _, err := handle(t, handler(unknown), `{"kind":"refresh","workspace":"nobody"}`); !errors.Is(err, googleapp.ErrUnknownWorkspace) {
		t.Errorf("an unknown workspace: %v", err)
	}
}

func TestARefreshEventNamingAPathIsRefused(t *testing.T) {
	p := &puller{}
	for _, ws := range []string{"../x", "a/b", strings.Repeat("a", 200), "a\\nb"} {
		payload, _ := json.Marshal(map[string]string{"kind": "refresh", "workspace": ws})
		if _, err := handle(t, handler(p), string(payload)); err == nil {
			t.Errorf("workspace %q was accepted", ws)
		}
	}
	if len(p.passed) != 0 {
		t.Errorf("a refresh ran for a refused event: %v", p.passed)
	}
}

// The function takes no tick, no run and no module call: only the refresh and
// the check.
func TestTheFunctionTakesOnlyRefreshAndCheck(t *testing.T) {
	p := &puller{}
	h := handler(p)
	for _, payload := range []string{`{"kind":"tick","target":"C0123abcd"}`, `{"kind":"cloudflare"}`} {
		if _, err := h.Handle(context.Background(), json.RawMessage(payload)); err == nil {
			t.Errorf("%s was answered", payload)
		}
	}
	// A run-now is sent to every controller, and one that runs no target ends it
	// cleanly with nothing done.
	if out, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"run","target":"C0123abcd"}`)); err != nil ||
		out.(lambdaapp.Result).Outcome != lambdaapp.OutcomeUnknown {
		t.Errorf("run: %v, %v", out, err)
	}
	raw, _ := json.Marshal(modcall.Request{Kind: modcall.Kind, Module: "google", Method: "identify", Payload: json.RawMessage(`{}`)})
	if _, err := h.Handle(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "runs no module") {
		t.Errorf("a module call: %v", err)
	}
	if out, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"check"}`)); err != nil || !out.(deploycheck.Report).OK {
		t.Errorf("check: %v, %v", out, err)
	}
	if len(p.passed) != 0 {
		t.Errorf("a refresh ran: %v", p.passed)
	}
}

// The check reports the Google kinds and no other module's: the document's own
// secrets are the workspaces' keys and the OAuth client's, and a secret of the
// issuer's or a policy's kind is filtered out.
func TestTheCheckReportsOnlyGoogleKinds(t *testing.T) {
	doc := &config.SluisGoogle{
		Secrets: &config.Secrets{Source: "ssm", Root: "/sluis/example", Layout: config.SecretsLayoutV5},
		Google: config.Google{
			Workspaces:  []config.DirectoryWorkspace{{ID: "C0123abcd", Backend: "google", Admin: "a@example.com", KeySecret: "directory/example/key"}},
			OAuthClient: &config.GoogleOAuthClient{Provider: "main"},
		},
	}
	declared := doc.DeclaredSecrets()
	var kinds []string
	for _, d := range declared {
		kinds = append(kinds, d.Kind)
	}
	slices.Sort(kinds)
	want := []string{config.SecretWorkspaceKey, config.SecretSignInClientID, config.SecretSignInClientSecret}
	slices.Sort(want)
	if !slices.Equal(kinds, want) {
		t.Fatalf("declared kinds %v, want %v", kinds, want)
	}
	foreign := append(slices.Clone(declared),
		config.DeclaredSecret{Kind: config.SecretClient, Subject: "grafana", Name: "clients/grafana/secret"},
		config.DeclaredSecret{Kind: config.SecretStateSecret, Name: "state"},
		config.DeclaredSecret{Kind: config.SecretMinter, Subject: "main", Name: "m", Ref: true})
	own := deploycheck.Own(foreign, googlefn.Kinds...)
	if len(own) != len(declared) {
		t.Fatalf("Own kept %+v of %d declared", own, len(declared))
	}
	for _, d := range own {
		if !slices.Contains(googlefn.Kinds, d.Kind) {
			t.Errorf("the check reads a %s secret", d.Kind)
		}
	}
	// Without a secrets section there is nothing to read from, and a store that
	// is not SSM is refused: the section is held to the rules.
	if _, err := googlefn.Check(&config.SluisGoogle{})(t.Context()); err == nil {
		t.Error("a check with no secrets section passed")
	}
	if _, err := googlefn.Check(&config.SluisGoogle{Secrets: &config.Secrets{Source: "env"}})(t.Context()); err == nil {
		t.Error("a check over a secrets source that is not ssm passed")
	}
	// Nothing declared reads nothing and reaches no AWS.
	rep, err := googlefn.Check(&config.SluisGoogle{Secrets: doc.Secrets})(t.Context())
	if err != nil || !rep.OK || len(rep.Results) != 0 {
		t.Errorf("an empty document: %+v, %v", rep, err)
	}
}

// A zip built for the Google module runs no other module's document.
func TestTheGoogleZipRefusesAnotherModulesDocument(t *testing.T) {
	file := t.TempDir() + "/sluis.yaml"
	if err := writeFile(file, "apiVersion: "+config.BackupAPIVersion+"\n"); err != nil {
		t.Fatal(err)
	}
	_, err := lambdaapp.OpenModule(context.Background(), func(k string) string {
		if k == config.EnvConfig {
			return file
		}
		return ""
	}, lambdaapp.ModuleGoogle)
	if err == nil || !strings.Contains(err.Error(), "sluis-google function") || !strings.Contains(err.Error(), "backup document") {
		t.Errorf("%v", err)
	}
}

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }

// The module answers the console's calls and the sign-in's through the alias it
// was invoked by, and assembles its service only when one arrives: a scheduled
// refresh never pays for it.
func TestTheModulesCallsOpenTheServiceOnTheFirstCallAndAnswerByAlias(t *testing.T) {
	var opened int
	var openErr error
	open := func(context.Context) (*googleconsole.Service, error) {
		opened++
		if openErr != nil {
			return nil, openErr
		}
		return googleconsole.NewService(hub.New(hub.NewMemoryStore(), hub.NewMemorySnapshots(), hub.Config{}, slog.New(slog.DiscardHandler)),
			nil, nil, slog.New(slog.DiscardHandler)), nil
	}
	p := &puller{all: hub.RefreshResult{Workspaces: 1, Ran: 1}}
	h := googlefn.WithCalls(handler(p), googlefn.Server(nil, open))

	if _, err := handle(t, h, `{"kind":"refresh"}`); err != nil || opened != 0 {
		t.Fatalf("a refresh opened the service %d times: %v", opened, err)
	}

	call := func(alias, method string, msg string) (string, error) {
		t.Helper()
		ctx := lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{
			InvokedFunctionArn: "arn:aws:lambda:eu-west-1:111111111111:function:sluis-x-google:" + alias})
		payload, _ := json.Marshal(modcall.Request{Kind: modcall.Kind, Module: "google", Method: method,
			Payload: json.RawMessage(`{"caller":{"actor":"ada@north.example","roles":["operator"]}` + msg + `}`)})
		out, err := h.Handle(ctx, payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(out)
		var res modcall.Response
		if err = json.Unmarshal(raw, &res); err != nil {
			t.Fatal(err)
		}
		body, err := modcall.Result(res)
		return string(body), err
	}

	// The service cannot be opened: unavailable, and the next call tries again.
	openErr = errors.New("secrets store unreachable")
	if _, err := call("live-console", "console.list_workspaces", ""); err == nil || !strings.Contains(err.Error(), modcall.CodeUnavailable) {
		t.Fatalf("a call with the service down = %v, want unavailable", err)
	}
	openErr = nil
	if body, err := call("live-console", "console.list_workspaces", ""); err != nil || !strings.Contains(body, `"msg"`) {
		t.Fatalf("the console's list: %q, %v", body, err)
	}
	// A console method as the issuer, and the sign-in as the console: refused.
	if _, err := call("live-issuer", "console.list_workspaces", ""); !errors.Is(err, modcall.Coded(modcall.CodeForbidden, "")) {
		t.Errorf("a console method as the issuer = %v, want forbidden", err)
	}
	if _, err := call("live-console", "identify", `,"code":"x"`); !errors.Is(err, modcall.Coded(modcall.CodeForbidden, "")) {
		t.Errorf("identify as the console = %v, want forbidden", err)
	}
	if opened != 2 {
		t.Errorf("the service was opened %d times, want one failed and one that worked", opened)
	}
}
