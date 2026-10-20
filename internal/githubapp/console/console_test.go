package console_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubapp/console"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/link"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/policy"
)

// memoryConnections is the store, in memory.
type memoryConnections struct {
	mu          sync.Mutex
	records     map[string]connection.Record
	credentials map[string]connection.Credential
	passes      map[string]connection.PassRequest
}

func newMemoryConnections() *memoryConnections {
	return &memoryConnections{records: map[string]connection.Record{}, credentials: map[string]connection.Credential{}}
}

func (m *memoryConnections) Put(_ context.Context, r connection.Record, c connection.Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[r.Org], m.credentials[c.Org] = r, c
	return nil
}

func (m *memoryConnections) List(context.Context) ([]connection.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]connection.Record, 0, len(m.records))
	for _, org := range slices.Sorted(maps.Keys(m.records)) {
		out = append(out, m.records[org])
	}
	return out, nil
}

func (m *memoryConnections) Credential(_ context.Context, org string) (connection.Credential, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.credentials[org]
	return c, ok, nil
}

func (m *memoryConnections) SetOwner(_ context.Context, org, owner string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[org]
	if !ok {
		return "", false, nil
	}
	previous := r.Owner
	r.Owner = owner
	m.records[org] = r
	return previous, true, nil
}

func (m *memoryConnections) RequestPass(_ context.Context, r connection.PassRequest) (bool, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.passes == nil {
		m.passes = map[string]connection.PassRequest{}
	}
	kept, last := true, time.Time{}
	if prev, ok := m.passes[r.Org]; ok {
		last = prev.At
		kept = r.At.Sub(prev.At) >= connection.PassGap
	}
	if kept {
		m.passes[r.Org] = r
	}
	return kept, last, nil
}

func (m *memoryConnections) PassRequests(context.Context) (map[string]connection.PassRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.passes), nil
}

func (m *memoryConnections) Delete(_ context.Context, org string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, org)
	delete(m.credentials, org)
	return nil
}

const boundPolicy = `
version: 1
groups:
  all:globex:employee: { matchers: [{ email_domain: globex.example }] }
github:
  globex:
    members: [all:globex:employee]
`

func service(t *testing.T, store *memoryConnections) *console.Service {
	t.Helper()
	declared, err := policy.Parse([]byte(boundPolicy))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	return console.New(console.Deps{Orgs: store, Policy: func() *policy.Set { return set }, PublicURL: "https://access.example", HTTP: nil})
}

func seed(t *testing.T, store *memoryConnections) {
	t.Helper()
	for org, owner := range map[string]string{"globex": "C0north", "initech": ""} {
		record := connection.Record{Org: org, AppID: 42, AppSlug: org + "-access-roster", InstallationID: 7, Owner: owner}
		if err := store.Put(context.Background(), record, connection.Credential{Org: org, AppID: 42, InstallationID: 7, PrivateKey: "key"}); err != nil {
			t.Fatal(err)
		}
	}
}

func as(actor string, roles []string, workspaces map[string]string) context.Context {
	return console.WithCaller(context.Background(), consolecall.Caller{Actor: actor, Roles: roles, Workspaces: workspaces})
}

var (
	everywhere = func() context.Context { return as("ada@example.test", []string{"operator"}, nil) }
	northOp    = func() context.Context { return as("ada@example.test", nil, map[string]string{"C0north": "operator"}) }
	southOp    = func() context.Context { return as("ada@example.test", nil, map[string]string{"C0south": "operator"}) }
)

func refused(err error) bool { return connect.CodeOf(err) == connect.CodePermissionDenied }

// The pass request is for the organisation's operators and rate limited: the
// rules the console's handler had, now on the caller snapshot.
func TestRequestingAPassIsForTheOrganisationsOperatorsAndRateLimited(t *testing.T) {
	store := newMemoryConnections()
	seed(t, store)
	svc := service(t, store)
	ask := func(ctx context.Context, org string) error {
		_, err := svc.RequestGitHubPass(ctx, &directoryrosterv1.RequestGitHubPassRequest{Org: org})
		return err
	}
	if err := ask(southOp(), "globex"); !refused(err) {
		t.Errorf("a foreign operator = %v, want permission denied", err)
	}
	if err := ask(as("v@example.test", nil, map[string]string{"C0north": "viewer"}), "globex"); !refused(err) {
		t.Errorf("a viewer of the owner = %v, want permission denied", err)
	}
	if err := ask(context.Background(), "globex"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("nobody = %v, want unauthenticated", err)
	}
	if err := ask(everywhere(), "not a login!"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a bad login = %v", err)
	}
	if err := ask(northOp(), "globex"); err != nil {
		t.Fatalf("the owner's operator = %v", err)
	}
	if err := ask(everywhere(), "globex"); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("a second request within a minute = %v, want resource exhausted", err)
	}
	if got, _ := store.PassRequests(context.Background()); got["globex"].By != "ada@example.test" {
		t.Errorf("markers = %+v", got)
	}
}

