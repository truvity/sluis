package controller_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/outlineroster/connection"
	"github.com/truvity/sluis/internal/outlineroster/controller"
	"github.com/truvity/sluis/internal/outlineroster/outlineapi"
	"github.com/truvity/sluis/internal/outlineroster/outlinefake"
	"github.com/truvity/sluis/internal/outlineroster/status"
	"github.com/truvity/sluis/policy"
)

// basePolicy binds one workspace: an admin role, an everyone collection and a
// collection of one company, which names that company's directory.
const basePolicy = `
version: 1
groups:
  all:wiki:admin: { members: [wiki-admins@example.com] }
  all:wiki:acme: { matchers: [{ email_domain: example.com }] }
  all:wiki:globex: { matchers: [{ email_domain: example.org }] }
clients:
  wiki: { kind: confidential, secret: wiki-oidc, redirects: [https://wiki.example.com/auth/oidc.callback], requires: [all:wiki:acme, all:wiki:globex] }
outline:
  workspaces:
    wiki:
      client: wiki
      roles: { admin: [all:wiki:admin] }
      ignore: [key.owner@example.com]
      collections:
        General: { mode: strict, read_write: [all:wiki:acme, all:wiki:globex], admin: [all:wiki:admin] }
        Acme: { mode: strict, directory: example.com, read_write: [all:wiki:acme] }
`

const digest = "policy-digest"

// person is what the fake directory says of one address.
type person struct {
	authoritative, found, suspended bool
	groups                          []string
	err                             error
}

// fakeDirectory is the console's AccessService as the controller asks it.
type fakeDirectory struct {
	mu      sync.Mutex
	people  map[string]person
	digest  string
	served  []*directoryrosterv1.DirectoryDomains
	groups  map[string]string // directory group address -> its directory's id
	explain int
}

func (d *fakeDirectory) Explain(_ context.Context, req *directoryrosterv1.ExplainRequest) (*directoryrosterv1.ExplainResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.explain++
	p, ok := d.people[req.GetEmail()]
	if !ok {
		p = person{authoritative: true}
	}
	if p.err != nil {
		return nil, p.err
	}
	out := &directoryrosterv1.ExplainResponse{Authoritative: p.authoritative, Found: p.found, Suspended: p.suspended, PolicyDigest: d.digest}
	admitted := false
	for _, g := range p.groups {
		out.Held = append(out.Held, &directoryrosterv1.HeldGroup{Group: g})
		admitted = admitted || g == "all:wiki:acme" || g == "all:wiki:globex"
	}
	out.Clients = []*directoryrosterv1.ClientAdmission{{Id: "wiki", Admitted: admitted}}
	return out, nil
}

func (d *fakeDirectory) ListServedDomains(context.Context, *directoryrosterv1.ListServedDomainsRequest) (*directoryrosterv1.ListServedDomainsResponse, error) {
	return &directoryrosterv1.ListServedDomainsResponse{Directories: d.served}, nil
}

func (d *fakeDirectory) ResolveDirectoryGroups(
	_ context.Context, req *directoryrosterv1.ResolveDirectoryGroupsRequest,
) (*directoryrosterv1.ResolveDirectoryGroupsResponse, error) {
	out := &directoryrosterv1.ResolveDirectoryGroupsResponse{PolicyDigest: d.digest}
	for _, g := range req.GetGroups() {
		owner, found := d.groups[g]
		out.Groups = append(out.Groups, &directoryrosterv1.ResolvedDirectoryGroup{Email: g, Found: found, WorkspaceId: owner, Authoritative: true})
	}
	return out, nil
}

// set says the directory has an address live, holding groups.
func (d *fakeDirectory) set(email string, groups ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.people[email] = person{authoritative: true, found: true, groups: groups}
}

func (d *fakeDirectory) setPerson(email string, p person) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.people[email] = p
}

// records is the console's records of one workspace.
type records struct {
	mu            sync.Mutex
	record        connection.Record
	credential    connection.Credential
	connected     bool
	confirmations []connection.Confirmation
}

func (r *records) Get(_ context.Context, workspace string) (connection.Record, connection.Credential, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.connected || workspace != r.record.Workspace {
		return connection.Record{}, connection.Credential{}, false, nil
	}
	return r.record, r.credential, true, nil
}

func (r *records) Confirmations(context.Context) ([]connection.Confirmation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.confirmations), nil
}

