package profile

import (
	"strings"
	"testing"
)

func TestCheckFrameworksLock(t *testing.T) {
	if err := CheckFrameworksLock([]string{"security", "history", "billing-nl"}, "NONE"); err != nil {
		t.Fatalf("lock-free framework profiles on a NONE bucket: %v", err)
	}

	if err := CheckFrameworksLock([]string{"security", "dora"}, "NONE"); err == nil {
		t.Fatal("dora demands compliance: want a refusal on NONE")
	}

	if err := CheckFrameworksLock([]string{"dora"}, "COMPLIANCE"); err != nil {
		t.Fatalf("dora on COMPLIANCE: %v", err)
	}

	if err := CheckFrameworksLock([]string{"nope"}, "none"); err == nil {
		t.Fatal("an unknown framework profile is refused")
	}

	if err := CheckFrameworksLock([]string{"security"}, "weird"); err == nil {
		t.Fatal("an unknown mode is refused")
	}
}

func TestTheOldPresetsKeyIsRefusedByName(t *testing.T) {
	_, err := ParseDeployment([]byte("apiVersion: " + DeploymentAPIVersion + "\nprofiles:\n  security:\n    presets: [security]\n"))
	if err == nil || !strings.Contains(err.Error(), "`presets` is now `frameworks`") {
		t.Fatalf("the old key was not refused by name: %v", err)
	}
	if _, err := ParseDeployment([]byte("apiVersion: " + DeploymentAPIVersion + "\nprofiles:\n  security:\n    frameworks: [security]\n")); err != nil {
		t.Fatalf("the new key was refused: %v", err)
	}
}
