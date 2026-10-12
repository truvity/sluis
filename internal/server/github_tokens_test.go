package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
	"github.com/truvity/sluis/internal/githubapp/mints"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/modcall"
)

var kept = time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)

// tokensRig is the Apps rig with a ring of recent requests, as the
// merged service assembles it: the issuer half writes it, this half
// reads it.
func tokensRig(t *testing.T) (*appsRig, *mints.Ring) {
	t.Helper()
	rig := newAppsRig(t)
	ring := mints.New(0, kept)
	rig.console.deps.GitHubMints = ring
	rig.catalogueApp(t, "renovate", rig.github.pem)
	return rig, ring
}

// tokens asks for one App's recent tokens as an operator.
func (r *appsRig) tokens(t *testing.T, id string) *directoryrosterv1.ListGitHubAppTokensResponse {
	t.Helper()
	listed, err := r.console.ListGitHubAppTokens(operator(),
		connect.NewRequest(&directoryrosterv1.ListGitHubAppTokensRequest{Id: id}))
	if err != nil {
		t.Fatalf("ListGitHubAppTokens(%s): %v", id, err)
	}
	return listed.Msg
}

// The page's whole point: the last requests of one App, newest first,
// each saying who asked, under which grant, for what, and what was
// decided. A refusal is one of them, spelled as a refusal — the refusals
// are usually why somebody opened the page.
func TestAnAppsRecentTokensAreNewestFirstAndSayWhatWasDecided(t *testing.T) {
	t.Parallel()
	rig, ring := tokensRig(t)
	ring.Add("renovate", mints.Token{
		At: kept.Add(time.Hour), Subject: "github:globex/site", Proof: "ci", Grant: "all:platform:engineer",
		Repositories: []string{"site"}, Permissions: "contents:read", Outcome: "ok",
	})
	ring.Add("renovate", mints.Token{
		At: kept.Add(2 * time.Hour), Subject: "ada@north.example", Proof: "person", Grant: "all:platform:engineer",
		Repositories: []string{"payments"}, Permissions: "contents:write", Outcome: "refused",
		Reason: "the request is wider than any grant this proof holds",
	})
	// Another App's requests are not this App's.
	ring.Add("releases", mints.Token{At: kept.Add(3 * time.Hour), Subject: "someone-else", Outcome: "ok"})

	listed := rig.tokens(t, "renovate")
	if len(listed.GetTokens()) != 2 {
		t.Fatalf("renovate has %d recent tokens, want 2", len(listed.GetTokens()))
	}
	first, second := listed.GetTokens()[0], listed.GetTokens()[1]
	if first.GetOutcome() != "refused" || first.GetSubject() != "ada@north.example" {
		t.Errorf("newest is %+v, want the refusal", first)
	}
	if first.GetReason() == "" {
		t.Error("a refusal says nothing about why")
	}
	if got := strings.Join(second.GetRepositories(), " "); got != "site" || second.GetPermissions() != "contents:read" {
		t.Errorf("the mint reads %q / %q, want site / contents:read", got, second.GetPermissions())
	}
	if second.GetGrant() != "all:platform:engineer" || second.GetProof() != "ci" {
		t.Errorf("the mint lost its grant or its proof: %+v", second)
	}
	if listed.GetKept() != int32(mints.PerApp) || listed.GetKeptSince().AsTime() != kept {
		t.Errorf("says it keeps %d since %v, want %d since %v",
			listed.GetKept(), listed.GetKeptSince().AsTime(), mints.PerApp, kept)
	}
}

// Nothing minted yet is an empty answer and NOT an error: the page says
// nothing has been asked for since the service started, which it can
// only do if it is told when that was.
func TestAnAppNothingHasAskedForAnswersEmptyAndSaysSinceWhen(t *testing.T) {
	t.Parallel()
	rig, _ := tokensRig(t)
	listed := rig.tokens(t, "renovate")
	if len(listed.GetTokens()) != 0 {
		t.Errorf("an App nothing asked for has %d tokens", len(listed.GetTokens()))
	}
	if listed.GetKeptSince().AsTime() != kept {
		t.Errorf("kept_since is %v, want %v", listed.GetKeptSince().AsTime(), kept)
	}
}

