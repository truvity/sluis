package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/consolecall"
	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/modcall"
)

// moduleOver is the GitHub module's server over the stores the console under
// test holds, for a test that reaches it through a transport.
func moduleOver(c *Console) *modcall.Server {
	deps := ghconsole.Deps{Orgs: c.deps.GitHubOrgs, Catalogue: c.deps.GitHubCatalogue, PublicURL: c.deps.PublicURL, RootURL: c.deps.RootURL, HTTP: c.deps.GitHubHTTP}
	if c.deps.Authorizer != nil {
		deps.Policy = c.deps.Authorizer.Policy
	}
	srv := modcall.NewServer(consolewire.Module)
	ghconsole.Register(srv, ghconsole.New(deps))
	return srv
}

// The module is reached the same way in this process and in another: the
// console's answer is the same.
func TestTheGitHubOperationsAreTheSameLocalAndRemote(t *testing.T) {
	store := newMemoryConnections()
	seedOrganisations(t, store)
	_, console := ownerConsoleOver(t, store)
	ask := func() error {
		_, err := console.ChangeGitHubOrganisationOwner(asIdentity(everywhere), connect.NewRequest(
			&directoryrosterv1.ChangeGitHubOrganisationOwnerRequest{Org: "initech", OwnerDirectory: "C0north"}))
		return err
	}
	if err := ask(); err != nil {
		t.Fatalf("local: %v", err)
	}
	if records, _ := store.List(context.Background()); recordOwner(records, "initech") != "C0north" {
		t.Fatalf("local: the owner was not changed: %+v", records)
	}

	// Remote: the same module behind its HTTP service, called as the class its
	// verified subject names.
	srv := httptest.NewServer(moduleOver(console).Handler(func(context.Context, string) (string, error) { return "console", nil }))
	t.Cleanup(srv.Close)
	console.deps.Modules.GitHub = &modcall.HTTPCaller{
		URLs:  map[string]string{consolewire.Module: srv.URL},
		Token: func(context.Context, string) (string, error) { return "token", nil },
	}
	if _, _, err := store.SetOwner(context.Background(), "initech", ""); err != nil {
		t.Fatal(err)
	}
	if err := ask(); err != nil {
		t.Fatalf("remote: %v", err)
	}
	if records, _ := store.List(context.Background()); recordOwner(records, "initech") != "C0north" {
		t.Fatalf("remote: the owner was not changed: %+v", records)
	}
}

// A console method answers the console class and no other: the transport names
// the class, and a payload cannot claim to be the console.
func TestAConsoleMethodRefusesAnotherCallerClass(t *testing.T) {
	_, console := ownerConsoleOver(t, newMemoryConnections())
	srv := httptest.NewServer(moduleOver(console).Handler(func(context.Context, string) (string, error) { return "issuer", nil }))
	t.Cleanup(srv.Close)
	console.deps.Modules.GitHub = &modcall.HTTPCaller{
		URLs:  map[string]string{consolewire.Module: srv.URL},
		Token: func(context.Context, string) (string, error) { return "token", nil },
	}
	_, err := console.RequestGitHubPass(asIdentity(everywhere), connect.NewRequest(&directoryrosterv1.RequestGitHubPassRequest{Org: "globex"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a console method called as the issuer = %v, want permission denied", err)
	}
	if msg := err.Error(); strings.Contains(msg, "globex") {
		t.Errorf("the refusal names what was asked: %s", msg)
	}
}

// The module authorizes on the snapshot: a role the console's coarse gate
// passes is still refused over an organisation another directory owns, with
// the module's words.
func TestTheModuleRefusesWhatTheSnapshotMayNotDo(t *testing.T) {
	store := newMemoryConnections()
	seedOrganisations(t, store)
	_, console := ownerConsoleOver(t, store)
	_, err := console.DisconnectGitHubOrganisation(asIdentity(southOp), connect.NewRequest(
		&directoryrosterv1.DisconnectGitHubOrganisationRequest{Org: "globex"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a foreign operator = %v, want permission denied", err)
	}
	if records, _ := store.List(context.Background()); recordOwner(records, "globex") != "C0north" {
		t.Errorf("a refused disconnect changed the records: %+v", records)
	}
	// And a snapshot of nobody is unauthenticated before it reaches the module.
	_, err = console.DisconnectGitHubOrganisation(context.Background(), connect.NewRequest(
		&directoryrosterv1.DisconnectGitHubOrganisationRequest{Org: "globex"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("nobody = %v, want unauthenticated", err)
	}
}

type countingCaller struct {
	calls atomic.Int32
	err   error
}

func (c *countingCaller) Call(context.Context, string, string, []byte) ([]byte, error) {
	c.calls.Add(1)
	return nil, c.err
}

// A flow's callback that the module cannot finish is told in fixed words, a
// 4xx page, with the reason in the log and no second attempt: GitHub's code is
// single use.
func TestAFailedCallbackForwardIsNotRetried(t *testing.T) {
	startFakeGitHub(t)
	store := newMemoryConnections()
	server, console := connectServer(t, store)
	begun, err := console.BeginGitHubConnect(operator(), connect.NewRequest(&directoryrosterv1.BeginGitHubConnectRequest{Org: "globex"}))
	if err != nil {
		t.Fatal(err)
	}
	cookie, state := cookieFrom(t, begun.Header()), mustQuery(t, begun.Msg.GetUrl(), "state")

	failing := &countingCaller{err: errors.New("lambda: throttled secret-detail-1234")}
	console.deps.Modules.GitHub = failing
	got := redirect(server.githubCallback, githubCallbackPath, url.Values{"code": {"created"}, "state": {state}}, cookie)
	if got.Code != http.StatusConflict {
		t.Fatalf("a module that fails = %d, want a 4xx page:\n%s", got.Code, got.Body)
	}
	if strings.Contains(got.Body.String(), "secret-detail-1234") {
		t.Errorf("the page tells the hop's failure:\n%s", got.Body)
	}
	if !strings.Contains(got.Body.String(), "single") && !strings.Contains(got.Body.String(), "once") {
		t.Errorf("the page does not say the code is spent:\n%s", got.Body)
	}
	if n := failing.calls.Load(); n != 1 {
		t.Errorf("the forward was attempted %d times, want once", n)
	}
	if records := mustList(t, store); len(records) != 0 {
		t.Errorf("a failed forward kept %+v", records)
	}
}

// What a module tells a flow as a Problem is the page, with its status.
func TestAFlowRefusalFromTheModuleIsThePage(t *testing.T) {
	startFakeGitHub(t)
	server, console := connectServer(t, newMemoryConnections())
	begun, err := console.BeginGitHubConnect(operator(), connect.NewRequest(&directoryrosterv1.BeginGitHubConnectRequest{Org: "globex"}))
	if err != nil {
		t.Fatal(err)
	}
	cookie, state := cookieFrom(t, begun.Header()), mustQuery(t, begun.Msg.GetUrl(), "state")
	console.deps.Modules.GitHub = problemCaller{&consolecall.Problem{Summary: "no thanks", Status: http.StatusForbidden}}
	got := redirect(server.githubCallback, githubCallbackPath, url.Values{"code": {"created"}, "state": {state}}, cookie)
	if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "no thanks") {
		t.Errorf("a refusal = %d:\n%s", got.Code, got.Body)
	}
}

type problemCaller struct{ p *consolecall.Problem }

func (c problemCaller) Call(context.Context, string, string, []byte) ([]byte, error) {
	return json.Marshal(consolecall.Reply{Problem: c.p})
}

func recordOwner(records []connection.Record, org string) string {
	r, _ := recordOf(records, org)
	return r.Owner
}
