package config_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/policy"
)

func backupPolicy(groups ...string) *config.PolicyDocument {
	d := config.NewPolicyDocument(policy.Policy{Groups: map[string]policy.Group{"all:infra:backup": {}, "all:infra:other": {}}})
	d.BackupRun = &config.PolicyBackup{RunGroups: groups}
	return d
}

func TestBackupRunGroupsAreHeldToThePolicysGroups(t *testing.T) {
	if err := backupPolicy("all:infra:backup").Validate(); err != nil {
		t.Fatalf("a declared group: %v", err)
	}
	for name, tc := range map[string]struct {
		groups []string
		want   string
	}{
		"an undeclared group": {[]string{"nobody"}, "not declared"},
		"an empty name":       {[]string{""}, "name the group"},
		"twice":               {[]string{"all:infra:backup", "all:infra:backup"}, "twice"},
	} {
		err := backupPolicy(tc.groups...).Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
}

// Nobody is implied: an empty section, no section and a nil one let no one run.
func TestWhoMayRunABackup(t *testing.T) {
	p := backupPolicy("all:infra:backup").Backup()
	if !p.MayRun([]string{"x", "all:infra:backup"}) || p.MayRun([]string{"all:infra:other", policy.GroupOperators}) || p.MayRun(nil) {
		t.Error("MayRun disagrees with the group list")
	}
	for name, b := range map[string]*config.PolicyBackup{
		"nil": nil, "empty": {}, "unset": config.NewPolicyDocument(policy.Policy{}).Backup(),
	} {
		if b.MayRun([]string{policy.GroupOperators, "all:infra:backup", ""}) {
			t.Errorf("%s section let someone run", name)
		}
	}
}
