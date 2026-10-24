package console_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/outlineroster/connection"
	"github.com/truvity/sluis/internal/outlineroster/console"
	"github.com/truvity/sluis/internal/outlineroster/outlinefake"
	"github.com/truvity/sluis/internal/outlineroster/status"
	"github.com/truvity/sluis/policy"
)

// outlineRecords is the console's Outline store, in memory.
type outlineRecords struct {
	mu            sync.Mutex
	records       map[string]connection.Record
	keys          map[string]string
	confirmations []connection.Confirmation
	requests      map[string]time.Time
}

func (o *outlineRecords) List(context.Context) ([]connection.Record, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []connection.Record
	for k := range o.records {
		r := o.records[k]
		out = append(out, r)
	}
	return out, nil
}

func (o *outlineRecords) Record(_ context.Context, ws string) (connection.Record, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	r, ok := o.records[ws]
	return r, ok, nil
}

func (o *outlineRecords) Put(_ context.Context, r connection.Record, c connection.Credential) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records[r.Workspace], o.keys[r.Workspace] = r, c.APIKey
	return nil
}

func (o *outlineRecords) Delete(_ context.Context, ws string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.records, ws)
	delete(o.keys, ws)
	return nil
}

func (o *outlineRecords) PutConfirmation(_ context.Context, c connection.Confirmation) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.confirmations = append(o.confirmations, c)
	return nil
}

func (o *outlineRecords) RequestPass(_ context.Context, r connection.PassRequest) (bool, time.Time, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	last := o.requests[r.Workspace]
	if r.At.Sub(last) < time.Minute {
		return false, last, nil
	}
	o.requests[r.Workspace] = r.At
	return true, last, nil
}

type outlineReports map[string]string

func (r outlineReports) Reports(context.Context) (map[string]string, error) { return r, nil }

const outlineConsolePolicy = `
version: 1
groups:
  all:wiki:admin: { members: [wiki-admins@example.com] }
outline:
  workspaces:
    wiki:
      ignore: [key.owner@example.com]
      roles: { admin: [all:wiki:admin] }
      collections:
        General: { read_write: [all:wiki:admin] }
`

func asRole(role access.Role) consolecall.Caller {
	return consolecall.Caller{Actor: "ada@example.com", Roles: []string{string(role)}, Source: consolecall.SourcePerson}
}

func code(err error) connect.Code { return connect.CodeOf(err) }

func outlineConsole(t *testing.T) (*console.Service, *outlineRecords, *outlinefake.Outline, outlineReports, *audittest.Recorder) {
	t.Helper()
	declared, err := policy.Parse([]byte(outlineConsolePolicy))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	o := outlinefake.New(t)
	records := &outlineRecords{records: map[string]connection.Record{}, keys: map[string]string{}, requests: map[string]time.Time{}}
	reports := outlineReports{}
	rec := audittest.New(t)
	c := console.NewService(console.Deps{
		Policy: set, Workspaces: records, Status: reports, Enabled: []string{"wiki"}, HTTP: o.Client(), Audit: rec,
	})
	return c, records, o, reports, rec
}

