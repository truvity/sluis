package githubfn_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/lambdacontext"

	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/githubapp/githubfake"
	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	githubcontroller "github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/lambdaapp/githubfn"
	"github.com/truvity/sluis/internal/modcall"
)

type pass struct {
	ran    bool
	err    error
	closed int
}

func (p *pass) Pass(context.Context, string, bool) (bool, error) { return p.ran, p.err }
func (p *pass) Close() error                                     { p.closed++; return nil }

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// declares is the policy's own answer, from a real document.
func declares(t *testing.T) func(string) bool {
	t.Helper()
	policy := write(t, "apiVersion: sluis.truvity.github.io/policy/v2\ngroups: {x:y:z: {}}\ngithub: {acme: {members: [x:y:z]}}\n")
	cfg, err := githubapp.Load(write(t, "apiVersion: sluis.truvity.github.io/controller-github/v2\npolicy: {file: "+policy+"}\nconsoleURL: http://console:8080\n"+
		"secrets: {source: ssm, root: /sluis/example, layout: v5}\n"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Declares
}

func handler(t *testing.T, p *pass, opened *int) *lambdaapp.HTTP {
	t.Helper()
	return githubfn.Handler(nil, declares(t), func(context.Context) (lambdaapp.Pass, error) {
		*opened++
		return p, nil
	}, nil, func(context.Context) (deploycheck.Report, error) { return deploycheck.Report{OK: true}, nil })
}

func result(t *testing.T, h *lambdaapp.HTTP, payload string) (lambdaapp.Result, error) {
	t.Helper()
	out, err := h.Handle(context.Background(), json.RawMessage(payload))
	if err != nil {
		return lambdaapp.Result{}, err
	}
	return out.(lambdaapp.Result), nil
}

func TestATickAndARunOfADeclaredTargetRunOnePass(t *testing.T) {
	for _, kind := range []string{"tick", "run"} {
		p, opened := &pass{ran: true}, 0
		res, err := result(t, handler(t, p, &opened), `{"kind":"`+kind+`","target":"acme"}`)
		if err != nil || res != (lambdaapp.Result{Kind: kind, Target: "acme", Outcome: lambdaapp.OutcomeRan}) {
			t.Errorf("%s: %+v, %v", kind, res, err)
		}
		if opened != 1 || p.closed != 1 {
			t.Errorf("%s: opened %d, closed %d", kind, opened, p.closed)
		}
	}
	// The links pass is a target of its own.
	p, opened := &pass{ran: true}, 0
	links := `{"kind":"tick","target":"` + githubcontroller.LinksTarget + `"}`
	if res, err := result(t, handler(t, p, &opened), links); err != nil || res.Outcome != lambdaapp.OutcomeRan {
		t.Errorf("links: %+v, %v", res, err)
	}
}

func TestAContendedPassEndsCleanlyAndAFailedOneIsAnError(t *testing.T) {
	opened := 0
	res, err := result(t, handler(t, &pass{}, &opened), `{"kind":"tick","target":"acme"}`)
	if err != nil || res.Outcome != lambdaapp.OutcomeContended {
		t.Errorf("contended: %+v, %v", res, err)
	}
	if _, err = result(t, handler(t, &pass{err: errors.New("github said no")}, &opened), `{"kind":"tick","target":"acme"}`); err == nil {
		t.Error("a failed pass returned success")
	}
}

// A target the policy does not declare is somebody else's: a run-now ends
// cleanly and a schedule naming it is a mistake to see, and in neither case is
// a controller assembled.
func TestATargetThePolicyDoesNotDeclareAssemblesNothing(t *testing.T) {
	p, opened := &pass{ran: true}, 0
	h := handler(t, p, &opened)
	res, err := result(t, h, `{"kind":"run","target":"somebody-else"}`)
	if err != nil || res.Outcome != lambdaapp.OutcomeUnknown {
		t.Errorf("run: %+v, %v", res, err)
	}
	if _, err = result(t, h, `{"kind":"tick","target":"somebody-else"}`); err == nil {
		t.Error("a tick of an undeclared target succeeded")
	}
	if opened != 0 {
		t.Errorf("a controller was assembled %d times", opened)
	}
}

// A function that serves no module call (rpc nil) refuses the mint from every class.
func TestAFunctionWithoutAModuleServerRefusesTheMintRPC(t *testing.T) {
	opened := 0
	h := handler(t, &pass{ran: true}, &opened)
	raw, _ := json.Marshal(modcall.Request{Kind: modcall.Kind, Module: "github", Method: "mint_installation", Payload: json.RawMessage(`{"app":"publisher"}`)})
	if _, err := h.Handle(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "runs no module") {
		t.Fatalf("%v", err)
	}
	if opened != 0 {
		t.Error("a controller was assembled for a module call")
	}
}

type apps map[string]catalogueapp.Record

var appKey = func() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}()

func (s apps) Get(_ context.Context, id string) (catalogueapp.Record, string, bool, error) {
	r, ok := s[id]
	return r, appKey, ok, nil
}

// invokedAs is a context whose function ARN carries the alias the caller used.
func invokedAs(alias string) context.Context {
	return lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{
		InvokedFunctionArn: strings.Join([]string{"arn", "aws", "lambda", "eu-west-1", "123456789012", "function", "sluis-example-github", alias}, ":")})
}

