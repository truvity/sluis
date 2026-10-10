package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/backup/job"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/port"
)

type fakeBackup struct {
	status  job.Status
	list    []job.Info
	restore restorejob.Status
	err     error
	limits  []int
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

// The console reads and cannot do more: the service has no method that runs a
// backup, restores, or previews a restore. The set is pinned so that adding one
// is a decision the diff shows.
func TestThereIsNoRestoreRouteInTheBackupService(t *testing.T) {
	t.Parallel()
	svc := directoryrosterv1.File_directoryroster_v1_backup_proto.Services().ByName("BackupService")
	want := map[string]bool{"GetBackupStatus": true, "ListBackups": true, "GetRestoreStatus": true}
	got := map[string]bool{}
	for i := range svc.Methods().Len() {
		name := string(svc.Methods().Get(i).Name())
		got[name] = true
		if !rpcIsRead("/x.v1.BackupService/" + name) {
			t.Errorf("%s is not named as a read, so maintenance would not recognise it", name)
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
