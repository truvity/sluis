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

func TestAModuleWithoutAProcessSaysNotYetSplit(t *testing.T) {
	for _, module := range []string{"console", "cloudflare", "google", "backup"} {
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
	for _, module := range []string{"issuer", "github", "slack"} {
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
