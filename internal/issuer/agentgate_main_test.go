package issuer_test

import (
	"os"
	"testing"

	"github.com/truvity/sluis/internal/agentgate"
)

// TestMain opens the agent-class gate for this test binary: the machinery
// behind `session: agent` is tested here while every real load refuses it
// (see internal/agentgate).
func TestMain(m *testing.M) {
	agentgate.OpenForTests(m)
	os.Exit(m.Run())
}
