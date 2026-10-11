package rosterapp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	"github.com/truvity/sluis/internal/modcall"
)

type recorder struct {
	module, method string
	deadline       time.Duration
	hasDeadline    bool
}

func (r *recorder) Call(ctx context.Context, module, method string, _ []byte) ([]byte, error) {
	r.module, r.method = module, method
	var d time.Time
	d, r.hasDeadline = ctx.Deadline()
	r.deadline = time.Until(d)
	return json.Marshal(modcall.Response{})
}

// The mint goes through the dialled client to the GitHub module as the issuer
// class, and a call that nobody answers is bounded.
func TestTheMintIsDialledAsTheIssuerWithADeadline(t *testing.T) {
	rec := &recorder{}
	var gotClass string
	var gotCfg modcall.Config
	cfg := Config{Dial: func(_ context.Context, c modcall.Config, class string) (modcall.Caller, error) {
		gotCfg, gotClass = c, class
		return rec, nil
	}}
	minter, err := dialMinter(context.Background(), cfg, config.ModuleRemote{Function: "sluis-example-github-mint"})
	if err != nil {
		t.Fatal(err)
	}
	if gotClass != "issuer" {
		t.Errorf("dialled as class %q, want issuer", gotClass)
	}
	if got := gotCfg.Modules["github"].Function; got != "sluis-example-github-mint" || len(gotCfg.Modules) != 1 {
		t.Errorf("routing %+v, want github -> sluis-example-github-mint alone", gotCfg.Modules)
	}
	_, _ = minter.MintInstallation(context.Background(), githubtokens.Request{App: "publisher"})
	if rec.module != "github" || rec.method != "mint_installation" {
		t.Errorf("called %s.%s, want github.mint_installation", rec.module, rec.method)
	}
	if !rec.hasDeadline || rec.deadline > MintDeadline || rec.deadline < MintDeadline-2*time.Second {
		t.Errorf("deadline in %v (set %v), want about %v", rec.deadline, rec.hasDeadline, MintDeadline)
	}
	if MintDeadline != 10*time.Second {
		t.Errorf("MintDeadline = %v, want 10s", MintDeadline)
	}
}

func TestADialFailureIsTheStartsFailure(t *testing.T) {
	want := errors.New("no transport")
	cfg := Config{Dial: func(context.Context, modcall.Config, string) (modcall.Caller, error) { return nil, want }}
	if _, err := dialMinter(context.Background(), cfg, config.ModuleRemote{Function: "f"}); !errors.Is(err, want) {
		t.Fatalf("err = %v, want the dial's", err)
	}
}
