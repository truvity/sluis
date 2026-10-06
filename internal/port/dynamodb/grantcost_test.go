package dynamodb

import (
	"maps"
	"testing"

	"github.com/truvity/sluis/internal/port/porttest/grantcost"
)

// What one grant costs the table, over the fake: the port calls and the
// DynamoDB requests they became (PutItem, GetItem, Query ...), held to
// grantcost.Budgets. This is what `go test ./...` runs with no network; the
// same budget runs against LocalStack in grantcost_localstack_test.go.
func TestGrantCostOverTheFake(t *testing.T) {
	t.Parallel()
	grantcost.Run(t, func(t *testing.T) grantcost.Env {
		f := newFake()
		s := fakeStore(t, f)
		return grantcost.Env{
			Set:     s.Set(),
			Advance: s.Advance,
			Calls: func() map[string]int {
				f.mu.Lock()
				defer f.mu.Unlock()
				return maps.Clone(f.calls)
			},
		}
	})
}