// Connecting checks the key with Outline: an admin's key is kept, with who it
// is, a member's is refused, and a viewer may not connect at all.
func TestConnectingAnOutlineWorkspaceChecksTheKey(t *testing.T) {
	t.Parallel()
	c, records, o, _, rec := outlineConsole(t)
	member := o.AddUser("M", "m@example.com", "member")
	o.Keys["key-member"] = member.ID
	req := &directoryrosterv1.ConnectOutlineWorkspaceRequest{Workspace: "wiki", Url: o.URL(), ApiKey: "key-member"}

	if _, err := c.ConnectOutlineWorkspace(context.Background(), asRole(access.RoleViewer), req); code(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer connecting: %v, want permission denied", err)
	}
	if _, err := c.ConnectOutlineWorkspace(context.Background(), asRole(access.RoleOperator), req); code(err) != connect.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "not an Outline admin") {
		t.Errorf("a member's key: %v, want refused", err)
	}
	if len(records.records) != 0 {
		t.Fatal("a refused key was kept")
	}
	req.ApiKey = "key-admin"
	resp, err := c.ConnectOutlineWorkspace(context.Background(), asRole(access.RoleOperator), req)
	if err != nil {
		t.Fatalf("an admin's key: %v", err)
	}
	if resp.GetKeyUser() != "key.owner@example.com" || !resp.GetKeyUserIgnored() {
		t.Errorf("response = %+v, want the key's user, on the ignore list", resp)
	}
	if r := records.records["wiki"]; r.Team != o.Team.ID || r.KeyUser != "key.owner@example.com" || records.keys["wiki"] != "key-admin" {
		t.Errorf("kept %+v", r)
	}
	if n := len(rec.Find("roster.outline_workspace.connected")); n != 2 {
		t.Errorf("recorded %d connects (the refusal and the success), want 2", n)
	}

	// A key of another Outline team is refused for a workspace connected
	// before.
	r := records.records["wiki"]
	r.Team = "another-team"
	records.records["wiki"] = r
	if _, err = c.ConnectOutlineWorkspace(context.Background(), asRole(access.RoleOperator), req); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("a key of another team: %v, want refused", err)
	}

	// A workspace the policy does not declare, and a URL that is not https.
	if _, err = c.ConnectOutlineWorkspace(context.Background(), asRole(access.RoleOperator),
		&directoryrosterv1.ConnectOutlineWorkspaceRequest{Workspace: "other", Url: o.URL(), ApiKey: "key-admin"}); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an undeclared workspace: %v", err)
	}
	if _, err = c.ConnectOutlineWorkspace(context.Background(), asRole(access.RoleOperator),
		&directoryrosterv1.ConnectOutlineWorkspaceRequest{Workspace: "wiki", Url: "http://wiki.example.com", ApiKey: "key-admin"},
	); code(err) != connect.CodeInvalidArgument {
		t.Errorf("an http URL: %v", err)
	}
}

// The status lists every declared workspace, its connection and its report,
// never the key; a confirmation must name the latest report's set.
func TestOutlineStatusAndConfirmation(t *testing.T) {
	t.Parallel()
	c, records, _, reports, rec := outlineConsole(t)
	records.records["wiki"] = connection.Record{Workspace: "wiki", URL: "https://wiki.example.com", KeyUser: "key.owner@example.com"}
	records.keys["wiki"] = "secret-key-value"
	raw, err := status.Encode(status.Workspace{Workspace: "wiki", Enabled: true,
		Breakers: []status.Breaker{{Scope: connection.ScopeSuspensions, Fingerprint: "f00d", Affected: 4, Total: 9}}})
	if err != nil {
		t.Fatal(err)
	}
	reports[status.Key("wiki")] = raw

	got, err := c.GetOutlineStatus(context.Background(), asRole(access.RoleViewer), &directoryrosterv1.GetOutlineStatusRequest{})
	if err != nil || len(got.GetWorkspaces()) != 1 {
		t.Fatalf("status = %+v, %v", got, err)
	}
	ws := got.GetWorkspaces()[0]
	if !ws.GetConnected() || !ws.GetEnabled() || ws.GetReportJson() != raw || strings.Contains(got.String(), "secret-key-value") {
		t.Errorf("status = %+v", ws)
	}

	confirm := &directoryrosterv1.ConfirmOutlineBreakerRequest{Workspace: "wiki", Scope: connection.ScopeSuspensions, Fingerprint: "beef"}
	if _, err = c.ConfirmOutlineBreaker(context.Background(), asRole(access.RoleOperator), confirm); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("a fingerprint the report does not show: %v", err)
	}
	confirm.Fingerprint = "f00d"
	if _, err = c.ConfirmOutlineBreaker(context.Background(), asRole(access.RoleOperator), confirm); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if len(records.confirmations) != 1 || records.confirmations[0].By != "ada@example.com" {
		t.Errorf("confirmations = %+v", records.confirmations)
	}
	if len(rec.Find("roster.outline_breaker.confirmed")) != 1 {
		t.Error("the confirmation was not recorded")
	}

	resp, err := c.RequestOutlinePass(context.Background(), asRole(access.RoleOperator), &directoryrosterv1.RequestOutlinePassRequest{Workspace: "wiki"})
	if err != nil || !resp.GetKept() {
		t.Errorf("request = %+v, %v", resp, err)
	}
	disconnect := &directoryrosterv1.DisconnectOutlineWorkspaceRequest{Workspace: "wiki"}
	if _, err = c.DisconnectOutlineWorkspace(context.Background(), asRole(access.RoleOperator), disconnect); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if _, ok := records.keys["wiki"]; ok || len(rec.Find("roster.outline_workspace.disconnected")) != 1 {
		t.Error("the key outlived the disconnect, or it was not recorded")
	}
}
