package app

import (
	"testing"
	"time"

	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
)

// The scheduled backup is gated by the flag in the backup module's own table,
// where the restore function writes it, and by no other table's.
func TestTheBackupGateReadsTheBackupTable(t *testing.T) {
	ctx := t.Context()
	flag := maintenance.Flag{State: maintenance.StateRestoring, Since: time.Now().UTC(), By: "test"}
	table := func(mods *memory.Modules) func(port.Module) (port.State, bool) {
		return func(m port.Module) (port.State, bool) { return mods.Store(m), true }
	}

	// The flag is in the backup table: the gate refuses.
	mods := memory.NewModules()
	if err := maintenance.Write(ctx, mods.Store(port.ModuleBackup), flag); err != nil {
		t.Fatal(err)
	}
	if err := backupGate(table(mods)).Writable(ctx); err == nil {
		t.Error("the flag is in the backup table and the gate lets the backup run")
	}

	// The flag is in the oidc table (what the router's fallback reads) and not
	// in the backup table: the gate does not look there.
	mods = memory.NewModules()
	if err := maintenance.Write(ctx, mods.Store(port.ModuleOIDC), flag); err != nil {
		t.Fatal(err)
	}
	if err := backupGate(table(mods)).Writable(ctx); err != nil {
		t.Errorf("a flag in the oidc table stops the backup: %v", err)
	}
}

func TestTheBackupGateIsNilWithoutABackupTable(t *testing.T) {
	if g := backupGate(func(port.Module) (port.State, bool) { return nil, false }); g != nil {
		t.Error("a gate without a table would refuse every run (or panic)")
	}
}
