//go:build !lambda

package modserve_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/consolecall"
	githubconsole "github.com/truvity/sluis/internal/githubapp/console"
	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modserve"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
)

const (
	issuerSubject = "system:serviceaccount:sluis:sluis-issuer"
	otherSubject  = "system:serviceaccount:sluis:sluis-google"
	wrongSpace    = "system:serviceaccount:elsewhere:sluis-issuer"
)

// review is the TokenReview, faked: the API server's answer for a token minted
// for the audience, by token. A token minted for another audience, and a
// forged one, are not authenticated.
func review(t *testing.T, audience string, seen *[]string) func(context.Context, string, []string) (string, error) {
	t.Helper()
	var mu sync.Mutex
	return func(_ context.Context, token string, audiences []string) (string, error) {
		mu.Lock()
		*seen = append(*seen, strings.Join(audiences, ","))
		mu.Unlock()
		if len(audiences) != 1 || audiences[0] != audience {
			return "", errors.New("audience")
		}
		switch token {
		case "issuer":
			return issuerSubject, nil
		case "google":
			return otherSubject, nil
		case "elsewhere":
			return wrongSpace, nil
		}
		return "", errors.New("rejected")
	}
}

func serve(t *testing.T, module string, s *modcall.Server) (url string, audiences *[]string) {
	t.Helper()
	seen := new([]string)
	verify := modserve.Reviewer(review(t, module, seen), modserve.Audience(module, nil))
	srv := httptest.NewServer(modserve.Mux(s, verify, []string{issuerSubject}))
	t.Cleanup(srv.Close)
	return srv.URL, seen
}

func dial(url, module, token string) modcall.Caller {
	return &modcall.HTTPCaller{
		URLs:     map[string]string{module: url},
		Token:    func(context.Context, string) (string, error) { return token, nil },
		MaxBytes: consolecall.ListMaxBytes,
	}
}

// reached says a console call got as far as the method: it answered, or it told
// a Problem or its own refusal in words. The class refusal, which has no
// message, and the transport's are not that.
func reached(err error) bool {
	var p *consolecall.Problem
	var coded *modcall.Error
	return err == nil || errors.As(err, &p) || (errors.As(err, &coded) && !(coded.Code == modcall.CodeForbidden && coded.Message == ""))
}

func TestTheIssuerReachesTheSlackConsoleOverTheTransportAndNoOtherAccountDoes(t *testing.T) {
	url, audiences := serve(t, "slack", slackconsole.NewServer(slackconsole.Static(slackconsole.Deps{})))
	ask := func(token string) error {
		_, err := consolecall.Proxy[*directoryrosterv1.GetSlackStatusRequest, *directoryrosterv1.GetSlackStatusResponse](
			context.Background(), dial(url, "slack", token), "slack", slackconsole.MethodGetStatus,
			consolecall.Caller{Actor: "a@example.test"}, &directoryrosterv1.GetSlackStatusRequest{})
		return err
	}
	if err := ask("issuer"); !reached(err) {
		t.Fatalf("the issuer's account was refused: %v", err)
	}
	for name, token := range map[string]string{"another account": "google", "the issuer's name in another namespace": "elsewhere", "a forged token": "forged"} {
		if err := ask(token); !errors.Is(err, modcall.ErrTransport) {
			t.Errorf("%s: %v, want a 401", name, err)
		}
	}
	for _, a := range *audiences {
		if a != "slack" {
			t.Errorf("a token was reviewed for audience %q, want slack", a)
		}
	}
}

func TestTheIssuerReachesTheGitHubMintAndConsoleOverTheTransportAndNoOtherAccountDoes(t *testing.T) {
	s := modcall.NewServer(githubtokens.Module)
	githubtokens.Register(s, minting{})
	githubconsole.Register(s, githubconsole.New(githubconsole.Deps{}))
	url, _ := serve(t, "github", s)

	// The issuer's account is the issuer class (the mint) and the console class
	// (the pages): one pod, both.
	got, err := githubtokens.NewClient(dial(url, "github", "issuer")).MintInstallation(context.Background(), githubtokens.Request{App: "ci"})
	if err != nil || got.Token != "ghs_fake" {
		t.Fatalf("the mint: %+v %v", got, err)
	}
	_, err = consolecall.Proxy[*directoryrosterv1.GetGitHubStatusRequest, *directoryrosterv1.GetGitHubStatusResponse](
		context.Background(), dial(url, "github", "issuer"), "github", consolewire.GetStatus,
		consolecall.Caller{Actor: "a@example.test"}, &directoryrosterv1.GetGitHubStatusRequest{})
	if !reached(err) {
		t.Fatalf("the console page was refused to the issuer's account: %v", err)
	}

	for name, token := range map[string]string{"another account": "google", "the issuer's name in another namespace": "elsewhere", "a forged token": "forged"} {
		if _, err = githubtokens.NewClient(dial(url, "github", token)).MintInstallation(context.Background(), githubtokens.Request{App: "ci"}); !errors.Is(err, modcall.ErrTransport) {
			t.Errorf("%s, the mint: %v, want a 401", name, err)
		}
		_, err = consolecall.Proxy[*directoryrosterv1.GetGitHubStatusRequest, *directoryrosterv1.GetGitHubStatusResponse](
			context.Background(), dial(url, "github", token), "github", consolewire.GetStatus,
			consolecall.Caller{Actor: "a@example.test"}, &directoryrosterv1.GetGitHubStatusRequest{})
		if !errors.Is(err, modcall.ErrTransport) {
			t.Errorf("%s, the console: %v, want a 401", name, err)
		}
	}
}

func TestACallWithoutABearerIs401AndHealthNeedsNoCredential(t *testing.T) {
	url, _ := serve(t, "slack", slackconsole.NewServer(slackconsole.Static(slackconsole.Deps{})))
	for name, header := range map[string]string{"none": "", "empty": "Bearer ", "scheme": "Basic issuer"} {
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
	res, err := http.Get(url + modserve.HealthPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("health: %d", res.StatusCode)
	}
}

func TestTheDefaultsAreTheModulesNameAndThePort(t *testing.T) {
	if got := modserve.Audience("github", nil); got != "github" {
		t.Error(got)
	}
	if got := modserve.Audience("github", &config.ModuleServe{Audience: "x"}); got != "x" {
		t.Error(got)
	}
	if got := modserve.Address(nil); got != ":8081" {
		t.Error(got)
	}
}

type minting struct{}

func (minting) MintInstallation(context.Context, githubtokens.Request) (githubtokens.Minted, error) {
	return githubtokens.Minted{Token: "ghs_fake"}, nil
}
