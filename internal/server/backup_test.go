package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/backup/rpc"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/policy"
)

type fakeBackup struct {
	status  job.Status
	list    []job.Info
	restore restorejob.Status
	err     error
	limits  []int
	// ran is who each Run was for.
	ran      []string
	startErr error
	preview  rpc.PreviewResponse
}

func (f *fakeBackup) StartRun(_ context.Context, by string) error {
	f.ran = append(f.ran, by)
	return f.startErr
}

func (f *fakeBackup) PreviewRestore(context.Context, string) (rpc.PreviewResponse, error) {
	return f.preview, f.err
}

func (f *fakeBackup) Status(context.Context) (job.Status, error) { return f.status, f.err }
func (f *fakeBackup) List(_ context.Context, limit int) ([]job.Info, error) {
	f.limits = append(f.limits, limit)
	return f.list, f.err
}
func (f *fakeBackup) RestoreStatus(context.Context) (restorejob.Status, error) {
	return f.restore, f.err
}

func newFakeBackup() *fakeBackup {
	at := time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC)
	run := &job.View{ID: "20261010T020000Z-3fa9c1", State: "paused", Started: at, Updated: at, Units: 8, Done: 3, Records: 40}
	done := &job.View{ID: "20261009T020000Z-aa", State: "completed", Started: at.Add(-24 * time.Hour),
		Finished: at.Add(-23 * time.Hour), Modules: 4, Records: 90, Chunks: 6}
	return &fakeBackup{
		status: job.Status{Latest: run, LastCompleted: done, Unfinished: run,
			Retention: &job.Pass{At: at, Keep: 7, MaxAge: "720h", Kept: 7, Removed: []string{"a", "b"}, Failed: []string{"c"}}},
		list: []job.Info{
			{ID: "old", Created: at.Add(-48 * time.Hour), Modules: 4},
			{ID: "new", Created: at.Add(-24 * time.Hour), Modules: 4, Records: 90, Chunks: 6, Layout: "v5"},
			{ID: "bad", Created: at.Add(-72 * time.Hour), Error: "manifest unreadable"},
		},
		restore: restorejob.Status{
			Latest:      &restorejob.Run{ID: "r1", BackupID: "new", State: "paused", Maintenance: "on", Slices: 2},
			Unfinished:  &restorejob.Run{ID: "r1", BackupID: "new", State: "paused"},
			Maintenance: []restorejob.ModuleFlag{{Module: port.ModuleOIDC, State: "restoring", Since: at}, {Module: port.ModuleSlack, Unreadable: true}},
		},
	}
}

func backupConsole(m BackupModule) *Console {
	return &Console{deps: ConsoleDeps{Backup: m}}
}

