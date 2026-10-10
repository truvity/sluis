package githubtokens_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	"github.com/truvity/sluis/internal/modcall"
)

// fake is the minter's answers, and records what it was asked.
type fake struct {
	err   error
	asked githubtokens.Request
	calls int
}

func (f *fake) MintInstallation(_ context.Context, r githubtokens.Request) (githubtokens.Minted, error) {
	f.asked = r
	f.calls++
	if f.err != nil {
		return githubtokens.Minted{}, f.err
	}
	return githubtokens.Minted{
		Token: "example-installation-token", ExpiresAt: time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC),
		Repositories: []string{"app"}, Permissions: map[string]string{"contents": "read"}, Installation: 42,
	}, nil
}

func server(f *fake) *modcall.Server {
	s := modcall.NewServer(githubtokens.Module)
	githubtokens.Register(s, f)
	return s
}

func client(f *fake) *githubtokens.Client {
	return githubtokens.NewClient(modcall.Local{githubtokens.Module: server(f)}.As(githubtokens.CallerIssuer))
}

var request = githubtokens.Request{
	App: "publisher", Repositories: []string{"app"}, Permissions: map[string]string{"contents": "read"},
}

func TestARequestCrossesTheBoundaryWithItsNarrowingAndComesBackWithTheInstallation(t *testing.T) {
	f := &fake{}
	got, err := client(f).MintInstallation(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if f.asked.App != "publisher" || !slices.Equal(f.asked.Repositories, []string{"app"}) ||
		!maps.Equal(f.asked.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("the module was asked %+v", f.asked)
	}
	if got.Token != "example-installation-token" || got.Installation != 42 || got.ExpiresAt.Hour() != 13 ||
		!slices.Equal(got.Repositories, []string{"app"}) || got.Permissions["contents"] != "read" {
		t.Errorf("%+v", got)
	}
	if g := got.Granted(); g.Token != got.Token || g.ExpiresAt != got.ExpiresAt {
		t.Errorf("granted = %+v", g)
	}
}

func TestTheThreeRefusalsComeBackAsTheirSentinelsWithTheReasonOfTheFirstTwo(t *testing.T) {
	for _, c := range []struct {
		sentinel error
		reason   bool
	}{{githubtokens.ErrUnavailable, true}, {githubtokens.ErrScope, true}, {githubtokens.ErrUpstream, false}} {
		f := &fake{err: fmt.Errorf("%w: detail of the refusal: %w", c.sentinel, errors.New("GitHub said ghs_secret"))}
		_, err := client(f).MintInstallation(context.Background(), request)
		if !errors.Is(err, c.sentinel) {
			t.Errorf("%v: got %v", c.sentinel, err)
			continue
		}
		if has := strings.Contains(err.Error(), "detail of the refusal"); has != c.reason {
			t.Errorf("%v: reason carried = %v, want %v (%v)", c.sentinel, has, c.reason, err)
		}
	}
}

func TestAnyOtherFailureIsInternalAndTellsNothing(t *testing.T) {
	_, err := client(&fake{err: errors.New("GitHub said token ghs_secret")}).MintInstallation(context.Background(), request)
	var e *modcall.Error
	if !errors.As(err, &e) || e.Code != modcall.CodeInternal || err.Error() != "module call: internal" {
		t.Fatalf("%v", err)
	}
}

// Only the issuer may ask: the decision of a grant is its, and no other module
// has a reason to hold an installation token.
func TestOnlyTheIssuerMayAsk(t *testing.T) {
	f := &fake{}
	local := modcall.Local{githubtokens.Module: server(f)}
	for name, c := range map[string]modcall.Caller{
		"console": local.As("console"), "cloudflare": local.As("cloudflare"), "empty class": local.As(""), "no class": local,
	} {
		_, err := githubtokens.NewClient(c).MintInstallation(context.Background(), request)
		var e *modcall.Error
		if !errors.As(err, &e) || e.Code != modcall.CodeForbidden {
			t.Errorf("%s: %v, want forbidden", name, err)
		}
	}
	if f.calls != 0 {
		t.Errorf("the minter ran %d times for callers it refuses", f.calls)
	}
}

func TestAnAnswerWithoutATokenIsAnError(t *testing.T) {
	s := modcall.NewServer(githubtokens.Module)
	githubtokens.Register(s, emptyMinter{})
	_, err := githubtokens.NewClient(modcall.Local{githubtokens.Module: s}.As(githubtokens.CallerIssuer)).
		MintInstallation(context.Background(), request)
	if err == nil {
		t.Fatal("an empty token was accepted")
	}
}

type emptyMinter struct{}

func (emptyMinter) MintInstallation(context.Context, githubtokens.Request) (githubtokens.Minted, error) {
	return githubtokens.Minted{}, nil
}

func TestWithoutAStoreEveryRequestIsUnavailable(t *testing.T) {
	_, err := githubtokens.InProcess{}.MintInstallation(context.Background(), request)
	if !errors.Is(err, githubtokens.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}
