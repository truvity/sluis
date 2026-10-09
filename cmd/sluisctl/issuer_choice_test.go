package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	choiceNorth = "https://access.north.example"
	choiceSouth = "https://access.south.example"
)

func choiceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(envIssuer, "")
}

func choiceSession(t *testing.T, issuer string, expires time.Time) {
	t.Helper()
	if err := saveSession(issuer, Session{RefreshToken: "r-" + issuer, Expires: expires}); err != nil {
		t.Fatalf("save %s: %v", issuer, err)
	}
}

func choiceConfig(t *testing.T, body string) {
	t.Helper()
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTwoLiveSessionsAndNoPinIsRefused(t *testing.T) {
	choiceEnv(t)
	choiceSession(t, choiceNorth, time.Time{})
	choiceSession(t, choiceSouth, time.Time{})
	// The last login is not a pin.
	choiceConfig(t, "lastIssuer: "+choiceSouth+"\n")

	_, err := loadConfig("", "")
	if err == nil {
		t.Fatal("two live sessions and no pin were accepted")
	}
	for _, want := range []string{choiceNorth, choiceSouth, "--issuer", "`issuer: <url>`", envIssuer} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not mention %q", err, want)
		}
	}
	if code := codeFor(err); code != codeFor(badUsage("x")) {
		t.Errorf("exit code %d is not the usage one", code)
	}
}

func TestOneLiveSessionIsUsed(t *testing.T) {
	choiceEnv(t)
	choiceSession(t, choiceNorth, time.Time{})
	choiceConfig(t, "lastIssuer: "+choiceSouth+"\n")

	cfg, err := loadConfig("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Issuer != choiceNorth {
		t.Errorf("issuer = %q, want the one live session", cfg.Issuer)
	}
}

func TestNoLiveSessionFallsBackToTheLastLogin(t *testing.T) {
	choiceEnv(t)
	choiceConfig(t, "lastIssuer: "+choiceSouth+"/\n")

	cfg, err := loadConfig("", "")
	if err != nil || cfg.Issuer != choiceSouth {
		t.Errorf("issuer = %q, err = %v", cfg.Issuer, err)
	}
}

func TestExplicitIssuerBeatsAmbiguity(t *testing.T) {
	choiceEnv(t)
	choiceSession(t, choiceNorth, time.Time{})
	choiceSession(t, choiceSouth, time.Time{})

	cfg, err := loadConfig(choiceNorth+"/", "")
	if err != nil || cfg.Issuer != choiceNorth {
		t.Errorf("issuer = %q, err = %v", cfg.Issuer, err)
	}
}

func TestEnvIssuerPins(t *testing.T) {
	choiceEnv(t)
	choiceSession(t, choiceNorth, time.Time{})
	choiceSession(t, choiceSouth, time.Time{})
	t.Setenv(envIssuer, choiceSouth)

	cfg, err := loadConfig("", "")
	if err != nil || cfg.Issuer != choiceSouth {
		t.Errorf("issuer = %q, err = %v", cfg.Issuer, err)
	}
}

func TestConfigIssuerPins(t *testing.T) {
	choiceEnv(t)
	choiceSession(t, choiceNorth, time.Time{})
	choiceSession(t, choiceSouth, time.Time{})
	choiceConfig(t, "issuer: "+choiceNorth+"\nlastIssuer: "+choiceSouth+"\n")

	cfg, err := loadConfig("", "")
	if err != nil || cfg.Issuer != choiceNorth {
		t.Errorf("issuer = %q, err = %v", cfg.Issuer, err)
	}
	// The flag still wins over the pin.
	cfg, err = loadConfig(choiceSouth, "")
	if err != nil || cfg.Issuer != choiceSouth {
		t.Errorf("issuer = %q, err = %v", cfg.Issuer, err)
	}
}

func TestAnExpiredSessionDoesNotCount(t *testing.T) {
	choiceEnv(t)
	choiceSession(t, choiceNorth, time.Time{})
	choiceSession(t, choiceSouth, time.Now().Add(-time.Hour))

	cfg, err := loadConfig("", "")
	if err != nil || cfg.Issuer != choiceNorth {
		t.Errorf("issuer = %q, err = %v", cfg.Issuer, err)
	}
}

func TestLoginWithoutIssuerReusesTheLastLoginEvenWithTwoSessions(t *testing.T) {
	choiceEnv(t)
	choiceSession(t, choiceNorth, time.Time{})
	choiceSession(t, choiceSouth, time.Time{})
	choiceConfig(t, "lastIssuer: "+choiceSouth+"\n")

	cfg, err := loadLoginConfig("", "")
	if err != nil || cfg.Issuer != choiceSouth {
		t.Errorf("issuer = %q, err = %v", cfg.Issuer, err)
	}
}
