package dial_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/dial"
)

type echoReq struct {
	Word string `json:"word"`
}
type echoRes struct {
	Said    string `json:"said"`
	Subject string `json:"subject"`
}

type local string

func (l local) Call(context.Context, string, string, []byte) ([]byte, error) {
	return []byte(`"` + string(l) + `"`), nil
}

func serviceFor(t *testing.T, module string) *httptest.Server {
	t.Helper()
	s := modcall.NewServer(module)
	modcall.Handle(s, "say", func(ctx context.Context, r echoReq) (echoRes, error) {
		return echoRes{Said: r.Word, Subject: modcall.CallerOf(ctx)}, nil
	})
	srv := httptest.NewServer(s.Handler(func(_ context.Context, bearer string) (string, error) {
		if bearer != "tok-"+module {
			return "", errors.New("bad bearer")
		}
		return "issuer", nil
	}))
	t.Cleanup(srv.Close)
	return srv
}

func tokenFile(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestDialRoutesAURLModuleWithItsOwnTokenFile(t *testing.T) {
	gh, sl := serviceFor(t, "github"), serviceFor(t, "slack")
	cfg := modcall.Config{Modules: map[string]modcall.Target{
		"github": {URL: gh.URL, TokenFile: tokenFile(t, "tok-github")},
		"slack":  {URL: sl.URL, Audience: "slack-aud", TokenFile: tokenFile(t, "tok-slack")},
	}}
	r, err := dial.Dial(context.Background(), cfg, "issuer", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"github", "slack"} {
		res, err := modcall.Do[echoReq, echoRes](context.Background(), r, m, "say", echoReq{Word: m})
		if err != nil || res.Said != m || res.Subject != "issuer" {
			t.Errorf("%s: %+v, %v", m, res, err)
		}
	}
}

func TestDialKeepsALocalModuleLocalAndRefusesWhenThereIsNone(t *testing.T) {
	cfg := modcall.Config{Modules: map[string]modcall.Target{"remote": {URL: "http://x.invalid"}, "explicit": {}}}
	r, err := dial.Dial(context.Background(), cfg, "issuer", local("here"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"explicit", "unnamed"} {
		if got, err := r.Call(context.Background(), m, "x", nil); err != nil || string(got) != `"here"` {
			t.Errorf("%s: %s, %v", m, got, err)
		}
	}
	r, _ = dial.Dial(context.Background(), cfg, "issuer", nil)
	if _, err = r.Call(context.Background(), "unnamed", "x", nil); !errors.Is(err, modcall.ErrNoRoute) {
		t.Fatalf("no local caller: %v", err)
	}
}

func TestDialRefusesAModuleThatNamesAFunctionAndAURL(t *testing.T) {
	cfg := modcall.Config{Modules: map[string]modcall.Target{"a": {Function: "f", URL: "http://a"}}}
	if _, err := dial.Dial(context.Background(), cfg, "issuer", nil); err == nil {
		t.Fatal("accepted")
	}
}

func TestDialWithNoModulesIsAllLocal(t *testing.T) {
	r, err := dial.Dial(context.Background(), modcall.Config{}, "", local("here"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Call(context.Background(), "any", "x", nil); string(got) != `"here"` {
		t.Fatalf("%s", got)
	}
}
