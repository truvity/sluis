package githubmintfn_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/lambdacontext"

	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/githubapp/githubfake"
	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/lambdaapp/githubmintfn"
	"github.com/truvity/sluis/internal/modcall"
)

type store map[string]catalogueapp.Record

var appKey = func() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}()

func (s store) Get(_ context.Context, id string) (catalogueapp.Record, string, bool, error) {
	r, ok := s[id]
	return r, appKey, ok, nil
}

// invokedAs is a context whose function ARN carries the alias the caller used.
func invokedAs(alias string) context.Context {
	return lambdacontext.NewContext(context.Background(), &lambdacontext.LambdaContext{
		InvokedFunctionArn: strings.Join([]string{"arn", "aws", "lambda", "eu-west-1", "123456789012", "function", "sluis-example-github-mint", alias}, ":")})
}

func mintEvent(t *testing.T) json.RawMessage {
	t.Helper()
	payload, _ := json.Marshal(githubtokens.Request{App: "publisher", Repositories: []string{"app"}, Permissions: map[string]string{"contents": "read"}})
	raw, err := json.Marshal(modcall.Request{Kind: modcall.Kind, Module: githubtokens.Module, Method: githubtokens.MethodMintInstallation, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// call is the answer the caller class gets, as modcall hands it back.
func call(t *testing.T, h *lambdaapp.HTTP, alias string) ([]byte, error) {
	t.Helper()
	out, err := h.Handle(invokedAs(alias), mintEvent(t))
	if err != nil {
		t.Fatalf("the invocation failed: %v", err)
	}
	raw, _ := json.Marshal(out)
	var res modcall.Response
	if err = json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	return modcall.Result(res)
}

func newMint(t *testing.T) (*lambdaapp.HTTP, *githubfake.Org) {
	t.Helper()
	github := githubfake.Start(t, "example-org")
	apps := store{"publisher": {ID: "publisher", Org: "example-org", AppID: 7, AppSlug: "publisher", InstallationID: 42}}
	check := func(context.Context) (deploycheck.Report, error) { return deploycheck.Report{OK: true}, nil }
	return githubmintfn.Handler(nil, githubapp.MintServer(apps, github.Client()), check), github
}

// The mint is the issuer's: asked as the issuer it mints against GitHub, asked
// as the console (or as no class at all) it refuses and GitHub is never called.
func TestTheMintAnswersTheIssuerAndRefusesTheConsole(t *testing.T) {
	h, github := newMint(t)

	body, err := call(t, h, "live-issuer")
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
	if len(github.TokenRequests) != 1 || github.TokenRequests[0].Installation != 42 {
		t.Fatalf("GitHub was asked %+v", github.TokenRequests)
	}

	for _, alias := range []string{"live-console", "live-cloudflare", "live-github", "live", ""} {
		_, err = call(t, h, alias)
		var e *modcall.Error
		if !errors.As(err, &e) || e.Code != modcall.CodeForbidden {
			t.Errorf("as %q: the call was not refused as forbidden: %v", alias, err)
		}
	}
	if len(github.TokenRequests) != 1 {
		t.Errorf("a refused caller reached GitHub: %+v", github.TokenRequests)
	}
}

// The function never runs a controller pass: a schedule's tick is an error the
// scheduler sees, a console's run-now ends cleanly as not its target.
func TestTheMintFunctionRunsNoControllerPass(t *testing.T) {
	h, github := newMint(t)
	if _, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"tick","target":"example-org"}`)); err == nil {
		t.Error("a tick was accepted")
	}
	out, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"run","target":"example-org"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res, ok := out.(lambdaapp.Result); !ok || res.Outcome != lambdaapp.OutcomeUnknown {
		t.Errorf("run-now = %#v", out)
	}
	if len(github.Hits) != 0 {
		t.Errorf("GitHub was called: %v", github.Hits)
	}
}