func TestBackupStatusCarriesTheRunsAndTheRetention(t *testing.T) {
	t.Parallel()
	res, err := backupConsole(newFakeBackup()).GetBackupStatus(asRole(access.RoleViewer),
		connect.NewRequest(&directoryrosterv1.GetBackupStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Msg
	if !m.GetAvailable() || m.GetLatest().GetState() != "paused" || m.GetUnfinished().GetUnits() != 8 || m.GetUnfinished().GetDone() != 3 {
		t.Errorf("status = %v", m)
	}
	if m.GetLastCompleted().GetId() != "20261009T020000Z-aa" {
		t.Errorf("last completed = %v", m.GetLastCompleted())
	}
	if r := m.GetRetention(); r.GetKeep() != 7 || r.GetRemoved() != 2 || r.GetRefused() != 1 || r.GetMaxAge() != "720h" {
		t.Errorf("retention = %v", r)
	}
}

func TestBackupListIsNewestFirstAndKeepsAnUnreadableManifest(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	res, err := backupConsole(f).ListBackups(asRole(access.RoleViewer),
		connect.NewRequest(&directoryrosterv1.ListBackupsRequest{Limit: 10}))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range res.Msg.GetBackups() {
		ids = append(ids, b.GetId())
	}
	if got := ids; len(got) != 3 || got[0] != "new" || got[1] != "old" || got[2] != "bad" {
		t.Errorf("order = %v, want new, old, bad", got)
	}
	if res.Msg.GetBackups()[2].GetError() == "" {
		t.Error("an unreadable manifest lost its error")
	}
	if len(f.limits) != 1 || f.limits[0] != 10 {
		t.Errorf("the limit reached the module as %v", f.limits)
	}
	if _, err := backupConsole(f).ListBackups(asRole(access.RoleViewer),
		connect.NewRequest(&directoryrosterv1.ListBackupsRequest{Limit: -1})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("a negative limit = %v", err)
	}
}

func TestRestoreStatusListsTheModulesUnderMaintenance(t *testing.T) {
	t.Parallel()
	res, err := backupConsole(newFakeBackup()).GetRestoreStatus(asRole(access.RoleViewer),
		connect.NewRequest(&directoryrosterv1.GetRestoreStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Msg
	if !m.GetAvailable() || m.GetLatest().GetBackupId() != "new" || m.GetUnfinished().GetState() != "paused" || len(m.GetMaintenance()) != 2 {
		t.Fatalf("restore status = %v", m)
	}
	if mm := m.GetMaintenance(); mm[0].GetModule() != "oidc" || mm[0].GetSince() == nil || !mm[1].GetUnreadable() {
		t.Errorf("maintenance = %v", mm)
	}
}

func TestTheBackupReadsAreForASignedInViewerAndNoOneElse(t *testing.T) {
	t.Parallel()
	c := backupConsole(newFakeBackup())
	for name, call := range map[string]func(context.Context) error{
		"status": func(ctx context.Context) error {
			_, err := c.GetBackupStatus(ctx, connect.NewRequest(&directoryrosterv1.GetBackupStatusRequest{}))
			return err
		},
		"list": func(ctx context.Context) error {
			_, err := c.ListBackups(ctx, connect.NewRequest(&directoryrosterv1.ListBackupsRequest{}))
			return err
		},
		"restore": func(ctx context.Context) error {
			_, err := c.GetRestoreStatus(ctx, connect.NewRequest(&directoryrosterv1.GetRestoreStatusRequest{}))
			return err
		},
	} {
		if code(call(context.Background())) != connect.CodeUnauthenticated {
			t.Errorf("%s: no sign-in is not unauthenticated", name)
		}
		if code(call(asRole(access.RoleNone))) != connect.CodePermissionDenied {
			t.Errorf("%s: a person with no role is not denied", name)
		}
		if err := call(asRole(access.RoleViewer)); err != nil {
			t.Errorf("%s: a viewer: %v", name, err)
		}
	}
}

func TestWithoutTheModulesThePageIsNotAvailable(t *testing.T) {
	t.Parallel()
	res, err := backupConsole(nil).GetBackupStatus(asRole(access.RoleViewer),
		connect.NewRequest(&directoryrosterv1.GetBackupStatusRequest{}))
	if err != nil || res.Msg.GetAvailable() {
		t.Errorf("no module: %v, %v", res, err)
	}
}

func TestAModuleThatDoesNotAnswerIsUnavailableAndSaysWhy(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	f.err = errors.New("lambda: throttled")
	_, err := backupConsole(f).GetRestoreStatus(asRole(access.RoleViewer),
		connect.NewRequest(&directoryrosterv1.GetRestoreStatusRequest{}))
	if code(err) != connect.CodeUnavailable {
		t.Errorf("code = %v, want unavailable (%v)", code(err), err)
	}
}

// The service starts a backup and previews a restore and has no method that
// starts or resumes a restore. The set is pinned so that adding one is a
// decision the diff shows.
func TestThereIsNoRestoreStartInTheBackupService(t *testing.T) {
	t.Parallel()
	svc := directoryrosterv1.File_directoryroster_v1_backup_proto.Services().ByName("BackupService")
	want := map[string]bool{"GetBackupStatus": true, "ListBackups": true, "GetRestoreStatus": true, "RunBackup": true, "GetRestorePreview": true}
	got := map[string]bool{}
	for i := range svc.Methods().Len() {
		name := string(svc.Methods().Get(i).Name())
		got[name] = true
		// Only the backup run writes, so only it is held by maintenance.
		if isRead := rpcIsRead("/x.v1.BackupService/" + name); isRead == (name == "RunBackup") {
			t.Errorf("%s: rpcIsRead = %v", name, isRead)
		}
	}
	if len(got) != len(want) {
		t.Errorf("methods = %v, want %v", got, want)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing %s", name)
		}
	}
}

type runners []string

func (r runners) MayRun(groups []string) bool {
	for _, g := range groups {
		if slices.Contains(r, g) {
			return true
		}
	}
	return false
}

func runnerConsole(m BackupModule, groups ...string) *Console {
	return &Console{deps: ConsoleDeps{Backup: m, BackupRunners: runners(groups)}}
}

// A backup is started by a group the policy names, and by nothing else: the
// operator role, which opens every other write, does not.
func TestRunBackupNeedsAGroupThePolicyNames(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	f.status.Unfinished = nil
	c := runnerConsole(f, "backup-runners")
	req := func() *connect.Request[directoryrosterv1.RunBackupRequest] {
		return connect.NewRequest(&directoryrosterv1.RunBackupRequest{})
	}

	if _, err := c.RunBackup(context.Background(), req()); code(err) != connect.CodeUnauthenticated {
		t.Errorf("no sign-in: %v", err)
	}
	for name, ctx := range map[string]context.Context{
		"an operator in no group": asRole(access.RoleOperator),
		"a viewer in another":     asRole(access.RoleViewer, "someone-else"),
		"no role":                 asRole(access.RoleNone),
	} {
		if _, err := c.RunBackup(ctx, req()); code(err) != connect.CodePermissionDenied {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.ran) != 0 {
		t.Fatalf("the module was called %d times for callers the policy does not admit", len(f.ran))
	}

	res, err := c.RunBackup(asRole(access.RoleNone, "backup-runners"), req())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Msg.GetAvailable() || res.Msg.GetOutcome() != "started" {
		t.Errorf("run = %v", res.Msg)
	}
	// The module is told who asked, which is what its audit record names.
	if len(f.ran) != 1 || f.ran[0] != "ada@example.com" {
		t.Errorf("run was for %v", f.ran)
	}
}

func TestNobodyMayRunABackupWhenThePolicyNamesNoGroup(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	for name, c := range map[string]*Console{
		"no runners":         {deps: ConsoleDeps{Backup: f}},
		"an empty group set": runnerConsole(f),
	} {
		_, err := c.RunBackup(asRole(access.RoleOperator, policy.GroupOperators, "backup-runners"), connect.NewRequest(&directoryrosterv1.RunBackupRequest{}))
		if code(err) != connect.CodePermissionDenied {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.ran) != 0 {
		t.Errorf("the module was called: %v", f.ran)
	}
}

func runReq(resume bool) *connect.Request[directoryrosterv1.RunBackupRequest] {
	return connect.NewRequest(&directoryrosterv1.RunBackupRequest{Resume: resume})
}

// A run is started and the answer comes at once, with the issuer's clock for the
// caller to find the run by; the run itself is read back with the status.
func TestRunBackupAnswersWhenTheModuleHasTheRun(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	f.status.Unfinished = nil
	c := runnerConsole(f, "r")
	res, err := c.RunBackup(asRole(access.RoleNone, "r"), runReq(false))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetOutcome() != "started" || res.Msg.GetStarted() == nil || res.Msg.GetId() != "" {
		t.Errorf("answer = %v", res.Msg)
	}
	if len(f.ran) != 1 || f.ran[0] != "ada@example.com" {
		t.Errorf("the module was told %v", f.ran)
	}
}

func TestRunBackupIsContendedWhileARunIsGoing(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	f.status.Unfinished = &job.View{ID: "x", State: job.StateRunning}
	res, err := runnerConsole(f, "r").RunBackup(asRole(access.RoleNone, "r"), runReq(false))
	if err != nil || res.Msg.GetOutcome() != "contended" || len(f.ran) != 0 {
		t.Errorf("%v, %v, %v", res, err, f.ran)
	}
}

// One start per person per window: the second asks the module nothing.
func TestARepeatedRunBackupByOnePersonIsContended(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	f.status.Unfinished = nil
	c := runnerConsole(f, "r")
	if res, err := c.RunBackup(asRole(access.RoleNone, "r"), runReq(false)); err != nil || res.Msg.GetOutcome() != "started" {
		t.Fatalf("first: %v, %v", res, err)
	}
	res, err := c.RunBackup(asRole(access.RoleNone, "r"), runReq(false))
	if err != nil || res.Msg.GetOutcome() != "contended" || len(f.ran) != 1 {
		t.Errorf("second: %v, %v, %v", res, err, f.ran)
	}
	// Someone else is not held by it.
	other := WithIdentity(context.Background(), access.Identity{Email: "bob@example.com", Groups: []string{"r"}})
	if res, err = c.RunBackup(other, runReq(false)); err != nil || res.Msg.GetOutcome() != "started" {
		t.Errorf("another person: %v, %v", res, err)
	}
}

func TestAFailedStartDoesNotHoldThePersonOff(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	f.status.Unfinished = nil
	f.startErr = errors.New("lambda: throttled")
	c := runnerConsole(f, "r")
	if _, err := c.RunBackup(asRole(access.RoleNone, "r"), runReq(false)); code(err) != connect.CodeUnavailable {
		t.Fatalf("code = %v", code(err))
	}
	f.startErr = nil
	if res, err := c.RunBackup(asRole(access.RoleNone, "r"), runReq(false)); err != nil || res.Msg.GetOutcome() != "started" {
		t.Errorf("retry: %v, %v", res, err)
	}
}

func TestAResumeHasNothingToContinue(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	res, err := runnerConsole(f, "r").RunBackup(asRole(access.RoleNone, "r"), runReq(true))
	if err != nil || res.Msg.GetOutcome() != "idle" || len(f.ran) != 0 {
		t.Errorf("%v, %v, %v", res, err, f.ran)
	}
}

func TestPreviewRefusesAnIdThatIsAPath(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	c := backupConsole(f)
	for _, id := range []string{"..", "../x", "a/b", "a%2Fb", `a\b`, ".hidden", "a b", "a\x00b", strings.Repeat("a", 65), "x/../../y"} {
		_, err := c.GetRestorePreview(asRole(access.RoleOperator), connect.NewRequest(&directoryrosterv1.GetRestorePreviewRequest{BackupId: id}))
		if code(err) != connect.CodeInvalidArgument {
			t.Errorf("%q: %v", id, err)
		}
	}
}

func TestRunBackupRefusesASignInThatCannotBeNamed(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	c := runnerConsole(f, "r")
	ctx := WithIdentity(context.Background(), access.Identity{Source: access.SourceRecovery, Subject: "system:serviceaccount:ns:recovery", Groups: []string{"r"}})
	if _, err := c.RunBackup(ctx, connect.NewRequest(&directoryrosterv1.RunBackupRequest{})); code(err) != connect.CodePermissionDenied {
		t.Errorf("recovery: %v", err)
	}
	if len(f.ran) != 0 {
		t.Errorf("the module was called: %v", f.ran)
	}
}

func TestRestorePreviewIsAnOperatorsAndCarriesCountsOnly(t *testing.T) {
	t.Parallel()
	f := newFakeBackup()
	f.preview = rpc.PreviewResponse{Outcome: "preview", Backup: "new", Report: &restore.Report{Installation: "example", ID: "new", Layout: "v5",
		Created: "2026-10-10T02:00:00Z", Sections: []restore.SectionReport{{Module: port.ModuleOIDC, Section: "state", Create: 2, Overwrite: 1, Same: 7}}}}
	c := backupConsole(f)
	req := func(id string) *connect.Request[directoryrosterv1.GetRestorePreviewRequest] {
		return connect.NewRequest(&directoryrosterv1.GetRestorePreviewRequest{BackupId: id})
	}
	if _, err := c.GetRestorePreview(context.Background(), req("new")); code(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous: %v", err)
	}
	if _, err := c.GetRestorePreview(asRole(access.RoleViewer), req("new")); code(err) != connect.CodePermissionDenied {
		t.Errorf("viewer: %v", err)
	}
	if _, err := c.GetRestorePreview(asRole(access.RoleOperator), req(" ")); code(err) != connect.CodeInvalidArgument {
		t.Errorf("no id: %v", err)
	}
	res, err := c.GetRestorePreview(asRole(access.RoleOperator), req("new"))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Msg
	if !m.GetAvailable() || m.GetOutcome() != "preview" || m.GetBackupId() != "new" || len(m.GetSections()) != 1 ||
		m.GetSections()[0].GetCreate() != 2 || m.GetSections()[0].GetOverwrite() != 1 || m.GetSections()[0].GetModule() != "oidc" {
		t.Errorf("preview = %v", m)
	}
}

func TestWhoamiSaysWhetherTheConsoleHasABackupsPage(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{false, true} {
		console := backupConsole(nil)
		if on {
			console.deps.Backup = newFakeBackup()
		}
		s := &ConsoleServer{console: console, log: slog.New(slog.DiscardHandler)}
		r := httptest.NewRequest(http.MethodGet, "/.access/whoami", nil).WithContext(asRole(access.RoleViewer))
		w := httptest.NewRecorder()
		s.whoami(w, r)
		if got := strings.Contains(w.Body.String(), `"backup":true`); got != on {
			t.Errorf("backups page = %v with the module %v: %s", got, on, w.Body.String())
		}
	}
}

// UseBackup is what the document's console.backup turns on.
func TestUseBackupConnectsThePage(t *testing.T) {
	t.Parallel()
	s := &ConsoleServer{console: backupConsole(nil)}
	s.UseBackup(newFakeBackup())
	if s.console.deps.Backup == nil {
		t.Error("UseBackup connected nothing")
	}
}
