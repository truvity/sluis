package chart_test

import (
	"fmt"
	"os"
	"testing"
)

// The chart is the repository's top-level charts/audit; every test here names
// its files relative to it, as they did when the tests lived beside the chart.
func TestMain(m *testing.M) {
	if err := os.Chdir("../../../charts/audit"); err != nil {
		fmt.Fprintln(os.Stderr, "the chart directory:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