// Only the installation-wide operator changes an owner, and only to a
// directory the console's snapshot lists, or to none.
func TestOnlyTheInstallationWideOperatorChangesAnOwner(t *testing.T) {
	store := newMemoryConnections()
	seed(t, store)
	svc := service(t, store)
	change := func(ctx context.Context, owner string) error {
		_, err := svc.ChangeGitHubOrganisationOwner(ctx, &directoryrosterv1.ChangeGitHubOrganisationOwnerRequest{Org: "initech", OwnerDirectory: owner})
		return err
	}
	if err := change(northOp(), "C0north"); !refused(err) {
		t.Errorf("a scoped operator = %v, want permission denied", err)
	}
	listed := consolecall.WithFacts(console.WithCaller(context.Background(), consolecall.Caller{
		Actor: "ada@example.test", Roles: []string{"operator"},
	}), &consolecall.DirectoryFacts{Directories: []consolecall.Directory{{ID: "C0north"}}})
	if err := change(listed, "C0nowhere"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("an unconnected directory = %v, want invalid argument", err)
	}
	if err := change(listed, "C0north"); err != nil {
		t.Fatalf("the installation-wide operator = %v", err)
	}
	if records, _ := store.List(context.Background()); records[1].Owner != "C0north" {
		t.Errorf("records = %+v", records)
	}
}

// Disconnecting is the operator of the organisation's owner's, or the
// installation-wide operator's.
func TestDisconnectingAsksWhoOwnsTheOrganisation(t *testing.T) {
	store := newMemoryConnections()
	seed(t, store)
	svc := service(t, store)
	disconnect := func(ctx context.Context) error {
		_, err := svc.DisconnectGitHubOrganisation(ctx, &directoryrosterv1.DisconnectGitHubOrganisationRequest{Org: "globex"})
		return err
	}
	if err := disconnect(southOp()); !refused(err) {
		t.Fatalf("a foreign operator = %v, want permission denied", err)
	}
	if records, _ := store.List(context.Background()); len(records) != 2 {
		t.Fatalf("a refused disconnect forgot something: %+v", records)
	}
	if err := disconnect(northOp()); err != nil {
		t.Fatalf("the owner's operator = %v", err)
	}
	if records, _ := store.List(context.Background()); len(records) != 1 {
		t.Errorf("records = %+v, want the organisation forgotten", records)
	}
}

// Connecting needs a bound organisation, and the URL it returns carries the
// placeholder the issuer signs the state into.
func TestBeginningAConnectLeavesTheStateToTheIssuer(t *testing.T) {
	svc := service(t, newMemoryConnections())
	begun, err := svc.BeginGitHubConnect(everywhere(), "globex", "")
	if err != nil {
		t.Fatal(err)
	}
	if begun.Bind != consolewire.BindOrg+"globex" || begun.Manifest == "" {
		t.Errorf("begun = %+v", begun)
	}
	if want := consolewire.StatePlaceholder; !strings.Contains(begun.URL, want) {
		t.Errorf("url = %s, want the state placeholder", begun.URL)
	}
	if _, err = svc.BeginGitHubConnect(everywhere(), "unbound", ""); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("an organisation the policy does not bind = %v, want failed precondition", err)
	}
	if _, err = svc.BeginGitHubConnect(as("v@example.test", []string{"viewer"}, nil), "globex", ""); !refused(err) {
		t.Errorf("a viewer = %v, want permission denied", err)
	}
}

// Every method the module registers answers the console class and nothing else.
func TestEveryConsoleMethodAnswersTheConsoleOnly(t *testing.T) {
	srv := modcall.NewServer(consolewire.Module)
	console.Register(srv, service(t, newMemoryConnections()))
	if bad := consolecall.Violations(srv); len(bad) != 0 {
		t.Fatalf("methods that break the console rule: %v", bad)
	}
	if len(srv.Methods()) < 6 {
		t.Errorf("only %d methods registered", len(srv.Methods()))
	}
}

