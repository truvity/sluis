package rosterapp

import (
	"testing"

	"github.com/truvity/sluis/internal/config"
)

func TestTheIssuersConfigurationNamesOnlyFunctionsALaptopCanInvoke(t *testing.T) {
	const (
		backupARN  = "arn:aws:lambda:eu-west-1:111122223333:function:example-backup"
		restoreARN = "arn:aws:lambda:eu-west-1:111122223333:function:example-restore"
	)
	for name, tc := range map[string]struct {
		in              config.ConsoleBackup
		backup, restore string
		region          string
	}{
		"two ARNs":        {config.ConsoleBackup{Function: backupARN, RestoreFunction: restoreARN}, backupARN, restoreARN, "eu-west-1"},
		"a qualified ARN": {config.ConsoleBackup{Function: backupARN + ":live-console"}, backupARN, "", "eu-west-1"},
		"bare names":      {config.ConsoleBackup{Function: "example-backup", RestoreFunction: "example-restore"}, "", "", ""},
		"cluster URLs":    {config.ConsoleBackup{URL: "http://backup.example", RestoreURL: "http://restore.example"}, "", "", ""},
		"another service": {config.ConsoleBackup{Function: "arn:aws:s3:::bucket"}, "", "", ""},
	} {
		doc := backupConfiguration(Config{Backup: &tc.in, BackupName: "example"})
		var backup, restore string
		if doc.Backup != nil {
			backup = doc.Backup.Endpoint
		}
		if doc.Restore != nil {
			restore = doc.Restore.Endpoint
		}
		if backup != tc.backup || restore != tc.restore || doc.Region != tc.region || doc.Instance != "example" {
			t.Errorf("%s: %+v", name, doc)
		}
	}
}
