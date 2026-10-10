package config_test

import (
	"testing"

	"github.com/truvity/sluis/internal/config"
)

func TestConsoleBackupNamesTheModulesTheConsoleReads(t *testing.T) {
	t.Parallel()
	for name, b := range map[string]*config.ConsoleBackup{
		"unset":        nil,
		"function":     {Function: "backup"},
		"with restore": {Function: "backup", RestoreFunction: "restore"},
		"service":      {URL: "https://backup.example", RestoreURL: "https://restore.example", Audience: "a"},
	} {
		if err := b.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, b := range map[string]*config.ConsoleBackup{
		"neither":      {},
		"both":         {Function: "backup", URL: "https://backup.example"},
		"two restores": {Function: "backup", RestoreFunction: "r", RestoreURL: "https://restore.example"},
		"bad url":      {URL: "backup"},
		"bad restore":  {Function: "backup", RestoreURL: "restore"},
	} {
		if err := b.Validate(); err == nil {
			t.Errorf("%s is accepted", name)
		}
	}
}

func TestAServiceDocumentWithConsoleBackupIsHeldToIt(t *testing.T) {
	t.Parallel()
	ok := &config.Serve{Console: &config.Console{Backup: &config.ConsoleBackup{Function: "backup"}}}
	if err := ok.ValidateCloudflare(); err != nil {
		t.Errorf("a valid section: %v", err)
	}
	bad := &config.Serve{Console: &config.Console{Backup: &config.ConsoleBackup{}}}
	if err := bad.ValidateCloudflare(); err == nil {
		t.Error("an empty console.backup is accepted")
	}
}