func statusOf(t *testing.T, err error) int {
	t.Helper()
	var p *consolecall.Problem
	if !errors.As(err, &p) {
		t.Fatalf("err = %v, want a Problem", err)
	}
	return p.Status
}

// A hand-over that is not a runner flow, or that no runner store backs, is a
// 4xx page; one a foreign operator tries to finish is a 403, before the module
// touches GitHub.
func TestAFlowHandOverIsCheckedBeforeGitHubIsAsked(t *testing.T) {
	store := newMemoryConnections()
	seed(t, store)
	svc := service(t, store)
	flow := consolewire.Flow{Bind: consolewire.BindRunner + "standard:globex", Actor: "ada@example.test", Code: "c"}

	if _, err := svc.CompleteGitHubRunnerCreate(as("", nil, nil), consolewire.Flow{Bind: "github:globex"}); statusOf(t, err) != http.StatusBadRequest {
		t.Errorf("a bind that is not a runner's = %v, want 400", err)
	}
	if _, err := svc.CompleteGitHubRunnerCreate(as("", nil, nil), flow); statusOf(t, err) != http.StatusConflict {
		t.Errorf("a deployment with no runner store = %v, want 409", err)
	}
	if _, err := svc.CompleteGitHubRunnerCreate(southOp(), flow); statusOf(t, err) != http.StatusForbidden {
		t.Errorf("a foreign operator finishing globex's flow = %v, want 403", err)
	}
	if _, err := svc.CompleteGitHubCatalogueCreate(
		as("", nil, nil), consolewire.Flow{Bind: consolewire.BindCatalogue + "renovate"},
	); statusOf(t, err) != http.StatusForbidden {
		t.Errorf("a flow nobody operates = %v, want 403", err)
	}
	if _, err := svc.BeginGitHubRunnerAppConnect(everywhere(), "globex", "standard"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a deployment declaring no tiers = %v, want failed precondition", err)
	}
}

type fixedLinkApp struct{}

func (fixedLinkApp) PutLinkApp(context.Context, link.App, link.AppCredential) error { return nil }
func (fixedLinkApp) LinkApp(context.Context) (link.App, bool, error)                { return link.App{}, true, nil }
func (fixedLinkApp) LinkAppCredential(context.Context) (link.AppCredential, bool, error) {
	return link.AppCredential{AppID: 9, ClientID: "client"}, true, nil
}
func (fixedLinkApp) DeleteLinkApp(context.Context) error { return nil }

type noLinks struct{ link.Links }

// The link page needs no caller and no session; its URL leaves the state to the
// issuer; and where no link App is kept it is a 404 page.
func TestTheLinkPageNeedsNoCallerAndLeavesTheStateToTheIssuer(t *testing.T) {
	svc := console.New(console.Deps{LinkApp: fixedLinkApp{}, Links: noLinks{}, PublicURL: "https://access.example"})
	page, err := svc.GitHubLinkPage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.AuthorizeURL, consolewire.StatePlaceholder) || !strings.Contains(page.AuthorizeURL, "client_id=client") {
		t.Errorf("authorize = %s", page.AuthorizeURL)
	}
	if _, err = console.New(console.Deps{}).GitHubLinkPage(context.Background()); statusOf(t, err) != http.StatusNotFound {
		t.Errorf("no link App kept = %v, want 404", err)
	}
}

// Whoever asks, the module finishes at most a bounded number of links a
// minute, and says so as a 429 before it asks GitHub anything.
func TestLinksAreFinishedAtABoundedRate(t *testing.T) {
	svc := console.New(console.Deps{LinkApp: fixedLinkApp{}, Links: noLinks{}})
	var refused int
	for i := 0; i < 100; i++ {
		_, err := svc.GitHubLinkComplete(context.Background(), consolewire.LinkComplete{GitHubError: "access_denied"})
		var p *consolecall.Problem
		if errors.As(err, &p) && p.Status == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused != 40 {
		t.Errorf("%d of 100 refused as too many, want 40 (60 a minute)", refused)
	}
}

// Confirming a removal set is the organisation's operator's, on the snapshot.
func TestConfirmingRemovalsIsRefusedToAForeignOperator(t *testing.T) {
	store := newMemoryConnections()
	seed(t, store)
	svc := service(t, store)
	_, err := svc.ConfirmGitHubRemovals(southOp(), &directoryrosterv1.ConfirmGitHubRemovalsRequest{Org: "globex", Fingerprint: "abc"})
	if !refused(err) {
		t.Errorf("a foreign operator = %v, want permission denied", err)
	}
}