func mintCall(t *testing.T, h *lambdaapp.HTTP, alias string) ([]byte, error) {
	t.Helper()
	payload, _ := json.Marshal(githubtokens.Request{App: "publisher", Repositories: []string{"app"}, Permissions: map[string]string{"contents": "read"}})
	raw, err := json.Marshal(modcall.Request{Kind: modcall.Kind, Module: githubtokens.Module, Method: githubtokens.MethodMintInstallation, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(invokedAs(alias), raw)
	if err != nil {
		t.Fatalf("the invocation failed: %v", err)
	}
	b, _ := json.Marshal(out)
	var res modcall.Response
	if err = json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	return modcall.Result(res)
}

// One function serves the passes and the mint: the mint answers the issuer's
// class and refuses the console (and every other class), and GitHub is never
// reached for a refused caller. A pass still runs on the same handler.
func TestTheMintAnswersTheIssuerClassAndRefusesTheConsole(t *testing.T) {
	github := githubfake.Start(t, "example-org")
	store := apps{"publisher": {ID: "publisher", Org: "example-org", AppID: 7, AppSlug: "publisher", InstallationID: 42}}
	opened := 0
	h := githubfn.Handler(nil, declares(t), func(context.Context) (lambdaapp.Pass, error) {
		opened++
		return &pass{ran: true}, nil
	}, githubapp.MintServer(store, github.Client()), func(context.Context) (deploycheck.Report, error) { return deploycheck.Report{OK: true}, nil })

	body, err := mintCall(t, h, "live-issuer")
	if err != nil {
		t.Fatalf("the issuer was refused: %v", err)
	}
	var minted githubtokens.Minted
	if err = json.Unmarshal(body, &minted); err != nil {
		t.Fatal(err)
	}
	if minted.Token != github.Token || minted.Installation != 42 {
		t.Errorf("minted = installation %d, token matches %v", minted.Installation, minted.Token == github.Token)
	}
	for _, alias := range []string{"live-console", "live-cloudflare", "live-github", "live", ""} {
		_, err = mintCall(t, h, alias)
		var e *modcall.Error
		if !errors.As(err, &e) || e.Code != modcall.CodeForbidden {
			t.Errorf("as %q: the call was not refused as forbidden: %v", alias, err)
		}
	}
	if len(github.TokenRequests) != 1 {
		t.Errorf("GitHub was asked %d times, want 1: %+v", len(github.TokenRequests), github.TokenRequests)
	}
	if res, err := result(t, h, `{"kind":"tick","target":"acme"}`); err != nil || res.Outcome != lambdaapp.OutcomeRan || opened != 1 {
		t.Errorf("a pass on the same handler: %+v, %v, opened %d", res, err, opened)
	}
}
