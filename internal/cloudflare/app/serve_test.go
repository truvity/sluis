package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/cloudflare/rpc"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/modcall"
)

// minting is the minter's answers, and counts what reached it.
type minting struct {
	err   error
	calls int
}

func (m *minting) MintFor(_ context.Context, preset string, _ rpc.Caller, _ time.Duration) (*rpc.Minted, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &rpc.Minted{Preset: preset, AccessKeyID: "id"}, nil
}

func (m *minting) Granted(rpc.Caller) []rpc.PresetInfo {
	m.calls++
	return []rpc.PresetInfo{{Name: "dns", Lifetime: time.Hour}}
}

// verify is the TokenReview: one token minted for the audience.
func verify(_ context.Context, bearer string) (string, error) {
	if bearer != "good" {
		return "", errors.New("rejected")
	}
	return "system:serviceaccount:sluis:issuer", nil
}

func listener(t *testing.T, m rpc.Minting) (url string) {
	t.Helper()
	s := modcall.NewServer(rpc.Module)
	rpc.Register(s, m)
	srv := httptest.NewServer(Mux(s, verify))
	t.Cleanup(srv.Close)
	return srv.URL
}

func dial(url, token string) *rpc.Client {
	c := &modcall.HTTPCaller{URLs: map[string]string{rpc.Module: url},
		Token: func(context.Context, string) (string, error) { return token, nil }}
	return rpc.NewClient(c, nil)
}

func TestARoundTripOverHTTPReachesTheMinterAndTheThreeRefusalsKeepTheirNames(t *testing.T) {
	m := &minting{}
	url := listener(t, m)
	got, err := dial(url, "good").MintFor(context.Background(), "dns", rpc.Caller{}, 0)
	if err != nil || got.Preset != "dns" || got.AccessKeyID != "id" {
		t.Fatalf("%+v %v", got, err)
	}
	if g := dial(url, "good").Granted(rpc.Caller{}); len(g) != 1 || g[0].Name != "dns" {
		t.Errorf("%+v", g)
	}
	for _, want := range []error{rpc.ErrUnknownPreset, rpc.ErrNotGranted, rpc.ErrLifetime} {
		m.err = want
		if _, err = dial(url, "good").MintFor(context.Background(), "dns", rpc.Caller{}, 0); !errors.Is(err, want) {
			t.Errorf("%v came back as %v", want, err)
		}
	}
	m.err = errors.New("cloudflare said token abc")
	if _, err = dial(url, "good").MintFor(context.Background(), "dns", rpc.Caller{}, 0); err == nil || err.Error() != "module call: internal" {
		t.Errorf("%v", err)
	}
}

func TestACallWithoutAValidBearerIs401AndReachesNoMethod(t *testing.T) {
	m := &minting{}
	url := listener(t, m)
	if _, err := dial(url, "forged").MintFor(context.Background(), "dns", rpc.Caller{}, 0); !errors.Is(err, modcall.ErrTransport) {
		t.Errorf("a forged token: %v", err)
	}
	for name, header := range map[string]string{"none": "", "empty": "Bearer ", "scheme": "Basic good"} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url+modcall.RPCPath, nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
	if m.calls != 0 {
		t.Errorf("%d calls reached the minter", m.calls)
	}
}

func TestTheHealthEndpointNeedsNoCredential(t *testing.T) {
	res, err := http.Get(listener(t, &minting{}) + HealthPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("%d", res.StatusCode)
	}
}

func TestTheAudienceDefaultsToTheModulesName(t *testing.T) {
	if got := (Config{}).Audience(); got != "cloudflare" {
		t.Error(got)
	}
	c := Config{cloudflare: &config.Cloudflare{Serve: &config.CloudflareServe{Audience: "x"}}}
	if !c.Serves() || c.Audience() != "x" {
		t.Error(c.Audience())
	}
}
