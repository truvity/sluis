package console

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/connection"
	"github.com/truvity/sluis/policy"
)

// An App's name is its slug once created, else the name it is created with.
func TestAnAppIsNamedBySlugOnceItExists(t *testing.T) {
	spec := githubAppSpec{purpose: appController, appSlug: "people-of-globex", entry: githubapp.OrganisationApp("globex")}
	if got := spec.declaredName(); got != "people-of-globex" {
		t.Errorf("declaredName = %q", got)
	}
}

// A scoped caller operates an App only through the directory that owns its
// organisation; the link App is installation-wide alone; the controller App of
// an organisation nobody connected is anyone's who could connect it.
func TestWhoMayOperateWhichApp(t *testing.T) {
	north := access.Identity{Scopes: map[string]access.Role{"C0north": access.RoleOperator}}
	everywhere := access.Identity{Role: access.RoleOperator}
	cases := []struct {
		name string
		spec githubAppSpec
		id   access.Identity
		want bool
	}{
		{"the owner's operator", githubAppSpec{purpose: appRunners, created: true, owner: "C0north"}, north, true},
		{"a foreign owner", githubAppSpec{purpose: appRunners, created: true, owner: "C0south"}, north, false},
		{"the link App, scoped", githubAppSpec{purpose: appLink}, north, false},
		{"the link App, installation-wide", githubAppSpec{purpose: appLink}, everywhere, true},
		{"an organisation not yet connected", githubAppSpec{purpose: appController}, north, true},
	}
	for _, c := range cases {
		if got := mayApp(c.id, access.RoleOperator, &c.spec); got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
}

// Reading an App through the module is refused to a caller who owns none of the
// organisations, and a snapshot of nobody is unauthenticated.
func TestListingAppsNeedsAnOrganisationToSee(t *testing.T) {
	declared, err := policy.Parse([]byte("version: 1\ngroups:\n  all:g:e: { matchers: [{ email_domain: g.example }] }\n" +
		"github:\n  globex:\n    members: [all:g:e]\n"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	store := &memConn{records: map[string]connection.Record{"globex": {Org: "globex", Owner: "C0north", AppID: 1, AppSlug: "g"}}}
	svc := New(Deps{Orgs: store, Policy: func() *policy.Set { return set }})
	list := func(ctx context.Context) error {
		_, err := svc.ListGitHubApps(ctx, &directoryrosterv1.ListGitHubAppsRequest{})
		return err
	}
	if err := list(
		WithCaller(context.Background(), consolecall.Caller{Actor: "x@y.test", Workspaces: map[string]string{"C0south": "viewer"}}),
	); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer of a directory that owns nothing = %v, want permission denied", err)
	}
	if err := list(context.Background()); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("nobody = %v, want unauthenticated", err)
	}
	if err := list(WithCaller(context.Background(), consolecall.Caller{Actor: "x@y.test", Workspaces: map[string]string{"C0north": "viewer"}})); err != nil {
		t.Errorf("the owner's viewer = %v", err)
	}
}

// memConn is a read-only store of connections.
type memConn struct {
	connection.Store
	records map[string]connection.Record
}

func (m *memConn) List(context.Context) ([]connection.Record, error) {
	out := make([]connection.Record, 0, len(m.records))
	for k := range m.records {
		out = append(out, m.records[k])
	}
	return out, nil
}

func (m *memConn) PassRequests(context.Context) (map[string]connection.PassRequest, error) {
	return nil, nil
}

func (m *memConn) Credential(context.Context, string) (connection.Credential, bool, error) {
	return connection.Credential{}, false, nil
}

type kubeStore struct{}

func (kubeStore) SecretName() string { return "sluis-github-orgs" }

type portsPlaces struct{}

func (portsPlaces) GitHubAppPlace(id string) (string, string, bool) {
	return "ssm", "/sluis/prod/internal/github/apps/" + id + "/", true
}

// Where a key is kept follows the store: a Kubernetes store names its Secret
// and the keys within it, a deployment on the ports names the adapter and the
// address of the App, and neither is claimed where nothing says.
func TestWhereAKeyIsKept(t *testing.T) {
	keys := []string{"link"}
	for _, tc := range []struct {
		name   string
		deps   Deps
		store  any
		id     string
		expect keyPlace
	}{
		{"kubernetes", Deps{}, kubeStore{}, "x", keyPlace{adapter: "kubernetes", location: "sluis-github-orgs", keys: keys}},
		{"ssm", Deps{Places: portsPlaces{}}, struct{}{}, "app-1", keyPlace{adapter: "ssm", location: "/sluis/prod/internal/github/apps/app-1/"}},
		{"unknown", Deps{}, struct{}{}, "app-1", keyPlace{}},
		{"no id", Deps{Places: portsPlaces{}}, struct{}{}, "", keyPlace{}},
	} {
		svc := &Service{deps: tc.deps}
		got := svc.placeOf(tc.store, tc.id, keys)
		if got.adapter != tc.expect.adapter || got.location != tc.expect.location || len(got.keys) != len(tc.expect.keys) {
			t.Errorf("%s: placeOf = %+v, want %+v", tc.name, got, tc.expect)
		}
	}
}

// notingConn is a store of connections that says why an organisation does not
// use the App declared for it.
type notingConn struct {
	memConn
	note string
}

func (n *notingConn) DeclaredAppNote(context.Context, string) (string, error) { return n.note, nil }

// An organisation's App says why the App declared for it is not in use, beside
// what GitHub says of the App that is, and says nothing when there is nothing
// to say.
func TestAnOrganisationsAppSaysWhyItsDeclaredAppIsNotInUse(t *testing.T) {
	record := connection.Record{Org: "globex", AppID: 1, AppSlug: "g", InstallationID: 2}
	store := &notingConn{
		memConn: memConn{records: map[string]connection.Record{"globex": record}},
		note:    "the App globex-declared is created and not installed in it yet",
	}
	svc := New(Deps{Orgs: store})
	specs, err := svc.controllerAppSpecs(context.Background(), nil, map[string]bool{})
	if err != nil || len(specs) != 1 || specs[0].note != store.note {
		t.Fatalf("specs = %+v, %v, want the note", specs, err)
	}
	if got := withNote("", specs[0].note); got != store.note {
		t.Errorf("a note alone reads %q", got)
	}
	if got := withNote("GitHub refused", specs[0].note); got != "GitHub refused; "+store.note {
		t.Errorf("a note after a reason reads %q", got)
	}
	store.note = ""
	if specs, err = svc.controllerAppSpecs(context.Background(), nil, map[string]bool{}); err != nil || specs[0].note != "" {
		t.Errorf("specs = %+v, %v, want no note", specs, err)
	}
	if got := withNote("GitHub refused", ""); got != "GitHub refused" {
		t.Errorf("no note changed the reason: %q", got)
	}
}
