package dynamodb

import (
	"context"

	"github.com/truvity/sluis/internal/port"
)

// DynamoDB keeps State and the Trigger in one table, with the platform's
// credentials. It runs on every runtime.
func init() {
	for _, c := range []struct {
		concern port.Concern
		summary string
	}{
		{port.ConcernState, "State, sessions included, in one DynamoDB table with per-item TTL."},
		{port.ConcernTrigger, "Notifications over the same table."},
	} {
		port.Register(port.Descriptor{
			Name: "dynamodb", Concern: c.concern, Summary: c.summary,
			Requires: port.Requires{AWS: true},
			Factory: func(ctx context.Context, s port.Settings) (any, error) {
				var cfg Config
				if err := s.Decode(&cfg); err != nil {
					return nil, err
				}
				return Open(ctx, cfg)
			},
		})
	}
}
