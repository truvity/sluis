package slackfn_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/lambdaapp/slackfn"
	"github.com/truvity/sluis/internal/modcall"
	slackapp "github.com/truvity/sluis/internal/slackroster/app"
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
	policy := write(t, "apiVersion: sluis.truvity.github.io/policy/v2\ngroups: {x:y:z: {}}\nslack: {workspaces: {acme: {}}}\n")
	cfg, err := slackapp.Load(write(t, "apiVersion: sluis.truvity.github.io/controller-slack/v2\npolicy: {file: "+policy+"}\nconsoleURL: http://console:8080\n"+
		"secrets: {source: ssm, root: /sluis/example, layout: v5}\n"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Declares
}

func handler(t *testing.T, p *pass, opened *int) *lambdaapp.HTTP {
	t.Helper()
	return slackfn.Handler(nil, declares(t), func(context.Context) (lambdaapp.Pass, error) {
		*opened++
		return p, nil
	}, func(context.Context) (deploycheck.Report, error) { return deploycheck.Report{OK: true}, nil })
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
}

func TestAContendedPassEndsCleanlyAndAFailedOneIsAnError(t *testing.T) {
	opened := 0
	res, err := result(t, handler(t, &pass{}, &opened), `{"kind":"tick","target":"acme"}`)
	if err != nil || res.Outcome != lambdaapp.OutcomeContended {
		t.Errorf("contended: %+v, %v", res, err)
	}
	if _, err = result(t, handler(t, &pass{err: errors.New("slack said no")}, &opened), `{"kind":"tick","target":"acme"}`); err == nil {
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

// The controller function does not serve the mint, from any caller class.
func TestTheControllerFunctionRefusesTheMintRPC(t *testing.T) {
	opened := 0
	h := handler(t, &pass{ran: true}, &opened)
	raw, _ := json.Marshal(modcall.Request{Kind: modcall.Kind, Module: "slack", Method: "mint_installation", Payload: json.RawMessage(`{"app":"publisher"}`)})
	if _, err := h.Handle(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "runs no module") {
		t.Fatalf("%v", err)
	}
	if opened != 0 {
		t.Error("a controller was assembled for a module call")
	}
}
