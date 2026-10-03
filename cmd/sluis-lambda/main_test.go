package main

import "testing"

// SLUIS_* is read first and ACCESS_ROSTER_* is the fallback, for the
// extension's settings only.
func TestGetenvPrefersTheNewNameAndFallsBackToTheOld(t *testing.T) {
	t.Setenv("ACCESS_ROSTER_ISSUER", "https://old.example")
	if got := getenv("ACCESS_ROSTER_ISSUER"); got != "https://old.example" {
		t.Fatalf("only the old name set: got %q", got)
	}
	t.Setenv("SLUIS_ISSUER", "https://new.example")
	if got := getenv("ACCESS_ROSTER_ISSUER"); got != "https://new.example" {
		t.Fatalf("both set: got %q, want the SLUIS_ one", got)
	}
	t.Setenv("SLUIS_AUDIENCE", "")
	t.Setenv("ACCESS_ROSTER_AUDIENCE", "aud")
	if got := getenv("ACCESS_ROSTER_AUDIENCE"); got != "aud" {
		t.Fatalf("an empty SLUIS_ value does not shadow the old one: got %q", got)
	}
	t.Setenv("OTEL_SERVICE_NAME", "svc")
	if got := getenv("OTEL_SERVICE_NAME"); got != "svc" {
		t.Fatalf("other variables are read as they are: got %q", got)
	}
}
