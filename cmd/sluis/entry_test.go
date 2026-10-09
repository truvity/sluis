package main

import "testing"

// Whatever the build, a command line is never taken for the Lambda runtime
// unless the runtime says so: a developer's `sluis` with no arguments is a
// usage error, not a hung process waiting for an invocation.
func TestThePlatformEntryNeedsTheRuntime(t *testing.T) {
	none := func(string) string { return "" }
	if platformEntry(nil, none) {
		t.Error("no arguments and no runtime API took the platform entry")
	}
	if platformEntry([]string{"serve"}, func(string) string { return "127.0.0.1:9001" }) {
		t.Error("a command line took the platform entry")
	}
}
