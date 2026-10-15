package rpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/backup/rpc"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/port"
)

type restoreFake struct{}

func (restoreFake) RestoreStatus(context.Context) (restorejob.Status, error) {
	r := restorejob.Run{ID: "20261010T030000Z-a1b2c3", BackupID: "20261010T020000Z-a1b2c3", State: restorejob.StatePaused,
		Started: at, Updated: at, Maintenance: restorejob.MaintenanceOn, Slices: 1, Reason: "time"}
	return restorejob.Status{Latest: &r, Unfinished: &r, Maintenance: []restorejob.ModuleFlag{{Module: port.ModuleOIDC, State: "restoring", Since: at}}}, nil
}

// PreviewRestore answers a report that lists a record by name, which must not
// cross to the console, and refuses the backup "missing" with a cause that
// names a bucket, which must not either.
func (restoreFake) PreviewRestore(_ context.Context, id string) (*restore.Report, error) {
	if id == "missing" {
		return nil, errors.New("s3://secret-bucket/key: no such object")
	}
	return &restore.Report{Installation: "example", ID: id, Layout: "v5", Created: "2026-10-10T02:00:00Z", Sections: []restore.SectionReport{{
		Module: port.ModuleOIDC, Section: "state", Create: 2, Overwrite: 1, Same: 7,
		Items: []restore.Item{{Type: restore.TypeState, Name: "clients/secret-name", Action: restore.Create}}, Truncated: true,
	}}}, nil
}

func restoreServer() *modcall.Server {
	s := modcall.NewServer(rpc.RestoreModule)
	rpc.RegisterRestore(s, restoreFake{})
	return s
}

func TestRestoreStatusIsReadByTheConsoleAndAdminsAndNoOneElse(t *testing.T) {
	s := restoreServer()
	for _, c := range []struct {
		caller string
		ok     bool
	}{{rpc.CallerConsole, true}, {rpc.CallerAdmin, true}, {rpc.CallerBreakglass, true}, {"issuer", false}, {"github", false}, {"", false}} {
		req := modcall.Request{V: 2, Kind: modcall.Kind, Module: rpc.RestoreModule, Method: rpc.MethodRestoreStatus, Payload: json.RawMessage(`{}`)}
		resp := s.Dispatch(modcall.WithCaller(context.Background(), c.caller), req)
		if got := resp.Error == nil; got != c.ok {
			t.Errorf("%q reads restore.status: allowed %v, want %v", c.caller, got, c.ok)
		}
		if resp.Error != nil && resp.Error.Code != modcall.CodeForbidden {
			t.Errorf("%q: code %q", c.caller, resp.Error.Code)
		}
	}
}

// The console can read how a restore stands and what one would do, and can do
// nothing else: the module has no method that starts or resumes one.
func TestTheRestoreModuleHasNoMethodThatStartsOrResumes(t *testing.T) {
	s := restoreServer()
	for _, method := range []string{"start", "run", "resume", "apply"} {
		req := modcall.Request{V: 2, Kind: modcall.Kind, Module: rpc.RestoreModule, Method: method, Payload: json.RawMessage(`{}`)}
		if resp := s.Dispatch(modcall.WithCaller(context.Background(), rpc.CallerAdmin), req); resp.Error == nil {
			t.Errorf("restore.%s is answered", method)
		}
	}
}

func TestRestoreStatusAnswersTheRecordAndTheFlags(t *testing.T) {
	req := modcall.Request{V: 2, Kind: modcall.Kind, Module: rpc.RestoreModule, Method: rpc.MethodRestoreStatus, Payload: json.RawMessage(`{}`)}
	resp := restoreServer().Dispatch(modcall.WithCaller(context.Background(), rpc.CallerConsole), req)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	var st restorejob.Status
	if err := json.Unmarshal(resp.Result, &st); err != nil || st.Unfinished == nil || st.Unfinished.Maintenance != "on" ||
		len(st.Maintenance) != 1 || st.Maintenance[0].Module != port.ModuleOIDC {
		t.Errorf("%s, %v", resp.Result, err)
	}
}

func preview(t *testing.T, caller, backup string) modcall.Response {
	t.Helper()
	req := modcall.Request{V: 2, Kind: modcall.Kind, Module: rpc.RestoreModule, Method: rpc.MethodRestorePreview,
		Payload: json.RawMessage(`{"backup":"` + backup + `"}`)}
	return restoreServer().Dispatch(modcall.WithCaller(context.Background(), caller), req)
}

func TestRestorePreviewIsAnsweredToTheConsoleAndAdminsAndNoOneElse(t *testing.T) {
	for _, c := range []struct {
		caller string
		ok     bool
	}{{rpc.CallerConsole, true}, {rpc.CallerAdmin, true}, {rpc.CallerBreakglass, true}, {"issuer", false}, {"", false}} {
		resp := preview(t, c.caller, "20261010T020000Z-a1b2c3")
		if got := resp.Error == nil; got != c.ok {
			t.Errorf("%q previews: allowed %v, want %v", c.caller, got, c.ok)
		}
	}
}

// The report that crosses is counts: no record's name, which would tell the
// console what the archive holds beyond the numbers.
func TestRestorePreviewCrossesAsCountsOnly(t *testing.T) {
	resp := preview(t, rpc.CallerConsole, "20261010T020000Z-a1b2c3")
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	var out rpc.PreviewResponse
	if err := json.Unmarshal(resp.Result, &out); err != nil || out.Outcome != rpc.PreviewOutcome || out.Report == nil {
		t.Fatalf("%s, %v", resp.Result, err)
	}
	sec := out.Report.Sections[0]
	if sec.Create != 2 || sec.Overwrite != 1 || sec.Same != 7 || len(sec.Items) != 0 || sec.Truncated {
		t.Errorf("section %+v", sec)
	}
	if strings.Contains(string(resp.Result), "secret-name") {
		t.Errorf("a record's name crossed: %s", resp.Result)
	}
}

func TestRestorePreviewRefusalCarriesNoCause(t *testing.T) {
	resp := preview(t, rpc.CallerConsole, "missing")
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	var out rpc.PreviewResponse
	if err := json.Unmarshal(resp.Result, &out); err != nil || out.Outcome != restorejob.OutcomeRefused || out.Report != nil {
		t.Fatalf("%s, %v", resp.Result, err)
	}
	if strings.Contains(string(resp.Result), "secret-bucket") {
		t.Errorf("the cause crossed: %s", resp.Result)
	}
}
