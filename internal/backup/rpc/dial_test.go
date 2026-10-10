package rpc_test

import (
	"testing"

	"github.com/truvity/sluis/internal/backup/rpc"
	"github.com/truvity/sluis/internal/config"
)

func TestDialNeedsTheSectionAndBuildsAClientFromAService(t *testing.T) {
	if _, err := rpc.Dial(t.Context(), nil); err == nil {
		t.Error("no section dialled")
	}
	c, err := rpc.Dial(t.Context(), &config.ConsoleBackup{URL: "https://backup.example", RestoreURL: "https://restore.example", TokenFile: "/nonexistent"})
	if err != nil || c == nil {
		t.Fatalf("dial = %v, %v", c, err)
	}
}
