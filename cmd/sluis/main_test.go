package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// The command surface is fixed: these are the commands a deployment is
// written against, and each is refused cleanly when it is wrong.
func TestTheCommandSurface(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"serve", "--help"}, {"serve", "--version"},
		{"controller", "github", "--help"}, {"controller", "slack", "--version"}} {
		var out bytes.Buffer
		if err := run(args, &out); err != nil {
			t.Errorf("%v: %v", args, err)
		}
		if out.Len() == 0 {
			t.Errorf("%v: printed nothing", args)
		}
	}
}

func TestAWrongCommandIsAUsageError(t *testing.T) {
	for name, args := range map[string][]string{
		"none":                  {},
		"unknown":               {"tick"},
		"controller alone":      {"controller"},
		"controller unknown":    {"controller", "gitlab"},
		"a retired binary name": {"access-issuer"},
	} {
		var out bytes.Buffer
		if err := run(args, &out); !errors.Is(err, errUsage) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSubcommandsNeedTheirFile(t *testing.T) {
	for _, args := range [][]string{{"serve"}, {"controller", "github"}, {"controller", "slack"}} {
		var out bytes.Buffer
		err := run(args, &out)
		if err == nil || !strings.Contains(err.Error(), "--config") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// The console is part of the issuer's process; it is not a command of its own.
func TestTheConsoleIsNotACommand(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"console"}, &out); !errors.Is(err, errUsage) {
		t.Errorf("sluis console: %v", err)
	}
}

func TestAModuleWithoutAProcessSaysNotYetSplit(t *testing.T) {
	for _, module := range []string{"google"} {
		var out bytes.Buffer
		err := run([]string{module}, &out)
		if !errors.Is(err, errNotSplit) {
			t.Errorf("%s: %v", module, err)
		}
	}
}

// A module that has a role today runs that role's command: with no file it
// refuses exactly as the command does.
func TestAModuleWithARoleRunsTodaysCommand(t *testing.T) {
	for _, module := range []string{"issuer", "github", "slack", "cloudflare"} {
		var out bytes.Buffer
		err := run([]string{module}, &out)
		if err == nil || !strings.Contains(err.Error(), "--config") {
			t.Errorf("%s: %v", module, err)
		}
		out.Reset()
		if err := run([]string{module, "--help"}, &out); err != nil || out.Len() == 0 {
			t.Errorf("%s --help: %v, %q", module, err, out.String())
		}
	}
}

func TestVersionNamesTheBinary(t *testing.T) {
	for _, arg := range []string{"--version", "version"} {
		var out bytes.Buffer
		if err := run([]string{arg}, &out); err != nil || !strings.HasPrefix(out.String(), "sluis ") {
			t.Errorf("%s: %v, %q", arg, err, out.String())
		}
	}
}

func TestAnUnknownFlagOrCommandIsAUsageError(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"bogus"}, {"tick"}} {
		var out bytes.Buffer
		if err := run(args, &out); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// The module commands tick one target like `tick` does: no target, a usage error.
func TestAModuleTickNeedsATarget(t *testing.T) {
	for _, module := range []string{"github", "slack", "cloudflare"} {
		var out bytes.Buffer
		if err := run([]string{module, "tick", "--config", "x.yaml"}, &out); !errors.Is(err, errUsage) {
			t.Errorf("%s tick: %v", module, err)
		}
	}
}

// The backup command is a tree of its own: a command is needed, an unknown one
// is a usage error, and each command asks for its file like any other.
func TestTheBackupCommand(t *testing.T) {
	for _, args := range [][]string{{"backup"}, {"backup", "restore"}, {"backup", "--json"}} {
		var out bytes.Buffer
		if err := run(args, &out); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
	for _, sub := range []string{"run", "list", "status", "prune"} {
		var out bytes.Buffer
		if err := run([]string{"backup", sub}, &out); err == nil || !strings.Contains(err.Error(), "--config") {
			t.Errorf("backup %s: %v", sub, err)
		}
	}
	var out bytes.Buffer
	if err := run([]string{"backup", "--help"}, &out); err != nil || !strings.Contains(out.String(), "prune [--dry-run]") {
		t.Errorf("backup --help: %v, %q", err, out.String())
	}
	// A flag that belongs to another command is the configuration line's to refuse.
	out.Reset()
	if err := run([]string{"backup", "list", "--dry-run", "--config", "x"}, &out); err == nil {
		t.Error("backup list took --dry-run")
	}
}

// The restore command is a tree of its own: a command is needed, `start` needs
// a backup (or --resume, not both), `preview` needs one, and each asks for its
// file like any other command.
func TestTheRestoreCommand(t *testing.T) {
	for _, args := range [][]string{
		{"restore"}, {"restore", "--json"}, {"restore", "apply"}, {"restore", "preview"}, {"restore", "start"},
		{"restore", "start", "b1", "--resume"}, {"restore", "status", "b1"}, {"restore", "start", "b1", "--confirm"},
		{"restore", "start", "b1", "--overwrite=yes"},
	} {
		var out bytes.Buffer
		if err := run(args, &out); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"restore", "status"}, {"restore", "preview", "b1"}, {"restore", "start", "b1", "--confirm", "x", "--overwrite"},
		{"restore", "start", "--resume"},
	} {
		var out bytes.Buffer
		if err := run(args, &out); err == nil || !strings.Contains(err.Error(), "--config") {
			t.Errorf("%v: %v", args, err)
		}
	}
	var out bytes.Buffer
	if err := run([]string{"restore", "--help"}, &out); err != nil || !strings.Contains(out.String(), "--overwrite") {
		t.Errorf("restore --help: %v, %q", err, out.String())
	}
	// A flag of another command is the configuration line's to refuse.
	out.Reset()
	if err := run([]string{"restore", "status", "--overwrite", "--config", "x"}, &out); err == nil {
		t.Error("restore status took --overwrite")
	}
}

func TestAPinnedBuildRunsOnlyItsModulesCommands(t *testing.T) {
	for _, tc := range []struct {
		pin  string
		args []string
		ok   bool
	}{
		{"", []string{"backup", "run"}, true},
		{"issuer", []string{"serve", "--config=x"}, true},
		{"issuer", []string{"backup", "run"}, false},
		{"backup", []string{"backup", "run"}, true},
		{"backup", []string{"restore", "start"}, true},
		{"backup", []string{"serve"}, false},
		{"cloudflare", []string{"cloudflare"}, true},
		{"cloudflare", []string{"restore"}, false},
		{"backup", []string{"--version"}, true},
		{"backup", []string{"version"}, true},
		{"nonsense", []string{"serve"}, false},
	} {
		if err := checkPin(tc.pin, tc.args); (err == nil) != tc.ok {
			t.Errorf("checkPin(%q, %v) = %v, want ok=%v", tc.pin, tc.args, err, tc.ok)
		}
	}
}