// A deployment whose issuer mints nothing here keeps no ring. The answer
// says so in a sentence, so the page can print it — an empty table would
// read as "nothing has been asked for".
func TestADeploymentThatKeepsNoRecentTokensSaysSo(t *testing.T) {
	t.Parallel()
	rig, _ := tokensRig(t)
	rig.console.deps.GitHubMints = nil
	_, err := rig.console.ListGitHubAppTokens(operator(),
		connect.NewRequest(&directoryrosterv1.ListGitHubAppTokensRequest{Id: "renovate"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a deployment keeping none = %v, want failed_precondition", err)
	}
	if !strings.Contains(err.Error(), "mints no installation tokens here") {
		t.Errorf("it does not say why: %v", err)
	}
}

// Only an App the catalogue declares mints installation tokens. Asking
// for a controller App's is a mistake worth a sentence, not an empty
// table that looks like a quiet week.
func TestOnlyACatalogueAppHasRecentTokens(t *testing.T) {
	t.Parallel()
	rig, _ := tokensRig(t)
	rig.connect(t, rig.github.pem)
	_, err := rig.console.ListGitHubAppTokens(operator(),
		connect.NewRequest(&directoryrosterv1.ListGitHubAppTokensRequest{Id: "globex-controller"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a controller App = %v, want failed_precondition", err)
	}
	if _, err = rig.console.ListGitHubAppTokens(operator(),
		connect.NewRequest(&directoryrosterv1.ListGitHubAppTokensRequest{Id: "no-such-app"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an App nothing declares = %v, want not_found", err)
	}
}

// A store that cannot be read is UNAVAILABLE, in the store's own words.
// Not an empty list: "nothing was asked for" and "nothing could be read"
// are opposite things to tell somebody looking for a token that was
// minted.
func TestAnUnreadableStoreIsNotAQuietWeek(t *testing.T) {
	t.Parallel()
	rig, _ := tokensRig(t)
	rig.console.deps.GitHubCatalogueApps = unreadableApps{}
	_, err := rig.console.ListGitHubAppTokens(operator(),
		connect.NewRequest(&directoryrosterv1.ListGitHubAppTokensRequest{Id: "renovate"}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("an unreadable store = %v, want unavailable", err)
	}
	if !strings.Contains(err.Error(), "the API server is not answering") {
		t.Errorf("the store's own words are lost: %v", err)
	}
}

// A request names who asked for it, so it is an operator's to read, as
// the Audit page is.
func TestRecentTokensAreOperatorOnly(t *testing.T) {
	t.Parallel()
	rig, _ := tokensRig(t)
	viewer := WithIdentity(context.Background(), access.Identity{Email: "lee@north.example", Role: access.RoleViewer})
	_, err := rig.console.ListGitHubAppTokens(viewer,
		connect.NewRequest(&directoryrosterv1.ListGitHubAppTokensRequest{Id: "renovate"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer = %v, want permission_denied", err)
	}
}

// A group's page says when its grant was last used, from the same ring:
// no store is read and nothing is asked of GitHub. A grant nothing has
// used says nothing at all rather than "never".
func TestAGroupsGrantCarriesWhenItWasLastMinted(t *testing.T) {
	t.Parallel()
	rig, ring := tokensRig(t)
	ring.Add("renovate", mints.Token{
		At: kept.Add(time.Hour), Subject: "github:globex/site", Grant: "all:platform:engineer",
		Repositories: []string{"site"}, Outcome: "ok",
	})
	ring.Add("renovate", mints.Token{
		At: kept.Add(2 * time.Hour), Subject: "github:globex/docs", Grant: "all:nobody:declares", Outcome: "refused",
	})

	grants := rig.console.githubGroupGrants()
	used := grants["all:platform:engineer"]
	if len(used) != 1 || used[0].GetLastMinted().AsTime() != kept.Add(time.Hour) {
		t.Fatalf("the used grant says %+v, want last minted %v", used, kept.Add(time.Hour))
	}
	refused := grants["all:nobody:declares"]
	if len(refused) != 1 || refused[0].GetLastMinted() != nil {
		t.Errorf("a grant that has only been refused claims a mint: %+v", refused)
	}
}

// unreadableApps is a catalogue App store that will not read: what an
// operator's page must not mistake for an installation with no Apps.
type unreadableApps struct{}

func (unreadableApps) Put(context.Context, catalogueapp.Record, string) error { return nil }

func (unreadableApps) List(context.Context) ([]catalogueapp.Record, error) {
	return nil, errors.New("the API server is not answering")
}

func (unreadableApps) Get(context.Context, string) (catalogueapp.Record, string, bool, error) {
	return catalogueapp.Record{}, "", false, errors.New("the API server is not answering")
}

func (unreadableApps) Delete(context.Context, string) error { return nil }

func (unreadableApps) PutWebhookSecret(context.Context, string, string) error { return nil }

func (unreadableApps) WebhookSecret(context.Context, string) (string, bool, error) {
	return "", false, errors.New("the API server is not answering")
}

// remoteModule puts the GitHub module in "another process" for the console:
// the same stores, and no ring, as the Lambda module has none.
func remoteModule(t *testing.T, c *Console) {
	t.Helper()
	deps := c.githubDeps()
	deps.Mints = nil
	srv := modcall.NewServer(consolewire.Module)
	ghconsole.Register(srv, ghconsole.New(deps))
	web := httptest.NewServer(srv.Handler(func(context.Context, string) (string, error) { return "console", nil }))
	t.Cleanup(web.Close)
	c.deps.Modules.GitHub = &modcall.HTTPCaller{
		URLs:  map[string]string{consolewire.Module: web.URL},
		Token: func(context.Context, string) (string, error) { return "token", nil },
	}
}

// A GitHub module in another process mints but keeps no ring: the issuer
// records each request itself. The page is answered from the issuer's ring,
// with the module asked only which App this is.
func TestARemoteModulesAppTokensAreAnsweredFromTheIssuersRing(t *testing.T) {
	t.Parallel()
	rig, ring := tokensRig(t)
	ring.Add("renovate", mints.Token{At: kept.Add(time.Hour), Subject: "github:globex/site", Proof: "ci", Outcome: "ok"})
	remoteModule(t, rig.console)

	listed := rig.tokens(t, "renovate")
	if len(listed.GetTokens()) != 1 || listed.GetTokens()[0].GetSubject() != "github:globex/site" {
		t.Fatalf("tokens = %+v, want the one the ring holds", listed.GetTokens())
	}
	if listed.GetKeptSince().AsTime() != kept {
		t.Errorf("kept_since = %v, want %v", listed.GetKeptSince().AsTime(), kept)
	}

	// The module still decides what the App is: an App nothing declares is
	// not_found, and one that mints no tokens is refused, as locally.
	_, err := rig.console.ListGitHubAppTokens(operator(),
		connect.NewRequest(&directoryrosterv1.ListGitHubAppTokensRequest{Id: "no-such-app"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an App nothing declares = %v, want not_found", err)
	}
	rig.connect(t, rig.github.pem)
	_, err = rig.console.ListGitHubAppTokens(operator(),
		connect.NewRequest(&directoryrosterv1.ListGitHubAppTokensRequest{Id: "globex-controller"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a controller App = %v, want failed_precondition", err)
	}

	// Truly no ring anywhere keeps the sentence.
	rig.console.deps.GitHubMints = nil
	_, err = rig.console.ListGitHubAppTokens(operator(),
		connect.NewRequest(&directoryrosterv1.ListGitHubAppTokensRequest{Id: "renovate"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("no ring = %v, want failed_precondition", err)
	}
}

// The people search narrows by the links, which a module in another process
// keeps: it is asked, and a module that keeps none still says so.
func TestARemoteModulesLinksAnswerThePeopleFilter(t *testing.T) {
	t.Parallel()
	ctx := asIdentity(everywhere)
	now := time.Now().UTC()
	rig, _ := tokensRig(t)
	rig.console.deps.GitHubLinks = &memoryLinks{byID: map[int64]link.Link{
		11: {ID: 11, Login: "ada-a", Emails: []string{"Ada@Globex.example"}, State: link.StateLinked, LinkedAt: now},
		12: {ID: 12, Login: "gone-g", Emails: []string{"gone@globex.example"}, State: link.StateLost, LinkedAt: now},
	}}
	remoteModule(t, rig.console)
	// The issuer's own deps hold no store: only the module has the links.
	rig.console.deps.GitHubLinks = nil

	logins, known, err := rig.console.githubLogins(ctx)
	if err != nil || !known || len(logins) != 1 || logins["ada@globex.example"] != "ada-a" {
		t.Fatalf("githubLogins = %+v, %v, %v; want ada only, known", logins, known, err)
	}

	// A module with no link store: not known, and no error.
	remoteModule(t, rig.console)
	if logins, known, err = rig.console.githubLogins(ctx); logins != nil || known || err != nil {
		t.Errorf("githubLogins(module without links) = %+v, %v, %v", logins, known, err)
	}
}
