package rpc_test

import (
	"context"
	"encoding/json"
	"testing"

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

// The console can read how a restore stands and can do nothing else: the module
// has no method that starts, resumes or previews one.
func TestTheRestoreModuleHasNoMethodButStatus(t *testing.T) {
	s := restoreServer()
	for _, method := range []string{"start", "run", "preview", "resume", "apply"} {
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
