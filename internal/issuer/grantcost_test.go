package issuer_test

import (
	"testing"

	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/port/porttest/grantcost"
)

// What one grant costs the storage ports, over the in-memory adapter: the
// State and Index calls, the directory snapshot reads and the times the hub
// is asked about the person, held to grantcost.Budgets. The DynamoDB adapter
// runs the same budget in internal/port/dynamodb, over its fake and over
// LocalStack.
//
// An installation where MCP clients refresh every few minutes pays for every
// refresh, so the refresh_token budget is the one that matters; the observed
// counts and every call behind them are logged (go test -v) so a change
// shows its before and after.
func TestGrantCostOverTheMemoryAdapter(t *testing.T) {
	// Not parallel: the harness builds its own provider and serves it unguarded,
	// which writes zitadel/oidc's shared default endpoints (see provider_guard_test.go).
	grantcost.Run(t, func(*testing.T) grantcost.Env {
		s := memory.New()
		return grantcost.Env{Set: s.Set(), Advance: s.Advance}
	})
}