func (r *records) PassRequests(context.Context) (map[string]connection.PassRequest, error) {
	return nil, nil
}

func (r *records) Digest(context.Context) ([32]byte, error) { return [32]byte{}, nil }

// reports is the report store.
type reports struct {
	mu   sync.Mutex
	docs map[string]string
}

func (s *reports) Replace(_ context.Context, documents map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs = maps.Clone(documents)
	return nil
}

func (s *reports) Put(_ context.Context, key, document string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.docs == nil {
		s.docs = map[string]string{}
	}
	s.docs[key] = document
	return nil
}

func (s *reports) Reports(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.docs), nil
}

type harness struct {
	t       *testing.T
	o       *outlinefake.Outline
	dir     *fakeDirectory
	rec     *records
	reports *reports
	audit   *audittest.Recorder
	ctl     *controller.Controller
	now     time.Time
}

type option func(*controller.Config, *policy.Policy)

func enabled() option {
	return func(c *controller.Config, _ *policy.Policy) { c.Enabled = map[string]bool{"wiki": true} }
}

func newHarness(t *testing.T, text string, opts ...option) *harness {
	t.Helper()
	declared, err := policy.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policy.NewSet(declared); err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t: t, o: outlinefake.New(t),
		dir: &fakeDirectory{people: map[string]person{}, digest: digest, groups: map[string]string{
			"wiki-admins@example.com": "dir-acme", "wiki-managers@example.com": "dir-acme",
		},
			served: []*directoryrosterv1.DirectoryDomains{
				{WorkspaceId: "dir-acme", PrimaryDomain: "example.com", Domains: []string{"example.com"}},
				{WorkspaceId: "dir-globex", PrimaryDomain: "example.org", Domains: []string{"example.org"}},
			}},
		reports: &reports{}, audit: audittest.New(t),
		now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	}
	h.rec = &records{connected: true,
		record: connection.Record{
			Version: 1, Workspace: "wiki", URL: h.o.URL(), KeyUser: "key.owner@example.com", Team: "team-1",
		},
		credential: connection.Credential{Version: 1, Workspace: "wiki", APIKey: "key-admin"},
	}
	// The key's own user is an admin the directory has.
	h.dir.set("key.owner@example.com", "all:wiki:admin", "all:wiki:acme")
	cfg := controller.Config{ShareScan: -1}
	for _, o := range opts {
		o(&cfg, &declared)
	}
	h.ctl = controller.New(cfg, controller.Deps{
		Directory: h.dir, Audit: h.audit, Status: h.reports, Records: h.rec, Policy: declared, Digest: digest,
		Outline: func(url, key string) *outlineapi.Client {
			return outlineapi.New(url, key, outlineapi.WithHTTPClient(h.o.Client()))
		},
		Now: func() time.Time { return h.now },
	})
	return h
}

// pass ticks the workspace once and returns its report.
func (h *harness) pass() status.Workspace {
	h.t.Helper()
	h.ctl.Tick(h.t.Context(), "wiki")
	h.now = h.now.Add(2 * time.Minute)
	raw, ok := h.reports.docs[status.Key("wiki")]
	if !ok {
		h.t.Fatal("no report was published")
	}
	report, err := status.Decode(raw)
	if err != nil {
		h.t.Fatal(err)
	}
	return report
}

// mutations are the changing calls since the last call of mutations.
func (h *harness) methods() []string {
	var out []string
	for _, c := range h.o.Mutations() {
		out = append(out, c.Method)
	}
	return out
}

func userRow(report status.Workspace, email string) status.User {
	for i := range report.Users {
		u := &report.Users[i]
		if u.Email == email {
			return *u
		}
	}
	return status.User{}
}

func groupMember(report status.Workspace, group, email string) status.Member {
	for _, g := range report.Groups {
		if g.Name != group {
			continue
		}
		for _, m := range g.Members {
			if m.Email == email {
				return m
			}
		}
	}
	return status.Member{}
}

func collectionRow(report status.Workspace, name string) status.Collection {
	for i := range report.Collections {
		c := &report.Collections[i]
		if c.Name == name {
			return *c
		}
	}
	return status.Collection{}
}

func has(actions []string, action string) bool { return slices.Contains(actions, action) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

var errUnreachable = errors.New("the directory is unreachable")
