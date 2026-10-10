package rpc_test

import (
	"testing"

	"github.com/truvity/sluis/internal/backup/rpc"
	"github.com/truvity/sluis/internal/modcall"
)

// The client reaches all three read methods as the console, through the same
// server the module registers, and the list honours its limit.
func TestTheConsoleClientReadsTheThreeMethods(t *testing.T) {
	back := modcall.NewServer(rpc.Module)
	rpc.Register(back, &fake{})
	rest := modcall.NewServer(rpc.RestoreModule)
	rpc.RegisterRestore(rest, restoreFake{})
	c := rpc.NewClient(modcall.Local{rpc.Module: back, rpc.RestoreModule: rest}.As(rpc.CallerConsole))

	if _, err := c.Status(t.Context()); err != nil {
		t.Errorf("status: %v", err)
	}
	got, err := c.List(t.Context(), 1)
	if err != nil || len(got) != 1 {
		t.Errorf("list = %v, %v; want 1", got, err)
	}
	st, err := c.RestoreStatus(t.Context())
	if err != nil || st.Unfinished == nil || len(st.Maintenance) != 1 {
		t.Errorf("restore status = %+v, %v", st, err)
	}
}
